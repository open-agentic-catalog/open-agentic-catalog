// Package overview defines bounded tenant-summary values without policy or storage authority.
package overview

import "time"

const SelectionLimit = 10000

type BoundedCount struct {
	Value   int  `json:"value"`
	Limited bool `json:"limited"`
}

func Count(rows int) BoundedCount {
	return BoundedCount{Value: min(rows, SelectionLimit), Limited: rows > SelectionLimit}
}

type Scope struct {
	TenantID       string    `json:"tenantId"`
	Scope          string    `json:"scope"`
	ObservedAt     time.Time `json:"observedAt"`
	SelectionLimit int       `json:"selectionLimit"`
}

func NewScope(tenant string, at time.Time) Scope {
	return Scope{TenantID: tenant, Scope: "current_access", ObservedAt: at.UTC(), SelectionLimit: SelectionLimit}
}
