package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

const exportSelectionLimit = 1 << 20

var errExportSelectionChanged = safeerr.New("export: record disagrees with frozen selection")

type exportBatchOptions struct {
	exportOptions
	mode, query, selectionFile string
	releases                   []string
	search                     remoteSearch
	maxPages, maxRecords       int
	maxTotalBytes              int64
}

type exportSelection struct {
	Format     int                   `json:"format"`
	Origin     string                `json:"origin"`
	Complete   bool                  `json:"complete"`
	StopReason string                `json:"stopReason"`
	Items      []exportSelectionItem `json:"items"`
}

// Preview pins the identities the API selected. Explicit UUID lists need only
// releaseId; every downloaded record still passes the single-export validation.
type exportSelectionItem struct {
	ReleaseID         string       `json:"releaseId"`
	RepresentationID  string       `json:"representationId,omitempty"`
	TenantID          string       `json:"tenantId,omitempty"`
	ResourceID        string       `json:"resourceId,omitempty"`
	VersionID         string       `json:"versionId,omitempty"`
	VersionRevisionID string       `json:"versionRevisionId,omitempty"`
	ListingID         string       `json:"listingId,omitempty"`
	ListingRevisionID string       `json:"listingRevisionId,omitempty"`
	Kind              catalog.Kind `json:"kind,omitempty"`
	Version           string       `json:"version,omitempty"`
}

type exportBatchItem struct {
	Selection         exportSelectionItem `json:"selection"`
	Outcome           string              `json:"outcome"`
	Code              string              `json:"code,omitempty"`
	Disposition       string              `json:"disposition,omitempty"`
	BrowserCompatible *bool               `json:"browserCompatible,omitempty"`
}

type exportBatchReport struct {
	Format            int               `json:"format"`
	Complete          bool              `json:"complete"`
	SelectionComplete bool              `json:"selectionComplete"`
	ResponseBytes     int64             `json:"responseBytes"`
	Items             []exportBatchItem `json:"items"`
}

