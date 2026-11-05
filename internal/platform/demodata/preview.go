package demodata

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

type PreviewItem struct {
	Key         string       `json:"key"`
	Kind        catalog.Kind `json:"kind"`
	Namespace   string       `json:"namespace"`
	State       string       `json:"state"`
	Origin      string       `json:"origin"`
	Digest      string       `json:"digest"`
	Disposition string       `json:"disposition"`
	Warnings    []string     `json:"warnings,omitempty"`
}
type Preview struct {
	Scope   string        `json:"scope"`
	Dataset string        `json:"dataset"`
	Profile string        `json:"profile"`
	Digest  string        `json:"digest"`
	Items   []PreviewItem `json:"items"`
}

// PreviewReceipt returns a local inventory comparison, never authority to adopt or mutate a record.
// N-88 receipts have no input digest: matching keys are explicitly unverified lookup hints.
func PreviewReceipt(plan Plan, filename string) (Preview, error) {
	known := map[string]bool{}
	if filename != "" {
		root, err := os.OpenRoot(filepath.Dir(filename))
		if errors.Is(err, fs.ErrNotExist) {
			result, e := PreviewReceipt(plan, "")
			for i := range result.Items {
				result.Items[i].Disposition = "candidate addition"
			}
			return result, e
		}
		if err != nil {
			return Preview{}, manifestError()
		}
		defer func() { _ = root.Close() }()
		raw, err := readAsset(root.FS(), filepath.Base(filename), 128<<10)
		if err != nil {
			if _, e := root.Lstat(filepath.Base(filename)); !errors.Is(e, fs.ErrNotExist) {
				return Preview{}, manifestError()
			}
		} else {
			var receipt struct {
				Bindings          json.RawMessage `json:"bindings,omitempty"`
				Dataset           string          `json:"dataset"`
				BaseURL           string          `json:"baseUrl"`
				TenantID          string          `json:"tenantId"`
				NamespaceID       string          `json:"namespaceId"`
				PublisherID       string          `json:"publisherId"`
				SnapshotID        string          `json:"snapshotId"`
				PluginNamespaceID string          `json:"pluginNamespaceId,omitempty"`
				ArchiveOrigin     string          `json:"archiveOrigin,omitempty"`
				Items             []struct {
					Key               string       `json:"key"`
					Kind              catalog.Kind `json:"kind"`
					State             string       `json:"state"`
					Identifier        string       `json:"identifier"`
					ResourceID        string       `json:"resourceId"`
					VersionID         string       `json:"versionId"`
					VersionRevisionID string       `json:"versionRevisionId"`
					ListingID         string       `json:"listingId"`
					ListingRevisionID string       `json:"listingRevisionId"`
					SubmissionID      string       `json:"submissionId,omitempty"`
					ReleaseID         string       `json:"releaseId,omitempty"`
				} `json:"items"`
			}
			if jsondoc.Decode(raw, &receipt) != nil || receipt.TenantID != TenantID || len(receipt.Items) > MaxResources || receipt.Items == nil {
				return Preview{}, manifestError()
			}
			for _, item := range receipt.Items {
				if !validKey(item.Key) || known[item.Key] {
					return Preview{}, manifestError()
				}
				known[item.Key] = true
			}
		}
	}
	result := Preview{Scope: "local validation only; receipt keys are unverified hints, not live API authority", Dataset: plan.Dataset, Profile: plan.Profile, Digest: plan.Digest, Items: []PreviewItem{}}
	for _, item := range plan.Resources {
		disposition := "selected"
		if filename != "" {
			disposition = "candidate addition"
			if known[item.Resource.Key] {
				disposition = "receipt key; state/content unverified"
			}
			delete(known, item.Resource.Key)
		}
		r := item.Resource
		result.Items = append(result.Items, PreviewItem{r.Key, r.Kind, r.Namespace, r.State, r.Origin, item.Digest, disposition, item.Warnings})
	}
	// Omitting a receipt key is visible; it never means deletion.
	remaining := make([]string, 0, len(known))
	for key := range known {
		remaining = append(remaining, key)
	}
	slices.Sort(remaining)
	for _, key := range remaining {
		result.Items = append(result.Items, PreviewItem{Key: key, Disposition: "receipt key outside selected profile; retained, not deleted"})
	}
	return result, nil
}

// EncodePreview writes only fixed inventory projections, never native document contents.
func EncodePreview(plan Plan, receipt string) ([]byte, error) {
	preview, err := PreviewReceipt(plan, receipt)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(preview, "", "  ")
}
