// Retained-evidence tests preserve document bindings and bounded revalidation work.
package catalog

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Adapter tests own archive admission. These bytes and their retained manifest
// let the service tests exercise identity renewal without parsing an archive.
func retainedPluginRevision(t *testing.T) (VersionRevision, []byte) {
	t.Helper()
	const manifest = `{"name":"tools","version":"1.0.0"}`
	var buffer bytes.Buffer
	w := zip.NewWriter(&buffer)
	file, err := w.Create("plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte(manifest)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	body := buffer.Bytes()
	r := Representation{ID: "plugin-representation", Role: RepresentationPrimary, MediaType: PluginMediaZIP, ProfileID: PluginProfile, URL: "https://example.com/plugin.zip", ContentDigest: digestBytes(body), Size: int64(len(body)), ValidationState: "valid", Plugin: pluginInspectionDocuments(map[string]string{"plugin.json": manifest})}
	if err := ValidatePackageRepresentation(r); err != nil {
		t.Fatal(err)
	}
	revision := VersionRevision{Representations: []Representation{r}}
	revision.ContentSetDigest, err = contentSetDigest(revision.Representations)
	if err != nil {
		t.Fatal(err)
	}
	return revision, body
}

func TestRetainedEvidencePluginRefresh(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"unchanged", "octet stream", "changed digest", "changed length", "wrong media", "HTTP failure", "fetch failure", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			f := newGuardrailFixture(t)
			revision, body := retainedPluginRevision(t)
			before, err := json.Marshal(revision)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				after, err := json.Marshal(revision)
				if err != nil || !bytes.Equal(before, after) {
					t.Error("refresh rewrote immutable Plugin evidence")
				}
			})
			f.fetcher.body = bytes.Clone(body)
			ctx, cancel := context.WithCancel(f.adminContext())
			defer cancel()
			wantFinding, wantPublication := "", ErrorCode("")
			sentinel := errors.New("archive unavailable")
			switch mode {
			case "octet stream":
				f.fetcher.mediaType = "application/octet-stream"
			case "changed digest":
				f.fetcher.body[0] ^= 1
				wantFinding = "EXTERNAL_CONTENT_CHANGED"
			case "changed length":
				f.fetcher.body = append(f.fetcher.body, 0)
				wantFinding = "EXTERNAL_CONTENT_CHANGED"
			case "wrong media":
				f.fetcher.mediaType = "text/plain"
				wantFinding = "EXTERNAL_MEDIA_TYPE_MISMATCH"
			case "HTTP failure":
				f.fetcher.status = 404
				wantFinding = "EXTERNAL_HTTP_STATUS"
			case "fetch failure":
				f.fetcher.err = sentinel
				wantFinding = "EXTERNAL_RETRIEVAL_FAILED"
				wantPublication = CodeDependencyUnavailable
			case "cancelled":
				cancel()
			}
			if wantFinding != "" && wantPublication == "" {
				wantPublication = CodePublicationNotReady
			}
			validations := 0
			f.service.validator = guardrailValidator(func(_ context.Context, kind Kind, input RepresentationInput) ValidationResult {
				validations++
				if kind != KindPlugin || input.URL != revision.Representations[0].URL || !bytes.Equal(input.PackageData, f.fetcher.body) || len(input.Data) != 0 {
					t.Fatal("validator lost Plugin byte or identity binding")
				}
				return ValidationResult{ProfileID: PluginProfile}
			})
			findings, retrievals, err := f.service.revalidateStoredRevision(ctx, guardrailTenantID, KindPlugin, "1.0.0", revision)
			if mode == "cancelled" {
				if !errors.Is(err, context.Canceled) || len(f.fetcher.requests) != 0 || validations != 0 || len(retrievals) != 0 {
					t.Fatalf("cancelled refresh: %v, fetches=%d validations=%d", err, len(f.fetcher.requests), validations)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(f.fetcher.requests) != 1 {
				t.Fatalf("refresh made %d fetches; want one", len(f.fetcher.requests))
			}
			if wantFinding == "" && len(findings) != 0 || wantFinding != "" && (len(findings) != 1 || findings[0].Code != wantFinding || findings[0].Severity != SeverityError) {
				t.Fatalf("findings=%+v; want %s", findings, wantFinding)
			}
			wantRetrievals := 1
			if mode == "fetch failure" {
				wantRetrievals = 0
			}
			if len(retrievals) != wantRetrievals || validations != wantRetrievals {
				t.Fatalf("retrievals=%d validations=%d", len(retrievals), validations)
			}
			if len(retrievals) == 1 && (retrievals[0].RepresentationID != revision.Representations[0].ID || retrievals[0].ContentDigest != digestBytes(f.fetcher.body) || retrievals[0].Size != int64(len(f.fetcher.body))) {
				t.Fatalf("retrieval lost observed identity: %+v", retrievals)
			}
			published, err := f.service.validateExternalPublication(ctx, guardrailTenantID, revision)
			if wantPublication == "" {
				if err != nil || !reflect.DeepEqual(published, retrievals) {
					t.Fatalf("publication=%+v error=%v", published, err)
				}
			} else if ErrorCodeOf(err) != wantPublication || len(published) != 0 {
				t.Fatalf("publication error=%v retrievals=%+v", err, published)
			}
			if mode == "fetch failure" && !errors.Is(err, sentinel) {
				t.Fatal("publication lost fetch cause")
			}
			if len(f.fetcher.requests) != 2 || validations != wantRetrievals {
				t.Fatalf("publication fetches=%d validations=%d", len(f.fetcher.requests)-1, validations)
			}
			for _, request := range f.fetcher.requests {
				if request.TenantID != guardrailTenantID || request.URL != revision.Representations[0].URL || request.MediaType != PluginMediaZIP || request.MaxResponseBytes != MaxCatalogDocumentBytes {
					t.Fatalf("unscoped or unbounded fetch: %+v", request)
				}
			}
		})
	}
}

