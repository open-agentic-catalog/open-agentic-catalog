package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"mime"
	"net/http"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/examples/mcp-scanner/receiver/internal/webhookreceiver"
)

type signer struct {
	Secret  string `json:"secret"`
	Version int    `json:"version"`
}

func loadSigner(name string) (signer, error) {
	raw, err := privateFile(name, 4096)
	if err != nil || uniqueJSON(raw) != nil {
		return signer{}, errors.New("signer_unavailable")
	}
	if _, err = fields(raw, "secret", "version"); err != nil {
		return signer{}, errors.New("invalid_signer")
	}
	var s signer
	if json.Unmarshal(raw, &s) != nil || s.Version < 1 || len(s.Secret) != 49 || s.Secret[:6] != "whsec_" {
		return s, errors.New("invalid_signer")
	}
	for _, c := range s.Secret[6:] {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return s, errors.New("invalid_signer")
		}
	}
	return s, nil
}

type application struct {
	store              store
	catalog            catalogReader
	collector          collector
	scanner            scanner
	tenant, signerFile string
	mappings           []mapping
	now                func() time.Time
	guard              func(context.Context) error
	slots              chan struct{}
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
	e, err := parseEvent(raw, a.tenant)
	if err != nil {
		http.Error(w, "invalid event", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if a.store.Accept(ctx, e, a.now().UTC()) != nil {
		http.Error(w, "acceptance unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *application) process(ctx context.Context) error {
	if a.guard(ctx) != nil {
		return failed("processor_lock_lost", false)
	}
	w, err := a.store.Next(ctx, a.now().UTC())
	if errors.Is(err, errEmpty) {
		return nil
	}
	if err != nil {
		return failed("inbox_read_failed", true)
	}
	if w.Event.Action == "webhook.test" {
		return a.store.Finish(ctx, w, nil, nil, a.now().UTC(), "test_accepted")
	}
	o, raw, err := a.assess(ctx, w)
	if ctx.Err() != nil {
		return ctx.Err()
	} // Shutdown leaves the accepted work pending.
	if a.guard(ctx) != nil {
		return failed("processor_lock_lost", false)
	}
	if err != nil {
		var cause *failure
		if errors.As(err, &cause) && cause.transient && w.Attempts < 2 {
			// #nosec G404 -- Jitter only schedules bounded retries, never credentials.
			delay := time.Duration(5<<w.Attempts)*time.Second + time.Duration(rand.Int64N(int64(time.Second)))
			return a.store.Retry(ctx, w, a.now().Add(delay), errorCode(err))
		}
		return a.store.Finish(ctx, w, o, nil, a.now().UTC(), errorCode(err))
	}
	err = a.store.Finish(ctx, w, o, raw, a.now().UTC(), "")
	if errors.Is(err, errFull) && o != nil {
		o.State = "failed"
		o.Failure = "receiver_report_capacity"
		o.ScanKey = ""
		return a.store.Finish(ctx, w, o, nil, a.now().UTC(), o.Failure)
	}
	return err
}

func (a *application) assess(ctx context.Context, w *work) (*observation, json.RawMessage, error) {
	ref, m, err := readRecord(ctx, a.catalog, w.Event.TargetID, a.tenant, a.mappings, a.now())
	if err != nil {
		return nil, nil, err
	}
	o := &observation{ResourceID: ref.ResourceID, AttemptedReleaseID: ref.ReleaseID, Catalog: &ref, State: "failed", CheckedAt: a.now().UTC(), Trigger: &w.Event}
	identity, err := a.scanner.Identity(ctx)
	if err != nil {
		o.Failure = errorCode(err)
		return o, nil, err
	}
	o.Scanner = &identity
	c, err := a.collector.Collect(ctx, *m)
	if err != nil {
		o.Failure = errorCode(err)
		return o, nil, err
	}
	o.Capture = &c
	keyRaw, marshalErr := json.Marshal([]string{ref.ResourceID, ref.ReleaseID, c.Digest, identity.key()})
	if marshalErr != nil {
		return o, nil, failed("scan_binding_failed", false)
	}
	scanKey := sha(keyRaw)
	raw, err := a.store.Cached(ctx, scanKey)
	if err != nil {
		return o, nil, failed("scan_cache_unavailable", true)
	}
	if len(raw) > 0 {
		var saved struct {
			Status  string `json:"status"`
			Capture struct {
				At time.Time `json:"capturedAt"`
			} `json:"capture"`
			Results []struct {
				Findings []json.RawMessage `json:"findings"`
			} `json:"results"`
		}
		if json.Unmarshal(raw, &saved) != nil {
			return o, nil, failed("invalid_cached_scan", false)
		}
		if saved.Status == "completed" {
			original := c
			original.CapturedAt = saved.Capture.At
			exit := 0
			for _, item := range saved.Results {
				if len(item.Findings) > 0 {
					exit = 1
				}
			}
			if validateScan(raw, original, identity, exit) != nil {
				return o, nil, failed("invalid_cached_scan", false)
			}
			o.AnalysisReused = true
		} else {
			raw = nil
		}
	}
	if len(raw) == 0 {
		raw, err = a.scanner.Scan(ctx, c, identity)
		if err != nil {
			o.Failure = errorCode(err)
			return o, nil, err
		}
	}
	o.ScanKey = scanKey
	var outcome struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(raw, &outcome) != nil {
		return o, nil, failed("invalid_scanner_report", false)
	}
	if outcome.Status != "completed" {
		o.ScanKey = sha([]byte(scanKey + sha(raw)))
	}
	current, _, err := readRecord(ctx, a.catalog, ref.ReleaseID, a.tenant, a.mappings, a.now())
	o.CheckedAt = a.now().UTC()
	if err != nil || current != ref {
		o.State = "historical"
		o.Failure = "release_or_access_changed"
		return o, raw, nil //nolint:nilerr // A failed recheck retains completed analysis as historical evidence.
	}
	eligible := o.CheckedAt
	o.EligibleAt = &eligible
	var result struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return o, nil, failed("invalid_scanner_report", false)
	}
	o.State = result.Status
	if result.Status == "completed" {
		o.State = "eligible_at_check"
	}
	return o, raw, nil
}

func (a *application) reconcile(ctx context.Context) error {
	if a.guard(ctx) != nil {
		return failed("processor_lock_lost", false)
	}
	loss, err := readLoss(ctx, a.catalog)
	var found map[string]string
	if err == nil {
		found, err = discover(ctx, a.catalog, a.mappings, a.tenant)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if a.guard(ctx) != nil {
		return failed("processor_lock_lost", false)
	}
	if err != nil {
		if a.store.ReconciliationFailed(ctx, errorCode(err), a.now().UTC()) != nil {
			return failed("reconciliation_save_failed", true)
		}
		return err
	}
	if a.store.Reconciled(ctx, a.mappings, found, loss, a.now().UTC()) != nil {
		return failed("reconciliation_save_failed", true)
	}
	return nil
}
