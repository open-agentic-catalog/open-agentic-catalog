package observability

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestActivityCountsExcludeObserversAndKeepListenerProvenance(t *testing.T) {
	r := New(nil)
	api := r.BeginHTTPFamily(FamilyPlatform)
	worker := r.BeginHTTP()
	product := r.BeginHTTPFamily(FamilyControlSearch)
	observer := r.BeginHTTPFamily(FamilyObserver)
	before, err := r.Activity(true)
	if err != nil || before.HTTP.ActiveCatalog != 1 || before.HTTP.Completed != 0 {
		t.Fatalf("wrong admitted work: %+v %v", before.HTTP, err)
	}
	api("GET", "GET /livez", 200, 0, time.Second)
	worker("GET", "GET /livez", 200, 0, 2*time.Second)
	product("POST", "POST /api/v1/catalog/search", 400, 0, 3*time.Second)
	product("POST", "POST /api/v1/catalog/search", 400, 0, 3*time.Second)
	// The observer samples itself before its completion.
	sample, err := r.Activity(true)
	if err != nil || sample.HTTP.Completed != 3 || sample.HTTP.ActiveCatalog != 0 {
		t.Fatalf("wrong completion: %+v %v", sample.HTTP, err)
	}
	if sample.HTTP.Families[FamilyPlatform].Completed != 1 || sample.HTTP.Families[FamilyAuxiliary].Completed != 1 || sample.HTTP.Families[FamilyControlSearch].Completed != 1 || sample.HTTP.Families[FamilyObserver].Completed != 0 {
		t.Fatal("listeners/observer conflated")
	}
	if sample.HTTP.Families[FamilyControlSearch].DurationSeconds != 3 || sample.HTTP.Families[FamilyControlSearch].StatusClasses[3].Completed != 1 {
		t.Fatal("wrong failure population")
	}
	observer("GET", "GET /api/v1/service-activity", 200, 0, time.Second)
	next, err := r.Activity(true)
	if err != nil || next.HTTP.Completed != 4 || next.HTTP.Families[FamilyObserver].Completed != 1 {
		t.Fatal("observer completion lost")
	}
	// Returned arrays are detached from registry authority.
	sample.HTTP.Families[0].Methods[0].Completed = 123
	if r.activity[0].methods[0] != 0 {
		t.Fatal("sample aliases registry")
	}
	if strings.Count(string(r.Render(context.Background())), "open_agentic_catalog_http_responses_total{method=\"GET\",route=\"/livez\",status_class=\"2xx\"} 2") != 1 {
		t.Fatal("existing raw series changed")
	}
}

func TestActivityNeverCollectsProvidersAndKeepsOutcomePopulationsSeparate(t *testing.T) {
	r := New(nil)
	r.SetPoolSnapshot(func() PoolSnapshot { panic("pool callback invoked") })
	r.SetSnapshotProvider(snapshotProviderFunc(func(context.Context, time.Time) (OperationalSnapshot, error) { panic("provider callback invoked") }))
	r.runtimeRead = func() runtimeReading { panic("runtime callback invoked") }
	r.ObserveOperation(OperationCatalogSearch, OutcomePartial)
	r.ObserveOperation(OperationObservationSearch, OutcomeSucceeded)
	r.ObserveMCP("call", "tool_error")
	s, err := r.Activity(true)
	if err != nil || !s.MCP.Enabled || s.HTTP.Completed != 0 || s.ControlSearch.Outcomes[1].Completed != 1 || s.MCP.Outcomes[21].Completed != 1 {
		t.Fatalf("wrong independent outcomes: %+v %v", s, err)
	}
	disabled, err := New(nil).Activity(false)
	if err != nil || disabled.MCP.Enabled {
		t.Fatal("disabled MCP misreported")
	}
}