type retainedDocumentReader struct {
	Repository
	document DocumentBlob
	err      error
	requests [][2]string
}

func (r *retainedDocumentReader) GetDocument(_ context.Context, tenant, id string) (DocumentBlob, error) {
	r.requests = append(r.requests, [2]string{tenant, id})
	return r.document, r.err
}

func TestRetainedEvidencePublicationDocumentBinding(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"valid", "missing", "read failure", "ID", "digest", "media", "size", "content"} {
		t.Run(mode, func(t *testing.T) {
			f := newGuardrailFixture(t)
			body := []byte(`{"name":"original"}`)
			digest := digestBytes(body)
			document := DocumentBlob{ID: strings.TrimPrefix(digest, "sha256:"), TenantID: guardrailTenantID, Digest: digest, MediaType: "application/json", Size: int64(len(body)), Content: body, CreatedAt: f.clock.now}
			if err := ValidateDocumentBlob(guardrailTenantID, document); err != nil {
				t.Fatal(err)
			}
			representation := Representation{ID: "representation", Role: RepresentationPrimary, ProfileID: "test-profile", ValidationState: "valid", URL: "https://example.com/agent.json", ArtifactID: document.ID, ContentDigest: digest, MediaType: document.MediaType, Size: document.Size}
			r := &retainedDocumentReader{document: document}
			f.service.repo, f.fetcher.body = r, body
			sentinel := errors.New("document read failed")
			switch mode {
			case "missing":
				r.err = E(CodeNotFound, "document missing")
			case "read failure":
				r.err = sentinel
			case "ID":
				r.document.ID = strings.Repeat("a", 64)
			case "digest":
				r.document.Digest = digestBytes([]byte(`{}`))
			case "media":
				r.document.MediaType = "text/plain"
			case "size":
				r.document.Size++
			case "content":
				r.document.Content = []byte(`{"name":"modified"}`)
			}
			second := representation
			second.ID, second.URL = "second-representation", "https://example.com/second.json"
			second.Role = RepresentationDistribution
			got, err := f.service.validateExternalPublication(f.adminContext(), guardrailTenantID, VersionRevision{Representations: []Representation{representation, second}})
			wantReads, wantFetches := 1, 0
			switch mode {
			case "valid":
				wantReads, wantFetches = 2, 2
				if err != nil || len(got) != 2 {
					t.Fatalf("valid publication=%+v error=%v", got, err)
				}
			case "read failure":
				if !errors.Is(err, sentinel) || len(got) != 0 {
					t.Fatalf("read failure=%v retrievals=%+v", err, got)
				}
			default:
				if ErrorCodeOf(err) != CodePublicationNotReady || len(got) != 0 {
					t.Fatalf("publication error=%v retrievals=%+v", err, got)
				}
			}
			if len(r.requests) != wantReads || len(f.fetcher.requests) != wantFetches {
				t.Fatalf("reads=%v fetches=%v", r.requests, f.fetcher.requests)
			}
			for _, request := range r.requests {
				if request != [2]string{guardrailTenantID, document.ID} {
					t.Fatalf("lost document binding: %v", request)
				}
			}
			for i, request := range f.fetcher.requests {
				want := []Representation{representation, second}[i]
				if request.TenantID != guardrailTenantID || request.URL != want.URL || request.MediaType != want.MediaType || request.MaxResponseBytes != MaxCatalogDocumentBytes-int64(i*len(body)) {
					t.Fatalf("fetch %d lost document scope or budget: %+v", i, request)
				}
				if got[i].RepresentationID != want.ID || got[i].ContentDigest != digest || got[i].Size != int64(len(body)) {
					t.Fatalf("retrieval %d lost document identity: %+v", i, got[i])
				}
			}
		})
	}
}

