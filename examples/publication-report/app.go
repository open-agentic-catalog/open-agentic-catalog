package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/examples/publication-report/internal/webhookreceiver"
)

type signer struct {
	Secret  string `json:"secret"`
	Version int    `json:"version"`
}
type application struct {
	store                            store
	catalog                          catalogReader
	tenant, host, signerFile, output string
	now                              func() time.Time
	slots                            chan struct{}
}

func (a *application) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/healthz" && r.Method == http.MethodGet {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.URL.Path != "/webhook" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	select {
	case a.slots <- struct{}{}:
		defer func() { <-a.slots }()
	default:
		http.Error(w, "receiver busy", http.StatusServiceUnavailable)
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/cloudevents+json" {
		http.Error(w, "invalid media type", http.StatusUnsupportedMediaType)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, webhookreceiver.MaxBodyBytes))
	if err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "invalid body", http.StatusBadRequest)
		}
		return
	}
	key, err := loadSigner(a.signerFile)
	if err != nil {
		http.Error(w, "signer unavailable", http.StatusServiceUnavailable)
		return
	}
	if webhookreceiver.Verify(key.Secret, key.Version, r.Header, raw, a.now()) != nil {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	e, err := parseEvent(raw, a.tenant, a.host)
	if err != nil {
		http.Error(w, "invalid event", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err = a.store.Accept(ctx, e, a.now().UTC()); err != nil {
		http.Error(w, "acceptance unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func loadSigner(path string) (signer, error) {
	raw, err := privateFile(path, 4096)
	if err != nil {
		return signer{}, err
	}
	if uniqueJSON(raw) != nil {
		return signer{}, errors.New("invalid_signer")
	}
	var key signer
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&key) != nil || key.Version < 1 || len(key.Secret) != 49 || key.Secret[:6] != "whsec_" {
		return signer{}, errors.New("invalid_signer")
	}
	for _, c := range key.Secret[6:] {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return signer{}, errors.New("invalid_signer")
		}
	}
	return key, nil
}

func (a *application) process(ctx context.Context) error {
	e, err := a.store.Next(ctx)
	if errors.Is(err, errEmpty) {
		return nil
	}
	if err != nil {
		return errors.New("inbox_read_failed")
	}
	if e == nil {
		return nil
	}
	if e.Action == "webhook.test" {
		if a.store.Complete(ctx, e, nil, a.now().UTC(), nil) != nil {
			return errors.New("receipt_commit_failed")
		}
		return nil
	}
	r, err := readReport(ctx, a.catalog, a.tenant, a.host, e, a.now())
	if err != nil {
		if a.store.Fail(ctx, a.now().UTC(), err.Error()) != nil {
			return errors.New("failure_record_failed")
		}
		return err
	}
	r.ObservedAt = a.now().UTC()
	if a.store.Complete(ctx, e, &r, a.now().UTC(), nil) != nil {
		return errors.New("report_commit_failed")
	}
	return nil
}

func (a *application) reconcile(ctx context.Context) error {
	// Counters are evidence, never a condition for reading current publication.
	r, err := readReport(ctx, a.catalog, a.tenant, a.host, nil, a.now())
	if err != nil {
		if a.store.Fail(ctx, a.now().UTC(), err.Error()) != nil {
			return errors.New("failure_record_failed")
		}
		return err
	}
	r.ObservedAt = a.now().UTC()
	capacity, delivery, err := readLoss(ctx, a.catalog)
	if err != nil {
		if a.store.Fail(ctx, a.now().UTC(), err.Error()) != nil {
			return errors.New("failure_record_failed")
		}
		return err
	}
	if a.store.Complete(ctx, nil, &r, a.now().UTC(), &[2]int64{capacity, delivery}) != nil {
		return errors.New("report_commit_failed")
	}
	return nil
}

func (a *application) render(ctx context.Context) error {
	s, err := a.store.Read(ctx)
	if err != nil {
		return errors.New("report_read_failed")
	}
	return render(a.output, s)
}
