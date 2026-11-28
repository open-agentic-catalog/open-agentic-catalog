//go:build functional && integration

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

type savedRenderScan struct {
	observation observation
	native      []byte
}

func saveRenderScan(t *testing.T, s *postgresStore, m mapping, releaseID string, at time.Time, findings ...map[string]any) savedRenderScan {
	t.Helper()
	c := captureFixture()
	c.Source, c.CapturedAt = m.Endpoint, at.Add(-time.Millisecond)
	raw := reportFixture(c, testIdentity, findings...)
	code := 0
	if len(findings) > 0 {
		code = 1
	}
	if err := validateScan(raw, c, testIdentity, code); err != nil {
		t.Fatalf("invalid fixture for release %s: %v", releaseID, err)
	}
	event := Event{Source: eventSource + tenant, ID: uuid.NewString(), Action: "listing.release.published", TargetID: releaseID}
	o := observation{
		ResourceID: m.ResourceID, AttemptedReleaseID: releaseID, State: "eligible_at_check", CheckedAt: at, EligibleAt: &at,
		Catalog: &reference{TenantID: tenant, NamespaceID: m.NamespaceID, ResourceID: m.ResourceID, ReleaseID: releaseID, Endpoint: m.Endpoint},
		Capture: &c, Scanner: &testIdentity, Trigger: &event,
		ScanKey: sha(fixtureJSON([]string{m.ResourceID, releaseID, c.Digest, testIdentity.key()})),
	}
	if err := s.Accept(t.Context(), event, at.Add(-2*time.Millisecond)); err != nil {
		t.Fatal("accept publication", err)
	}
	if err := s.Finish(t.Context(), &work{Event: event}, &o, raw, at, ""); err != nil {
		t.Fatal("complete scan", err)
	}
	return savedRenderScan{o, raw}
}

func requireStoredRenderScans(t *testing.T, s *postgresStore, scans ...savedRenderScan) {
	t.Helper()
	for _, scan := range scans {
		o := scan.observation
		var key string
		err := s.pool.QueryRow(t.Context(), `SELECT scan_key FROM mcp_receiver_observations
			WHERE resource_id=$1 AND release_id=$2 AND document=$3::jsonb AND checked_at=$4`,
			o.ResourceID, o.Catalog.ReleaseID, fixtureJSON(o), o.CheckedAt).Scan(&key)
		if err != nil || key != o.ScanKey {
			t.Fatalf("stored observation for %s differs: %v", o.ResourceID, err)
		}
		raw, err := s.Cached(t.Context(), o.ScanKey)
		if err != nil || !bytes.Equal(raw, scan.native) {
			t.Fatalf("stored native scan for %s differs: %v", o.ResourceID, err)
		}
		var completed time.Time
		err = s.pool.QueryRow(t.Context(), `SELECT completed_at FROM mcp_receiver_inbox
			WHERE source=$1 AND id=$2 AND event->>'targetId'=$3 AND outcome=''`,
			o.Trigger.Source, o.Trigger.ID, o.Catalog.ReleaseID).Scan(&completed)
		if err != nil || !completed.Equal(o.CheckedAt) {
			t.Fatalf("completion receipt for %s differs: %v", o.ResourceID, err)
		}
	}
}