type retainedSequenceFetcher struct {
	*guardrailExternalFetcher
	bodies [][]byte
}

func (f *retainedSequenceFetcher) FetchExternalRepresentation(ctx context.Context, request ExternalRepresentationFetchRequest) (ExternalRepresentationFetchResult, error) {
	f.body = f.bodies[len(f.requests)]
	return f.guardrailExternalFetcher.FetchExternalRepresentation(ctx, request)
}

func TestRetainedEvidenceRevalidationRetrievalBudget(t *testing.T) {
	for _, mode := range []string{"exhausted", "overflow at minimum fetch limit", "exact remaining bytes"} {
		t.Run(mode, func(t *testing.T) {
			f, repo, withdrawer := revalidationFailureFixture(t)
			revision, _ := retainedPluginRevision(t)
			second := revision.Representations[0]
			second.ID, second.Role = "second-plugin", RepresentationDistribution
			second.URL = "https://example.com/second.zip"
			repo.revision.Representations = []Representation{revision.Representations[0], second}
			repo.resource.Kind = KindPlugin
			var err error
			repo.revision.ContentSetDigest, err = contentSetDigest(repo.revision.Representations)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidatePackageRevisionDigest(repo.revision); err != nil {
				t.Fatal(err)
			}
			before, err := json.Marshal(repo.revision)
			if err != nil {
				t.Fatal(err)
			}
			// Remote bytes can grow after review. The retained archives themselves
			// remain small and valid; fresh retrievals consume the shared budget.
			firstSize, secondSize := MaxCatalogDocumentBytes, 0
			if mode != "exhausted" {
				firstSize, secondSize = MaxCatalogDocumentBytes-512, 1024
			}
			if mode == "exact remaining bytes" {
				secondSize = 512
			}
			fetcher := &retainedSequenceFetcher{guardrailExternalFetcher: f.fetcher, bodies: [][]byte{bytes.Repeat([]byte{'x'}, firstSize), bytes.Repeat([]byte{'y'}, secondSize)}}
			f.service.externalFetcher = fetcher
			f.service.validator = guardrailValidator(func(context.Context, Kind, RepresentationInput) ValidationResult {
				return ValidationResult{ProfileID: PluginProfile}
			})
			_, evidence, _, err := f.service.RevalidateVersionRevision(f.adminContext(), repo.revision.ID)
			wantCalls := 2
			if mode == "exhausted" {
				wantCalls = 1
			}
			if mode == "exact remaining bytes" {
				if err != nil || evidence.State != EvidenceBlocked || len(evidence.ExternalRetrievals) != 2 || len(evidence.Findings) != 2 || evidence.SubjectDigest != repo.revision.ContentSetDigest || len(repo.runs) != 1 {
					t.Fatalf("bounded drift evidence=%+v error=%v writes=%d", evidence, err, len(repo.runs))
				}
				for i, retrieval := range evidence.ExternalRetrievals {
					if retrieval.RepresentationID != repo.revision.Representations[i].ID || retrieval.ContentDigest != digestBytes(fetcher.bodies[i]) || retrieval.Size != int64(len(fetcher.bodies[i])) {
						t.Fatalf("retrieval %d lost observed identity: %+v", i, retrieval)
					}
					if evidence.Findings[i].Code != "EXTERNAL_CONTENT_CHANGED" || evidence.Findings[i].Severity != SeverityError {
						t.Fatalf("bounded drift finding %d: %+v", i, evidence.Findings[i])
					}
				}
				if !reflect.DeepEqual(repo.runs[0].Evidence, evidence) {
					t.Fatal("returned evidence differs from persisted evidence")
				}
			} else if ErrorCodeOf(err) != CodeDependencyUnavailable || len(repo.runs) != 0 || repo.selections != 0 || len(withdrawer.calls) != 0 {
				t.Fatalf("budget error=%v writes=%d selections=%d withdrawals=%v", err, len(repo.runs), repo.selections, withdrawer.calls)
			}
			if len(fetcher.requests) != wantCalls {
				t.Fatalf("fetches=%d; want %d", len(fetcher.requests), wantCalls)
			}
			for i, request := range fetcher.requests {
				wantLimit := int64(MaxCatalogDocumentBytes)
				if i == 1 {
					wantLimit = 1024
				}
				if request.TenantID != guardrailTenantID || request.URL != repo.revision.Representations[i].URL || request.MaxResponseBytes != wantLimit {
					t.Fatalf("fetch %d lost scope or budget: %+v", i, request)
				}
			}
			after, err := json.Marshal(repo.revision)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("revalidation rewrote immutable revision")
			}
		})
	}
}

