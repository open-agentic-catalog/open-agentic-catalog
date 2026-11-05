// Package tracing decodes bounded captures from the owned functional fixture.
// It is test tooling only and is not linked into the shipped binaries.
package tracing

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	collector "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

// Link identifies one retained causal relationship without copying span attributes.
type Link struct {
	TraceID string `json:"traceId"`
	SpanID  string `json:"spanId"`
}

// Span contains only the identity and causality fields needed by fixture assertions.
// It deliberately omits arbitrary attributes, event payloads and status messages.
type Span struct {
	Name         string `json:"name"`
	TraceID      string `json:"traceId"`
	SpanID       string `json:"spanId"`
	ParentID     string `json:"parentId"`
	Service      string `json:"service"`
	Instance     string `json:"instance"`
	Installation string `json:"installation"`
	Links        []Link `json:"links"`
}

// Decode checks the real wire format and its privacy contract before returning
// only fields needed for causal assertions in Compose and Minikube.
func Decode(raw []byte) ([]Span, error) {
	if len(raw) > 24<<20 {
		return nil, errors.New("trace capture too large")
	}
	var capture struct {
		Batches [][]byte `json:"batches"`
	}
	if err := json.Unmarshal(raw, &capture); err != nil || len(capture.Batches) > 2048 {
		return nil, errors.New("invalid trace capture")
	}
	var spans []Span
	for _, batch := range capture.Batches {
		var request collector.ExportTraceServiceRequest
		if len(batch) > 1<<20 || proto.Unmarshal(batch, &request) != nil {
			return nil, errors.New("invalid OTLP protobuf")
		}
		for _, rs := range request.ResourceSpans {
			if rs.Resource == nil {
				return nil, errors.New("missing trace resource")
			}
			resource := map[string]string{}
			for _, attribute := range rs.Resource.Attributes {
				switch attribute.Key {
				case "service.name", "service.namespace", "service.version", "service.instance.id":
				default:
					return nil, errors.New("unexpected trace resource attribute")
				}
				resource[attribute.Key] = attribute.Value.GetStringValue()
			}
			if len(resource) != 4 || resource["service.instance.id"] == "" {
				return nil, errors.New("missing process identity")
			}
			for _, scope := range rs.ScopeSpans {
				if len(scope.Scope.GetAttributes()) != 0 {
					return nil, errors.New("unexpected trace scope attribute")
				}
				for _, span := range scope.Spans {
					if len(spans) >= 100000 || !validID(span.TraceId, 16) || !validID(span.SpanId, 8) || len(span.ParentSpanId) != 0 && !validID(span.ParentSpanId, 8) || len(span.Name) > 160 || len(span.Events) != 0 || span.TraceState != "" || len(span.Links) > 1 || span.Status.GetMessage() != "" {
						return nil, errors.New("unsafe or invalid exported span")
					}
					if len(span.Attributes) != 1 {
						return nil, errors.New("expected one span outcome")
					}
					for _, a := range span.Attributes {
						if a.Key != "catalog.outcome" {
							return nil, errors.New("unexpected span attribute")
						}
						// Non-string values yield an empty string and are rejected too.
						switch a.Value.GetStringValue() {
						case "ok", "partial", "succeeded", "4xx", "cancelled", "unsupported", "waiting", "error", "failed", "dead_letter", "5xx", "dependency_error", "other":
						default:
							return nil, errors.New("unexpected span outcome")
						}
					}
					if strings.Contains(span.Name, "?") || strings.Contains(span.Name, "fixture-secret") {
						return nil, errors.New("private request data in span")
					}
					value := Span{Name: span.Name, TraceID: hex.EncodeToString(span.TraceId), SpanID: hex.EncodeToString(span.SpanId), ParentID: hex.EncodeToString(span.ParentSpanId), Service: resource["service.name"], Instance: resource["service.instance.id"], Installation: resource["service.namespace"]}
					for _, link := range span.Links {
						if !validID(link.TraceId, 16) || !validID(link.SpanId, 8) || link.TraceState != "" || len(link.Attributes) != 0 {
							return nil, errors.New("unsafe trace link")
						}
						value.Links = append(value.Links, Link{hex.EncodeToString(link.TraceId), hex.EncodeToString(link.SpanId)})
					}
					spans = append(spans, value)
				}
			}
		}
	}
	return spans, nil
}

// validID accepts the exact OTLP identity width with at least one nonzero byte.
func validID(id []byte, size int) bool {
	if len(id) != size {
		return false
	}
	for _, b := range id {
		if b != 0 {
			return true
		}
	}
	return false
}
