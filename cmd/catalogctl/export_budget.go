package main

import (
	"io"
	"net/http"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// One serial CLI invocation shares a response-body budget across pages, record
// reads, artifacts and refusals. It measures bytes actually read, not advertised
// Content-Length. One probe byte detects an overrun and is counted explicitly.
type exportByteBudget struct {
	transport http.RoundTripper
	remaining int64
}

func (b *exportByteBudget) RoundTrip(request *http.Request) (*http.Response, error) {
	if b.remaining <= 0 {
		return nil, safeerr.New("export: aggregate response byte budget exhausted")
	}
	response, err := b.transport.RoundTrip(request)
	if err != nil {
		return response, err
	}
	response.Body = &exportBudgetBody{ReadCloser: response.Body, budget: b}
	return response, nil
}

func (b *exportByteBudget) CloseIdleConnections() {
	if transport, ok := b.transport.(interface{ CloseIdleConnections() }); ok {
		transport.CloseIdleConnections()
	}
}

type exportBudgetBody struct {
	io.ReadCloser
	budget *exportByteBudget
}

func (body *exportBudgetBody) Read(p []byte) (int, error) {
	b := body.budget
	if b.remaining < 0 {
		return 0, safeerr.New("export: aggregate response byte budget exceeded")
	}
	if int64(len(p)) > b.remaining+1 {
		p = p[:b.remaining+1]
	}
	n, err := body.ReadCloser.Read(p)
	b.remaining -= int64(n)
	if b.remaining < 0 {
		return n, safeerr.New("export: aggregate response byte budget exceeded")
	}
	return n, err
}