func TestRetainedEvidenceRevalidationDocumentMismatch(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"digest", "media", "size"} {
		t.Run(field, func(t *testing.T) {
			f, repo, withdrawer := revalidationFailureFixture(t)
			body := []byte(`{}`)
			digest := digestBytes(body)
			document := DocumentBlob{ID: strings.TrimPrefix(digest, "sha256:"), TenantID: guardrailTenantID, Digest: digest, MediaType: "application/json", Size: int64(len(body)), Content: body, CreatedAt: f.clock.now}
			if err := ValidateDocumentBlob(guardrailTenantID, document); err != nil {
				t.Fatal(err)
			}
			r := &repo.revision.Representations[0]
			r.Data, r.ArtifactID, r.URL = nil, document.ID, "https://example.com/agent.json"
			switch field {
			case "digest":
				r.ContentDigest = digestBytes([]byte(`{"changed":true}`))
			case "media":
				r.MediaType = "text/plain"
			case "size":
				r.Size++
			}
			var err error
			repo.revision.ContentSetDigest, err = contentSetDigest(repo.revision.Representations)
			if err != nil {
				t.Fatal(err)
			}
			reader := &retainedDocumentReader{Repository: repo, document: document}
			f.service.repo = reader
			f.service.validator = guardrailValidator(func(context.Context, Kind, RepresentationInput) ValidationResult {
				t.Fatal("mismatched retained bytes reached validation")
				return ValidationResult{}
			})
			revision, evidence, _, err := f.service.RevalidateVersionRevision(f.adminContext(), repo.revision.ID)
			if ErrorCodeOf(err) != CodeDependencyUnavailable || revision.ID != "" || evidence.ID != "" || len(repo.runs) != 0 || len(f.fetcher.requests) != 0 || len(withdrawer.calls) != 0 {
				t.Fatalf("error=%v writes=%d fetches=%d withdrawals=%v", err, len(repo.runs), len(f.fetcher.requests), withdrawer.calls)
			}
			if !reflect.DeepEqual(reader.requests, [][2]string{{guardrailTenantID, document.ID}}) {
				t.Fatalf("lost retained document binding: %v", reader.requests)
			}
		})
	}
}