func parseExportBatch(args []string, getenv func(string) string) (exportBatchOptions, error) {
	var o exportBatchOptions
	o.mode = args[0]
	f := flag.NewFlagSet("export "+o.mode, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&o.outputDir, "output-dir", "", "new private directory")
	f.StringVar(&o.remote.baseURL, "base-url", getenv("CATALOG_REMOTE_BASE_URL"), "API origin")
	f.StringVar(&o.remote.tokenFile, "token-file", getenv("CATALOG_REMOTE_TOKEN_FILE"), "token file")
	f.StringVar(&o.remote.loginFile, "login-file", "", "explicit human login credentials")
	f.StringVar(&o.remote.issuerCAFile, "issuer-ca-file", "", "issuer roots for refresh")
	f.StringVar(&o.remote.caFile, "ca-file", getenv("CATALOG_REMOTE_CA_FILE"), "TLS roots")
	f.DurationVar(&o.remote.timeout, "timeout", 30*time.Second, "whole invocation deadline")
	f.Int64Var(&o.remote.maxResponseBytes, "max-response-bytes", remoteMaxBody, "response limit")
	f.IntVar(&o.maxRecords, "max-records", 64, "selection limit")
	f.Int64Var(&o.maxTotalBytes, "max-total-bytes", 64<<20, "aggregate response limit")
	if o.mode == "batch" {
		f.StringVar(&o.format, "format", "native", "native or draft")
		f.Func("release-id", "release UUID (repeatable)", func(value string) error {
			if len(o.releases) == 64 || !identity.ValidTenantID(value) {
				return errors.New("invalid selection")
			}
			o.releases = append(o.releases, strings.ToLower(value))
			return nil
		})
		f.StringVar(&o.selectionFile, "selection-file", "", "frozen local selection")
		f.StringVar(&o.representationID, "representation-id", "", "explicit representation for a single release")
		f.Int64Var(&o.maxContentBytes, "max-content-bytes", remoteMaxBody, "document limit")
	} else {
		f.StringVar(&o.query, "query", "", "search or browse")
		f.StringVar(&o.search.Text, "text", "", "Search text")
		f.StringVar(&o.search.NamespaceID, "namespace", "", "local namespace UUID")
		f.StringVar(&o.search.Publisher, "publisher", "", "Search publisher")
		f.StringVar(&o.search.License, "license", "", "Search license")
		f.Func("visibility", "visibility", func(value string) error { o.search.Visibility = catalog.Visibility(value); return nil })
		f.Func("kind", "kind", func(value string) error { o.search.Kinds = append(o.search.Kinds, catalog.Kind(value)); return nil })
		f.Func("tag", "Search tag", func(value string) error { o.search.Tags = append(o.search.Tags, value); return nil })
		f.Func("category", "Search category", func(value string) error { o.search.Categories = append(o.search.Categories, value); return nil })
		f.IntVar(&o.search.Limit, "limit", 25, "page size")
		f.IntVar(&o.maxPages, "max-pages", 10, "page budget")
		o.search.Origins = []catalog.SearchResultType{catalog.SearchResultLocal}
	}
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return o, flag.ErrHelp
		}
		return o, safeerr.New("export: invalid batch/preview flags; use catalogctl export --help")
	}
	f.Visit(func(f *flag.Flag) {
		if f.Name == "representation-id" {
			o.representationSelected = true
		}
	})
	if f.NArg() != 0 || o.outputDir == "" || o.remote.timeout <= 0 || o.remote.timeout > 2*time.Minute || o.remote.maxResponseBytes < 1 || o.remote.maxResponseBytes > 64<<20 || o.maxRecords < 1 || o.maxRecords > 64 || o.maxTotalBytes < 1 || o.maxTotalBytes > 256<<20 {
		return o, safeerr.New("export: require output, timeout within (0, 2m], records within 1..64, response bytes within 1..67108864 and total bytes within 1..268435456")
	}
	if o.mode == "preview" {
		if (o.query != "search" && o.query != "browse") || o.maxPages < 1 || o.maxPages > 100 {
			return o, safeerr.New("export: preview requires --query search|browse and pages within 1..100")
		}
		if err := o.search.Validate(); err != nil {
			return o, err
		}
		if o.query == "browse" && (len(o.search.Kinds) > 1 || o.search.Text != "" || o.search.Publisher != "" || o.search.License != "" || len(o.search.Tags)+len(o.search.Categories) != 0) {
			return o, safeerr.New("export: Browse supports namespace, one kind, visibility and page size; use Search for other filters")
		}
	} else if (o.selectionFile == "") == (len(o.releases) == 0) || o.maxContentBytes < 1 || o.maxContentBytes > 64<<20 || o.representationID != "" && (!identity.ValidTenantID(o.representationID) || len(o.releases) != 1) {
		return o, safeerr.New("export: batch requires either release UUIDs or a selection file; representation UUID requires one explicit release; content bytes within 1..67108864")
	}
	if o.mode == "batch" && (o.format != "native" && o.format != "draft" || o.format == "draft" && (o.representationID != "" || o.representationSelected)) {
		return o, safeerr.New("export: batch format must be native or draft; draft format rejects representation selectors")
	}
	if err := selectRemoteCredentials(&o.remote, getenv); err != nil {
		return o, err
	}
	return o, nil
}

func runExportBatch(ctx context.Context, args []string, getenv func(string) string, output io.Writer) error {
	o, err := parseExportBatch(args, getenv)
	if errors.Is(err, flag.ErrHelp) {
		return writeImportOutput(output, []byte(exportUsage))
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, o.remote.timeout)
	defer cancel()
	parent, name, err := openExportParent(o.outputDir)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	selection := exportSelection{Format: 1, Complete: true, StopReason: "exhausted", Items: []exportSelectionItem{}}
	if o.mode == "batch" {
		if o.selectionFile != "" {
			selection, err = readExportSelection(ctx, o.selectionFile, o.maxRecords)
			if err != nil {
				return err
			}
		} else {
			for _, id := range o.releases {
				selection.Items = append(selection.Items, exportSelectionItem{ReleaseID: id, RepresentationID: strings.ToLower(o.representationID)})
			}
			selection.Items, err = deduplicateExportSelection(selection.Items, o.maxRecords)
			if err != nil {
				return err
			}
		}
	}
	if o.format == "draft" {
		for _, item := range selection.Items {
			if item.RepresentationID != "" {
				return safeerr.New("export: draft format rejects representation selectors in selection files")
			}
		}
	}
	session, err := newRemoteSession(ctx, o.remote)
	if err != nil {
		return err
	}
	defer session.close()
	if selection.Origin != "" && selection.Origin != session.origin {
		return safeerr.New("export: selection belongs to a different API origin")
	}
	selection.Origin = session.origin
	budget := &exportByteBudget{transport: session.client.Transport, remaining: o.maxTotalBytes}
	session.client.Transport = budget
	if o.mode == "preview" {
		selection, err = previewExport(ctx, session, o)
		if err != nil {
			return err
		}
	}
	root, err := createExportDirectory(parent, name)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := saveExportJSON(ctx, root, "selection.json", selection); err != nil {
		return err
	}
	if o.mode == "preview" {
		if err := writeExportJSON(output, selection); err != nil {
			return err
		}
		if !selection.Complete {
			return safeerr.New("export: preview is incomplete; inspect selection.json before selecting a batch")
		}
		return nil
	}
	report := executeExportBatch(ctx, session, o, root, selection, budget)
	// A separate completion budget permits a bounded report after cancellation.
	// It authorizes no further API reads, downloads or per-release publication.
	reportCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	if err := saveExportJSON(reportCtx, root, "batch.json", report); err != nil {
		return err
	}
	if err := writeExportJSON(output, report); err != nil {
		return err
	}
	if !report.Complete {
		return safeerr.New("export: batch is incomplete; inspect batch.json; completed release files remain available")
	}
	return nil
}

