package main

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSeedQueriesKeepBoundedCostsWithoutPrivateValues(t *testing.T) {
	queries := &seedQueries{costs: make(map[string]seedQueryCost)}
	queries.ObserveDatabase("select", nil, 12*time.Microsecond)
	queries.ObserveDatabase("select", errors.New("private DSN"), -time.Second)
	queries.ObserveDatabase("private SQL", errors.New("private credentials"), 7*time.Microsecond)
	got := queries.snapshot()
	if len(got) != 2 || got["select"] != (seedQueryCost{Calls: 2, Errors: 1, Micros: 12}) || got["other"] != (seedQueryCost{Calls: 1, Errors: 1, Micros: 7}) {
		t.Fatalf("unexpected measurements: %+v", got)
	}
	raw, err := json.Marshal(got)
	if err != nil || strings.Contains(string(raw), "private") {
		t.Fatal("measurements retained private values", err)
	}
	delete(got, "select")
	if queries.snapshot()["select"].Calls != 2 {
		t.Fatal("snapshot changed retained measurements")
	}
}

func TestSeedQueryMeasurementsAreConcurrent(t *testing.T) {
	queries := &seedQueries{costs: make(map[string]seedQueryCost)}
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			for range 100 {
				queries.ObserveDatabase("insert", nil, time.Microsecond)
				_ = queries.snapshot()
			}
		})
	}
	workers.Wait()
	if got := queries.snapshot()["insert"]; got != (seedQueryCost{Calls: 400, Micros: 400}) {
		t.Fatalf("lost measurements: %+v", got)
	}
}