func TestActivityExactCountersAndInvalidSamples(t *testing.T) {
	for _, value := range []uint64{1<<53 + 1, math.MaxUint64} {
		r := New(nil)
		r.requests = value
		r.activity[0] = activityMetric{completed: value, seconds: 1, methods: [7]uint64{value}, classes: [5]uint64{0, value}}
		s, err := r.Activity(false)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			HTTP struct {
				Completed string `json:"completed"`
			} `json:"http"`
		}
		if err = json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		want := "9007199254740993"
		if value == math.MaxUint64 {
			want = "18446744073709551615"
		}
		if got.HTTP.Completed != want {
			t.Fatalf("counter lost: %s", got.HTTP.Completed)
		}
	}
	for _, fault := range []string{"nan", "infinity", "negative", "wrong total", "wrong methods", "wrong classes", "method sum overflow", "overflow", "active overflow", "search overflow", "mcp overflow"} {
		t.Run(fault, func(t *testing.T) {
			r := New(nil)
			switch fault {
			case "nan":
				r.activity[0].seconds = math.NaN()
			case "infinity":
				r.activity[0].seconds = math.Inf(1)
			case "negative":
				r.activity[0].seconds = -1
			case "wrong total":
				r.requests = 1
			case "wrong methods":
				r.activity[0].methods[0] = 1
			case "wrong classes":
				r.activity[0].classes[0] = 1
			case "method sum overflow":
				r.activity[0].methods = [7]uint64{math.MaxUint64, 1}
			case "overflow":
				r.requests = math.MaxUint64
				f := r.BeginHTTP()
				f("GET", "GET /status", 200, 0, 0)
			case "active overflow":
				r.activeCatalog = math.MaxUint64
				f := r.BeginHTTPFamily(FamilyControlRead)
				f("GET", "GET /api/v1/resources", 200, 0, 0)
			case "search overflow":
				r.operations[operationKey{OperationCatalogSearch, OutcomeFailed}] = math.MaxUint64
				r.ObserveOperation(OperationCatalogSearch, OutcomeFailed)
			case "mcp overflow":
				r.mcp = map[mcpKey]uint64{{"call", "tool_error"}: math.MaxUint64}
				r.ObserveMCP("call", "tool_error")
			}
			if _, err := r.Activity(true); err == nil {
				t.Fatal("invalid sample succeeded")
			}
		})
	}
	var missing *Registry
	if _, err := missing.Activity(false); err == nil {
		t.Fatal("missing registry returned zero")
	}
}

func TestActivityConcurrentCompletionAndSnapshots(t *testing.T) {
	r := New(nil)
	var work sync.WaitGroup
	for i := 0; i < 40; i++ {
		work.Go(func() {
			f := r.BeginHTTPFamily(FamilyControlRead)
			if _, err := r.Activity(true); err != nil {
				t.Error(err)
			}
			f("HEAD", "GET /api/v1/resources", 403, 0, time.Second)
			f("HEAD", "GET /api/v1/resources", 403, 0, time.Second)
		})
	}
	work.Wait()
	s, err := r.Activity(true)
	if err != nil || s.HTTP.Completed != 40 || s.HTTP.ActiveCatalog != 0 || s.HTTP.Families[FamilyControlRead].Methods[1].Completed != 40 {
		t.Fatalf("unbalanced counts: %+v %v", s.HTTP, err)
	}
}

func TestActivityUnknownVocabularyStaysAuxiliaryAndBounded(t *testing.T) {
	r := New(nil)
	finish := r.BeginHTTPFamily(HTTPFamily(255))
	finish("arbitrary-method", "/private-path?credential=sentinel", 700, 0, -time.Second)
	sample, err := r.Activity(false)
	if err != nil {
		t.Fatal(err)
	}
	row := sample.HTTP.Families[FamilyAuxiliary]
	if sample.HTTP.ActiveCatalog != 0 || row.Completed != 1 || row.Methods[6].Completed != 1 || row.StatusClasses[1].Completed != 1 || row.DurationSeconds != 0 {
		t.Fatalf("unbounded/invalid normalization: %+v", row)
	}
	raw, err := json.Marshal(sample)
	if err != nil || strings.Contains(string(raw), "sentinel") || strings.Contains(string(raw), "arbitrary-method") {
		t.Fatal("unreviewed label escaped", err)
	}
}

func TestActivityConcurrentDuplicateCompletionCountsOnce(t *testing.T) {
	r := New(nil)
	finish := r.BeginHTTPFamily(FamilyControlCommand)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := 0; i < 32; i++ {
		workers.Go(func() { <-start; finish("POST", "POST /api/v1/namespaces", 201, 0, 2*time.Second) })
	}
	close(start)
	workers.Wait()
	sample, err := r.Activity(false)
	if err != nil || sample.HTTP.Completed != 1 || sample.HTTP.ActiveCatalog != 0 || sample.HTTP.Families[FamilyControlCommand].DurationSeconds != 2 {
		t.Fatalf("duplicate completion changed population: %+v %v", sample.HTTP, err)
	}
}
