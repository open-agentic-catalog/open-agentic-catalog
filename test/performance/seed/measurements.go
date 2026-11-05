package main

import (
	"maps"
	"sync"
	"time"
)

// Seed measurements retain fixed statement classes, never SQL, parameters or errors.
type seedQueryCost struct {
	Calls  uint64 `json:"calls"`
	Errors uint64 `json:"errors"`
	Micros int64  `json:"micros"`
}

type seedQueries struct {
	mu    sync.Mutex
	costs map[string]seedQueryCost
}

func (s *seedQueries) ObserveDatabase(operation string, err error, elapsed time.Duration) {
	switch operation {
	case "select", "insert", "update", "delete", "with", "begin", "commit", "rollback", "copy":
	default:
		operation = "other"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cost := s.costs[operation]
	cost.Calls++
	if err != nil {
		cost.Errors++
	}
	cost.Micros += max(0, elapsed.Microseconds())
	s.costs[operation] = cost
}

func (s *seedQueries) snapshot() map[string]seedQueryCost {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.costs)
}
