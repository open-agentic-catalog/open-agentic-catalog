// Package processinfo captures one execution's identity and elapsed time.
// It has no database, configuration, network or background work.
package processinfo

import (
	"errors"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

// Snapshot describes only the process that answered, not deployment membership.
type Snapshot struct {
	InstanceID    string    `json:"instanceId"`
	StartedAt     time.Time `json:"startedAt"`
	ObservedAt    time.Time `json:"observedAt"`
	UptimeSeconds int64     `json:"uptimeSeconds"`
}

// Process retains the original monotonic start instant. Serialized timestamps
// use UTC, but UTC conversion must never replace the elapsed-time source.
type Process struct {
	instance string
	started  time.Time
	now      func() time.Time
	since    func(time.Time) time.Duration
}

// New initializes one execution. Call once and share it across process surfaces.
func New() (*Process, error) {
	return newProcess(time.Now, time.Since, id.Generator{})
}

func newProcess(now func() time.Time, since func(time.Time) time.Duration, generator id.Generator) (*Process, error) {
	started := now()
	instance, err := generator.New(started)
	if err != nil {
		return nil, errors.New("process identity unavailable")
	}
	return &Process{instance: instance, started: started, now: now, since: since}, nil
}

// ID returns the same opaque execution identifier even when tracing is disabled.
func (p *Process) ID() string { return p.instance }

// Read uses monotonic elapsed time independently of wall-clock corrections.
// ObservedAt can precede StartedAt after a wall-clock correction.
func (p *Process) Read() Snapshot {
	snapshot, _ := p.ReadWithElapsed()
	return snapshot
}

// ReadWithElapsed adds precise monotonic elapsed time without changing Read's wire contract.
func (p *Process) ReadWithElapsed() (Snapshot, time.Duration) {
	elapsed := max(time.Duration(0), p.since(p.started))
	return Snapshot{InstanceID: p.instance, StartedAt: p.started.UTC(), ObservedAt: p.now().UTC(), UptimeSeconds: int64(elapsed / time.Second)}, elapsed
}
