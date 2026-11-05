package webhooksender

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/fetcher"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
)

type recordingFetcher struct {
	request fetcher.Request
	result  fetcher.Result
	err     error
}

func (f *recordingFetcher) Fetch(_ context.Context, request fetcher.Request) (fetcher.Result, error) {
	f.request = request
	return f.result, f.err
}

func TestNewRequiresFetcher(t *testing.T) {
	t.Parallel()

	if _, err := New(nil); err == nil {
		t.Fatal("nil fetcher was accepted")
	}
	outbound, err := fetcher.New(fetcher.Policy{Timeout: time.Second, MaxResponseBytes: 1024, MaxRedirects: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sender, err := New(outbound); err != nil || sender == nil {
		t.Fatalf("valid fetcher returned sender %#v, %v", sender, err)
	}
}

func TestSenderMapsCloudEventRequestAndStatus(t *testing.T) {
	t.Parallel()

	outbound := &recordingFetcher{result: fetcher.Result{Status: http.StatusAccepted, RetryAfter: "120"}}
	sender := &Sender{fetcher: outbound}
	request := webhook.SendRequest{
		URL:     "https://hooks.example.com/events",
		Body:    []byte(`{"specversion":"1.0"}`),
		Headers: map[string]string{"Ce-Id": "event-1", "Ce-Type": "com.open.agentic.catalog.resource.published.v1", "Ce-Dataschema": "https://github.com/open-agentic-catalog/open-agentic-catalog/schemas/event/v1"},
	}
	result, err := sender.Send(context.Background(), request)
	if err != nil || result.Status != http.StatusAccepted || result.RetryAfter != "120" {
		t.Fatalf("send result = %#v, %v", result, err)
	}
	if outbound.request.URL != request.URL || outbound.request.Method != http.MethodPost || string(outbound.request.Body) != string(request.Body) {
		t.Fatalf("outbound request = %#v", outbound.request)
	}
	if outbound.request.ContentType != "application/cloudevents+json" || len(outbound.request.Accept) != 1 || outbound.request.Accept[0] != "application/json" {
		t.Fatalf("outbound media contract = %#v", outbound.request)
	}
	if outbound.request.Headers["Ce-Id"] != "event-1" || outbound.request.Headers["Ce-Dataschema"] != "https://github.com/open-agentic-catalog/open-agentic-catalog/schemas/event/v1" || !outbound.request.RejectRedirects {
		t.Fatalf("outbound security contract = %#v", outbound.request)
	}
}

func TestSenderReturnsFetchFailure(t *testing.T) {
	t.Parallel()

	want := errors.New("delivery failed")
	sender := &Sender{fetcher: &recordingFetcher{err: want}}
	result, err := sender.Send(context.Background(), webhook.SendRequest{URL: "https://hooks.example.com/events"})
	if !errors.Is(err, want) || result != (webhook.SendResult{}) {
		t.Fatalf("send failure = %#v, %v", result, err)
	}
}
