package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestSeedPairsJoinPreparationBeforeOrderedPublication(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	entered := make(chan int, 5)
	release := make(chan struct{})
	finished := make(chan error, 1)
	progress := seedProgress{}
	var mu sync.Mutex
	active, peak, published := 0, 0, 0
	seen := make(map[int]int)
	go func() {
		finished <- seedPairs(ctx, 5, &progress, func(call context.Context, index int, p *seedProgress) (preparedRecord, error) {
			mu.Lock()
			active++
			peak = max(peak, active)
			seen[index]++
			mu.Unlock()
			defer func() { mu.Lock(); active--; mu.Unlock() }()
			entered <- index
			select {
			case <-release:
				return preparedRecord{listing: catalog.CatalogListing{ID: fmt.Sprint(index)}}, nil
			case <-call.Done():
				return preparedRecord{}, call.Err()
			}
		}, func(record preparedRecord, p *seedProgress) error {
			mu.Lock()
			defer mu.Unlock()
			if active != 0 || record.listing.ID != fmt.Sprint(published) {
				return errors.New("publication overlaps preparation or changes order")
			}
			published++
			p.Stage = "publish"
			return nil
		})
	}()
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("both preparation workers did not start")
		}
	}
	close(release)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("preparation and publication did not finish")
	}
	if peak != 2 || active != 0 || published != 5 || progress.CompletedLocalRecords != 5 || progress.Stage != "publish" || len(seen) != 5 {
		t.Fatal("worker bound, publication or progress changed", peak, active, published, progress, seen)
	}
	for index := range 5 {
		if seen[index] != 1 {
			t.Fatal("record was skipped or repeated", index, seen)
		}
	}
}

func TestSeedPairsCancelAndJoinFailedPreparation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started := make(chan struct{})
	sentinel := errors.New("synthetic listing failure")
	progress := seedProgress{}
	err := seedPairs(ctx, 5, &progress, func(call context.Context, index int, p *seedProgress) (preparedRecord, error) {
		if index == 0 {
			close(started)
			<-call.Done()
			p.Stage = "version"
			return preparedRecord{}, call.Err()
		}
		select {
		case <-started:
			p.Stage = "listing"
			return preparedRecord{}, sentinel
		case <-call.Done():
			return preparedRecord{}, call.Err()
		}
	}, func(preparedRecord, *seedProgress) error {
		t.Error("failed pair reached publication")
		return nil
	})
	if !errors.Is(err, sentinel) || progress.Stage != "listing" || progress.CompletedLocalRecords != 0 {
		t.Fatal("original failure or progress changed", err, progress)
	}
}

func TestSeedPairsStopOnPublicationFailureWithoutRetry(t *testing.T) {
	sentinel := errors.New("synthetic publication failure")
	progress := seedProgress{}
	var mu sync.Mutex
	prepared, published := 0, 0
	err := seedPairs(t.Context(), 5, &progress, func(context.Context, int, *seedProgress) (preparedRecord, error) {
		mu.Lock()
		prepared++
		mu.Unlock()
		return preparedRecord{}, nil
	}, func(_ preparedRecord, p *seedProgress) error {
		p.Stage = "publish"
		published++
		if published == 2 {
			return sentinel
		}
		return nil
	})
	if !errors.Is(err, sentinel) || prepared != 2 || published != 2 || progress.CompletedLocalRecords != 1 || progress.Stage != "publish" {
		t.Fatal("publication failure was repeated or counted as complete", err, progress, prepared, published)
	}
}

func TestSeedPairsHonorParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	progress := seedProgress{}
	err := seedPairs(ctx, 5, &progress, func(context.Context, int, *seedProgress) (preparedRecord, error) {
		t.Error("canceled setup started preparation")
		return preparedRecord{}, nil
	}, func(preparedRecord, *seedProgress) error {
		t.Error("canceled setup published a record")
		return nil
	})
	if !errors.Is(err, context.Canceled) || progress.CompletedLocalRecords != 0 {
		t.Fatal("parent cancellation was lost", err, progress)
	}
}
