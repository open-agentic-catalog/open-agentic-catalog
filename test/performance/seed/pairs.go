package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

type preparedRecord struct {
	listing      catalog.CatalogListing
	submissionID string
}

// Prepare at most two independent drafts within the existing pool limit.
// Join their preparation before serial publication: every discovery-affecting
// write advances the shared tenant generation used by Serializable publication.
func seedPairs(ctx context.Context, count int, progress *seedProgress, prepare func(context.Context, int, *seedProgress) (preparedRecord, error), publish func(preparedRecord, *seedProgress) error) error {
	type result struct {
		index  int
		record preparedRecord
		stage  string
		err    error
	}
	for first := 0; first < count; first += 2 {
		if err := ctx.Err(); err != nil {
			return err
		}
		progress.Stage = "prepare-records"
		call, cancel := context.WithCancel(ctx)
		n := min(2, count-first)
		results := make(chan result, 2)
		var workers sync.WaitGroup
		for offset := range n {
			index := first + offset
			workers.Go(func() {
				local := seedProgress{}
				record, err := prepare(call, index, &local)
				results <- result{index: index, record: record, stage: local.Stage, err: err}
			})
		}
		records := make([]preparedRecord, n)
		var failure error
		for range n {
			value := <-results
			records[value.index-first] = value.record
			if value.err != nil && failure == nil {
				failure = fmt.Errorf("local record %d: %w", value.index, value.err)
				progress.Stage = value.stage
				cancel()
			}
		}
		workers.Wait()
		cancel()
		if failure != nil {
			return failure
		}
		for offset, record := range records {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := publish(record, progress); err != nil {
				return fmt.Errorf("local record %d: %w", first+offset, err)
			}
			progress.CompletedLocalRecords++
			if progress.CompletedLocalRecords%100 == 1 {
				fmt.Printf("seeded local records: %d\n", progress.CompletedLocalRecords)
			}
		}
	}
	return ctx.Err()
}