func executeExportBatch(ctx context.Context, session *remoteSession, o exportBatchOptions, root *os.Root, selection exportSelection, budget *exportByteBudget) exportBatchReport {
	r := exportBatchReport{Format: 1, Complete: selection.Complete, SelectionComplete: selection.Complete, Items: []exportBatchItem{}}
	for _, item := range selection.Items {
		result := exportBatchItem{Selection: item, Outcome: "failed", Code: "read_failed"}
		if ctx.Err() != nil || budget.remaining <= 0 {
			result.Outcome, result.Code = "not_attempted", "budget_exhausted"
		} else {
			options := o.exportOptions
			options.releaseID, options.representationID = item.ReleaseID, item.RepresentationID
			options.selection = &item
			options.remote.path = "/api/v1/catalog/resources/" + item.ReleaseID
			manifest, content, err := prepareExport(ctx, session, options)
			if errors.Is(err, errExportSelectionChanged) {
				result.Code = "selection_changed"
			}
			if err == nil {
				err = writeExport(ctx, root, item.ReleaseID, manifest, content)
				result.Code = "file_failed"
			}
			if err == nil {
				result.Outcome, result.Code, result.Disposition = "complete", "", manifest.Disposition
				result.BrowserCompatible = manifest.BrowserCompatible
				if manifest.Disposition == "reference_only" {
					result.Outcome = "reference_only"
				}
			} else {
				var failure remoteAPIError
				if errors.As(err, &failure) {
					if failure.Status == 401 || failure.Status == 403 {
						result.Outcome, result.Code = "denied", "access_denied"
					}
					if failure.Status == 404 {
						result.Code = "not_found"
					}
				}
			}
		}
		if result.Outcome != "complete" && result.Outcome != "reference_only" {
			r.Complete = false
		}
		r.Items = append(r.Items, result)
	}
	r.ResponseBytes = o.maxTotalBytes - budget.remaining
	return r
}

func (s exportSelectionItem) matches(m exportManifest) bool {
	return s.ReleaseID == m.ReleaseID && (s.RepresentationID == "" || s.RepresentationID == m.RepresentationID) && (s.ResourceID == "" || s.TenantID == m.TenantID && s.ResourceID == m.ResourceID && s.VersionID == m.VersionID && s.VersionRevisionID == m.VersionRevisionID && s.ListingID == m.ListingID && s.ListingRevisionID == m.ListingRevisionID && s.Kind == m.Kind && s.Version == m.Version)
}

func writeExportJSON(output io.Writer, value any) error {
	// Version labels and API origins can contain Unicode. Reuse the remote
	// command's ASCII JSON encoding so terminal controls remain inert.
	raw, err := remoteJSON(value)
	if err != nil || len(raw)+1 > exportSelectionLimit {
		return safeerr.New("export: selection/report exceeds its 1 MiB limit")
	}
	if n, err := output.Write(append(raw, '\n')); err != nil || n != len(raw)+1 {
		return safeerr.New("export: cannot write result; inspect saved files")
	}
	return nil
}

func saveExportJSON(ctx context.Context, root *os.Root, name string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil || len(raw)+1 > exportSelectionLimit {
		return safeerr.New("export: selection/report exceeds its 1 MiB limit")
	}
	if err := writeExportFile(ctx, root, name, append(raw, '\n')); err != nil {
		return err
	}
	directory, err := root.Open(".")
	if err != nil {
		return safeerr.New("export: cannot synchronize output directory")
	}
	defer func() { _ = directory.Close() }()
	return safeerr.Wrap(directory.Sync(), "export: cannot synchronize output directory")
}