func readRenderFile(t *testing.T, dir *os.Root, name string) []byte {
	t.Helper()
	raw, err := dir.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Report consumers look up case-sensitive JSON keys. Unmarshaling into a Go
// struct would also accept spellings such as "ScanFile".
type renderObject map[string]json.RawMessage

func renderField[T any](t *testing.T, obj renderObject, key string) T {
	t.Helper()
	var value T
	raw, ok := obj[key]
	if !ok {
		t.Fatalf("report is missing field %q", key)
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("invalid field %q: %v", key, err)
	}
	return value
}

func requireRenderMetadata(t *testing.T, dir *os.Root, name string, scan savedRenderScan) {
	t.Helper()
	want := scan.observation
	var doc renderObject
	if err := json.Unmarshal(readRenderFile(t, dir, name), &doc); err != nil {
		t.Fatal(err)
	}
	catalog := renderField[renderObject](t, doc, "catalogEvidence")
	if renderField[string](t, doc, "resourceId") != want.ResourceID || renderField[string](t, catalog, "resourceId") != want.ResourceID ||
		renderField[string](t, catalog, "releaseId") != want.Catalog.ReleaseID || renderField[string](t, doc, "attemptedReleaseId") != want.AttemptedReleaseID {
		t.Fatalf("%s does not describe Resource %s at release %s", name, want.ResourceID, want.Catalog.ReleaseID)
	}
	if renderField[string](t, doc, "state") != want.State || !renderField[time.Time](t, doc, "checkedAt").Equal(want.CheckedAt) {
		t.Fatalf("%s has the wrong observation state or check time", name)
	}
	capture := renderField[renderObject](t, doc, "capture")
	if renderField[string](t, capture, "source") != want.Capture.Source ||
		!renderField[time.Time](t, capture, "capturedAt").Equal(want.Capture.CapturedAt) {
		t.Fatalf("%s has the wrong capture source or time for release %s", name, want.Catalog.ReleaseID)
	}
	native := renderField[string](t, doc, "scanFile")
	if native != want.ResourceID+"-scan.json" {
		t.Fatalf("%s points to the wrong native file %q", name, native)
	}
	if !bytes.Equal(readRenderFile(t, dir, native), scan.native) {
		t.Fatalf("%s contains the wrong native report for Resource %s", native, want.ResourceID)
	}
	if renderField[string](t, doc, "scanSha256") != fmt.Sprintf("%x", sha256.Sum256(scan.native)) {
		t.Fatalf("%s has the wrong digest for Resource %s", name, want.ResourceID)
	}
}

func requireRenderIndex(t *testing.T, dir *os.Root, scans ...savedRenderScan) []byte {
	t.Helper()
	raw := readRenderFile(t, dir, "index.json")
	var index renderObject
	if err := json.Unmarshal(raw, &index); err != nil {
		t.Fatal(err)
	}
	items := renderField[[]renderObject](t, index, "items")
	if len(items) != len(scans) {
		t.Fatalf("index contains %d entries, want %d", len(items), len(scans))
	}
	remaining := make(map[string]savedRenderScan, len(scans))
	for _, scan := range scans {
		remaining[scan.observation.ResourceID] = scan
	}
	for _, item := range items {
		resourceID := renderField[string](t, item, "resourceId")
		scan, ok := remaining[resourceID]
		if !ok {
			t.Fatalf("unexpected or duplicate Resource %q in index", resourceID)
		}
		delete(remaining, resourceID)
		file := renderField[string](t, item, "file")
		if file != resourceID+".json" || renderField[string](t, item, "state") != scan.observation.State ||
			!renderField[time.Time](t, item, "checkedAt").Equal(scan.observation.CheckedAt) {
			t.Fatalf("wrong index entry for %s", resourceID)
		}
		requireRenderMetadata(t, dir, file, scan)
	}
	return raw
}

func TestReceiverRenderRecoversReportsForTwoResources(t *testing.T) {
	binary := os.Getenv("MCP_SCANNER_RECEIVER_BINARY")
	if binary == "" {
		t.Fatal("select make test-mcp-scanner-receiver with the built receiver")
	}
	pool, dsn := newReceiverDatabase(t, t.Context())
	s := &postgresStore{pool: pool, capacity: 10, retention: time.Hour}
	path := filepath.Join(t.TempDir(), "reports")
	a := testMapping
	a.TokenFile = filepath.Join(t.TempDir(), "unused-token")
	b := a
	b.ResourceID = "00000000-0000-4000-8000-000000000020"
	b.Endpoint = "https://127.0.0.1:9445/second"
	config := writePrivate(t, "mapping.json", fixtureJSON(configuration{Resources: []mapping{b, a}}))
	db := writePrivate(t, "receiver.dsn", []byte(dsn))
	run := func(action string, wantCode int) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		// #nosec G204 G702 -- Make selects the built receiver. All flags are local test inputs.
		cmd := exec.CommandContext(ctx, binary, action, "--origin", "https://127.0.0.1", "--tenant", tenant,
			"--config-file", config, "--database-file", db, "--reports-dir", path)
		err := cmd.Run()
		var exitErr *exec.ExitError
		if err != nil && !errors.As(err, &exitErr) {
			t.Fatalf("could not run %s: %v", action, err)
		}
		if ctx.Err() != nil || cmd.ProcessState.ExitCode() != wantCode {
			t.Fatalf("%s exited %d, want %d (context: %v)", action, cmd.ProcessState.ExitCode(), wantCode, ctx.Err())
		}
	}
	run("init", 0)
	first := saveRenderScan(t, s, a, release, fixed)
	run("render", 0)
	dir, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dir.Close() }()
	oldIndex := requireRenderIndex(t, dir, first)

	updated := saveRenderScan(t, s, a, uuid.NewString(), fixed.Add(time.Second))
	other := saveRenderScan(t, s, b, uuid.NewString(), fixed.Add(2*time.Second), scannerUnitFinding())
	requireStoredRenderScans(t, s, updated, other)

	// B's native scan can be replaced, but renaming its metadata must fail.
	blocked := b.ResourceID + ".json"
	if err := dir.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(blocked, "sentinel")
	if err := dir.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	run("render", 1)
	requireRenderMetadata(t, dir, a.ResourceID+".json", updated)
	nativeB := b.ResourceID + "-scan.json"
	if !bytes.Equal(readRenderFile(t, dir, nativeB), other.native) {
		t.Fatal("render failed before writing the second native report")
	}
	if !bytes.Equal(readRenderFile(t, dir, "index.json"), oldIndex) || string(readRenderFile(t, dir, sentinel)) != "keep" {
		t.Fatal("failed render changed the previous index or the obstructing directory")
	}
	requireStoredRenderScans(t, s, updated, other)

	if err := dir.Remove(sentinel); err != nil {
		t.Fatal(err)
	}
	if err := dir.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	// A valid scan for the wrong Resource must be regenerated from the database.
	if err := dir.WriteFile(nativeB, updated.native, 0600); err != nil {
		t.Fatal(err)
	}
	run("render", 0)
	requireRenderIndex(t, dir, updated, other)
	requireStoredRenderScans(t, s, updated, other)
}
