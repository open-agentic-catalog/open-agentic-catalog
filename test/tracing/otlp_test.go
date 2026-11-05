package tracing

import (
	"encoding/json"
	"reflect"
	"testing"

	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	common "go.opentelemetry.io/proto/otlp/common/v1"
	resource "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// TestDecodeTracePrivacyContract checks the retained projection and rejects
// private fields or malformed identities before the raw capture is discarded.
func TestDecodeTracePrivacyContract(t *testing.T) {
	type captureCase struct {
		name    string
		outcome string
		valid   bool
	}
	cases := []captureCase{
		{name: "valid", valid: true},
		{name: "parent", valid: true},
		{name: "causal link", valid: true},
		{name: "missing resource"}, {name: "extra attribute"},
		{name: "event"}, {name: "vendor state"}, {name: "link"}, {name: "status"},
		{name: "private outcome"}, {name: "non-string outcome"},
		{name: "missing outcome"}, {name: "duplicate outcome"},
		{name: "scope attribute"},
		{name: "short trace ID"}, {name: "zero trace ID"},
		{name: "short span ID"}, {name: "zero span ID"},
		{name: "short parent ID"}, {name: "zero parent ID"},
		{name: "zero link trace ID"}, {name: "zero link span ID"},
		{name: "short link span ID"}, {name: "link vendor state"}, {name: "link attribute"},
		// Keep the other allowed outcomes independent of the production switch.
		{name: "outcome partial", outcome: "partial", valid: true},
		{name: "outcome succeeded", outcome: "succeeded", valid: true},
		{name: "outcome 4xx", outcome: "4xx", valid: true},
		{name: "outcome cancelled", outcome: "cancelled", valid: true},
		{name: "outcome unsupported", outcome: "unsupported", valid: true},
		{name: "outcome waiting", outcome: "waiting", valid: true},
		{name: "outcome error", outcome: "error", valid: true},
		{name: "outcome failed", outcome: "failed", valid: true},
		{name: "outcome dead_letter", outcome: "dead_letter", valid: true},
		{name: "outcome 5xx", outcome: "5xx", valid: true},
		{name: "outcome dependency_error", outcome: "dependency_error", valid: true},
		{name: "outcome other", outcome: "other", valid: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &resource.Resource{}
			for key, value := range map[string]string{"service.name": "catalog-fixture", "service.namespace": "test-installation", "service.version": "v1", "service.instance.id": "process-one"} {
				r.Attributes = append(r.Attributes, &common.KeyValue{Key: key, Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: value}}})
			}
			outcome := tc.outcome
			if outcome == "" {
				outcome = "ok"
			}
			s := &tracepb.Span{Name: "job process", TraceId: make([]byte, 16), SpanId: make([]byte, 8), Attributes: []*common.KeyValue{{Key: "catalog.outcome", Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: outcome}}}}}
			s.TraceId[15] = 1
			s.SpanId[7] = 1
			scope := &tracepb.ScopeSpans{Scope: &common.InstrumentationScope{Name: "open-agentic-catalog"}, Spans: []*tracepb.Span{s}}
			link := &tracepb.Span_Link{TraceId: make([]byte, 16), SpanId: make([]byte, 8)}
			link.TraceId[15] = 3
			link.SpanId[7] = 4
			want := Span{Name: "job process", TraceID: "00000000000000000000000000000001", SpanID: "0000000000000001", Service: "catalog-fixture", Instance: "process-one", Installation: "test-installation"}
			switch tc.name {
			case "parent":
				s.ParentSpanId = []byte{0, 0, 0, 0, 0, 0, 0, 2}
				want.ParentID = "0000000000000002"
			case "causal link":
				s.Links = []*tracepb.Span_Link{link}
				want.Links = []Link{{TraceID: "00000000000000000000000000000003", SpanID: "0000000000000004"}}
			case "missing resource":
				r = nil
			case "extra attribute":
				s.Attributes[0].Key = "db.statement"
			case "event":
				s.Events = []*tracepb.Span_Event{{Name: "exception"}}
			case "vendor state":
				s.TraceState = "private=value"
			case "link":
				link.TraceId = []byte{1}
				s.Links = []*tracepb.Span_Link{link}
			case "status":
				s.Status = &tracepb.Status{Message: "private"}
			case "private outcome":
				s.Attributes[0].Value = &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: "fixture-secret"}}
			case "non-string outcome":
				s.Attributes[0].Value = &common.AnyValue{Value: &common.AnyValue_IntValue{IntValue: 1}}
			case "missing outcome":
				s.Attributes = nil
			case "duplicate outcome":
				s.Attributes = append(s.Attributes, s.Attributes[0])
			case "scope attribute":
				scope.Scope.Attributes = []*common.KeyValue{{Key: "private", Value: &common.AnyValue{Value: &common.AnyValue_StringValue{StringValue: "fixture-secret"}}}}
			case "short trace ID":
				s.TraceId = []byte{1}
			case "zero trace ID":
				clear(s.TraceId)
			case "short span ID":
				s.SpanId = []byte{1}
			case "zero span ID":
				clear(s.SpanId)
			case "short parent ID":
				s.ParentSpanId = []byte{1}
			case "zero parent ID":
				s.ParentSpanId = make([]byte, 8)
			case "zero link trace ID":
				clear(link.TraceId)
				s.Links = []*tracepb.Span_Link{link}
			case "zero link span ID":
				clear(link.SpanId)
				s.Links = []*tracepb.Span_Link{link}
			case "short link span ID":
				link.SpanId = []byte{1}
				s.Links = []*tracepb.Span_Link{link}
			case "link vendor state":
				link.TraceState = "private=fixture-secret"
				s.Links = []*tracepb.Span_Link{link}
			case "link attribute":
				link.Attributes = []*common.KeyValue{{Key: "private"}}
				s.Links = []*tracepb.Span_Link{link}
			}
			batch, err := proto.Marshal(&collector.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{Resource: r, ScopeSpans: []*tracepb.ScopeSpans{scope}}}})
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(map[string]any{"batches": [][]byte{batch}})
			if err != nil {
				t.Fatal(err)
			}
			spans, err := Decode(raw)
			if tc.valid {
				if err != nil || !reflect.DeepEqual(spans, []Span{want}) {
					t.Fatalf("valid projection: spans=%#v err=%v", spans, err)
				}
			} else if err == nil || spans != nil {
				t.Fatalf("unsafe capture: spans=%#v err=%v", spans, err)
			}
		})
	}
	for _, raw := range [][]byte{[]byte("invalid"), []byte(`{"batches":["/w=="]}`), make([]byte, (24<<20)+1)} {
		if _, err := Decode(raw); err == nil {
			t.Fatal("invalid capture accepted")
		}
	}
}
