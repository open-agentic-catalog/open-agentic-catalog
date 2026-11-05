package observability

import (
	"errors"
	"math"
)

// HTTPFamily is a closed request population, never a request-supplied label.
type HTTPFamily uint8

const (
	FamilyControlSearch HTTPFamily = iota
	FamilyControlExplore
	FamilyObservationSearch
	FamilyAdministrativeLiveSearch
	FamilyARDSearch
	FamilyARDExplore
	FamilyARDList
	FamilyDiscoveryDocuments
	FamilyHTTPMCP
	FamilyControlRead
	FamilyControlCommand
	FamilyPlatform
	FamilyObserver
	FamilyAuxiliary
	activityFamilyCount
)

var activityFamilies = [...]string{"control_search", "control_explore", "observation_search", "administrative_live_search", "ard_search", "ard_explore", "ard_list", "discovery_documents", "http_mcp", "control_read", "control_command", "platform", "observer", "unknown_auxiliary"}
var activityMethods = [...]string{"GET", "HEAD", "POST", "PATCH", "DELETE", "OPTIONS", "OTHER"}
var activityClasses = [...]string{"1xx", "2xx", "3xx", "4xx", "5xx"}
var activitySearchOutcomes = [...]Outcome{OutcomeSucceeded, OutcomePartial, OutcomeRejected, OutcomeFailed}
var activityMCPOperations = [...]string{"protocol", "discover", "list", "call"}
var activityMCPOutcomes = [...]string{"completed", "rejected", "failed", "tool_error", "busy", "cancelled"}

type activityMetric struct {
	completed uint64
	seconds   float64
	methods   [7]uint64
	classes   [5]uint64
}

// ActivityMethod and ActivityStatus keep exact uint64 values on the JSON wire.
type ActivityMethod struct {
	Method    string `json:"method"`
	Completed uint64 `json:"completed,string"`
}
type ActivityStatus struct {
	StatusClass string `json:"statusClass"`
	Completed   uint64 `json:"completed,string"`
}
type ActivityFamily struct {
	Family          string            `json:"family"`
	Completed       uint64            `json:"completed,string"`
	DurationSeconds float64           `json:"durationSeconds"`
	Methods         [7]ActivityMethod `json:"methods"`
	StatusClasses   [5]ActivityStatus `json:"statusClasses"`
}
type ActivityHTTP struct {
	Completed     uint64             `json:"completed,string"`
	ActiveCatalog uint64             `json:"activeCatalog,string"`
	Families      [14]ActivityFamily `json:"families"`
}
type ActivityOutcome struct {
	Outcome   string `json:"outcome"`
	Completed uint64 `json:"completed,string"`
}
type ActivitySearch struct {
	Outcomes [4]ActivityOutcome `json:"outcomes"`
}
type ActivityMCPOutcome struct {
	Operation string `json:"operation"`
	Outcome   string `json:"outcome"`
	Completed uint64 `json:"completed,string"`
}
type ActivityMCP struct {
	Enabled  bool                   `json:"enabled"`
	Outcomes [24]ActivityMCPOutcome `json:"outcomes"`
}

// ActivitySample copies only process counters; it has no provider or I/O path.
type ActivitySample struct {
	HTTP          ActivityHTTP   `json:"http"`
	ControlSearch ActivitySearch `json:"controlSearch"`
	MCP           ActivityMCP    `json:"mcp"`
}

func incrementActivity(value *uint64) bool {
	if *value == math.MaxUint64 {
		return false
	}
	*value++
	return true
}

func activitySum(values []uint64) (uint64, bool) {
	var sum uint64
	for _, value := range values {
		if value > math.MaxUint64-sum {
			return 0, false
		}
		sum += value
	}
	return sum, true
}

// Activity returns one coherent bounded copy, without Snapshot/Render callbacks.
func (r *Registry) Activity(mcpEnabled bool) (ActivitySample, error) {
	if r == nil {
		return ActivitySample{}, errors.New("activity sample unavailable")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.activityInvalid {
		return ActivitySample{}, errors.New("activity sample unavailable")
	}
	sample := ActivitySample{HTTP: ActivityHTTP{Completed: r.requests, ActiveCatalog: r.activeCatalog}, MCP: ActivityMCP{Enabled: mcpEnabled}}
	var counts [14]uint64
	for i, name := range activityFamilies {
		value := r.activity[i]
		methods, methodsOK := activitySum(value.methods[:])
		classes, classesOK := activitySum(value.classes[:])
		if !methodsOK || !classesOK || methods != value.completed || classes != value.completed || math.IsNaN(value.seconds) || math.IsInf(value.seconds, 0) || value.seconds < 0 {
			return ActivitySample{}, errors.New("activity sample unavailable")
		}
		counts[i] = value.completed
		row := ActivityFamily{Family: name, Completed: value.completed, DurationSeconds: value.seconds}
		for j, method := range activityMethods {
			row.Methods[j] = ActivityMethod{Method: method, Completed: value.methods[j]}
		}
		for j, class := range activityClasses {
			row.StatusClasses[j] = ActivityStatus{StatusClass: class, Completed: value.classes[j]}
		}
		sample.HTTP.Families[i] = row
	}
	total, ok := activitySum(counts[:])
	if !ok || total != r.requests {
		return ActivitySample{}, errors.New("activity sample unavailable")
	}
	for i, outcome := range activitySearchOutcomes {
		sample.ControlSearch.Outcomes[i] = ActivityOutcome{Outcome: string(outcome), Completed: r.operations[operationKey{operation: OperationCatalogSearch, outcome: outcome}]}
	}
	for i, operation := range activityMCPOperations {
		for j, outcome := range activityMCPOutcomes {
			sample.MCP.Outcomes[i*len(activityMCPOutcomes)+j] = ActivityMCPOutcome{Operation: operation, Outcome: outcome, Completed: r.mcp[mcpKey{operation: operation, outcome: outcome}]}
		}
	}
	return sample, nil
}
