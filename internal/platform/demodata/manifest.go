// A sample manifest is local authoring data, never a program or network policy.
package demodata

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/adapter"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/artwork"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/demoarchive"
	"github.com/open-agentic-catalog/open-agentic-catalog/samples"
	"go.yaml.in/yaml/v3"
)

const (
	MaxManifestBytes    = 2 << 20
	MaxResources        = 64
	MaxAssetBytes       = 1 << 20
	MaxPlanBytes        = 8 << 20
	MaxEncodedBodyBytes = 2 << 20
	MaxFiles            = 128
)

type Namespace struct {
	Key         string `json:"key"`
	DisplayName string `json:"displayName"`
}
type Publisher struct {
	Key         string   `json:"key"`
	DisplayName string   `json:"displayName"`
	Domains     []string `json:"domains"`
}
type Listing struct {
	Artwork               *artwork.Input     `json:"artwork,omitempty"`
	Tags                  []string           `json:"tags"`
	Categories            []string           `json:"categories,omitempty"`
	RepresentativeQueries []string           `json:"representativeQueries"`
	License               string             `json:"license"`
	Visibility            catalog.Visibility `json:"visibility"`
	DocumentationURL      string             `json:"documentationUrl,omitempty"`
}

// Provenance describes curation evidence, never invocation authority.
type Provenance struct {
	Source        string   `json:"source"`
	Revision      string   `json:"revision"`
	Path          string   `json:"path,omitempty"`
	Attribution   string   `json:"attribution"`
	Qualification string   `json:"qualification"`
	Prerequisites []string `json:"prerequisites"`
}

// Body admits exactly one literal or relative file. Pointers distinguish omitted from empty.
type Body struct {
	Inline *string `json:"inline,omitempty"`
	File   string  `json:"file,omitempty"`
}
type RepresentationSource struct {
	MediaType        string                `json:"mediaType"`
	Inline           *string               `json:"inline,omitempty"`
	File             string                `json:"file,omitempty"`
	PackageDirectory string                `json:"packageDirectory,omitempty"`
	Files            map[string]Body       `json:"files,omitempty"`
	Handoff          *catalog.SkillHandoff `json:"handoff,omitempty"`
}
type Resource struct {
	Key            string               `json:"key"`
	Kind           catalog.Kind         `json:"kind"`
	Name           string               `json:"name"`
	Description    string               `json:"description"`
	Namespace      string               `json:"namespace"`
	Publisher      string               `json:"publisher"`
	Identifier     string               `json:"identifier"`
	Version        string               `json:"version"`
	State          string               `json:"state"`
	Origin         string               `json:"origin"`
	Provenance     *Provenance          `json:"provenance,omitempty"`
	Listing        Listing              `json:"listing"`
	Representation RepresentationSource `json:"representation"`
}
type Manifest struct {
	Format     int                 `json:"format"`
	Dataset    string              `json:"dataset"`
	Revision   string              `json:"revision"`
	Namespaces []Namespace         `json:"namespaces"`
	Publishers []Publisher         `json:"publishers"`
	Profiles   map[string][]string `json:"profiles"`
	Resources  []Resource          `json:"resources"`
}
type PreparedResource struct {
	Resource       Resource                    `json:"resource"`
	Representation catalog.RepresentationInput `json:"representation"`
	BodyDigest     string                      `json:"bodyDigest"`
	Digest         string                      `json:"digest"`
	Archive        []byte                      `json:"-"`
	Warnings       []string                    `json:"warnings,omitempty"`
}
type Plan struct {
	Format     int                `json:"format"`
	Dataset    string             `json:"dataset"`
	Revision   string             `json:"revision"`
	Profile    string             `json:"profile"`
	Namespaces []Namespace        `json:"namespaces"`
	Publishers []Publisher        `json:"publishers"`
	Resources  []PreparedResource `json:"resources"`
	Digest     string             `json:"digest"`
}

func digest(raw []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) }
func validKey(key string) bool { return catalog.ValidSkillName(key) }
func boundedText(s string, maximum int) bool {
	return strings.TrimSpace(s) != "" && len(s) <= maximum && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}
func manifestError() error { return errors.New("invalid sample manifest, asset or selected profile") }

// Load reads one snapshot beneath the selected manifest directory. It never fetches URLs.
func Load(filename, profile, origin string) (Plan, error) {
	if filename == "" {
		return Decode(samples.Files, "catalog.yaml", profile, origin)
	}
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return Plan{}, manifestError()
	}
	for p := absolute; ; p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if e != nil || info.Mode()&os.ModeSymlink != 0 {
			return Plan{}, manifestError()
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	root, err := os.OpenRoot(filepath.Dir(absolute))
	if err != nil {
		return Plan{}, manifestError()
	}
	defer func() { _ = root.Close() }()
	return Decode(root.FS(), filepath.Base(absolute), profile, origin)
}

// Decode is shared by embedded fixtures, preview and preparation. All records are validated.
func Decode(assets fs.FS, filename, profile, origin string) (Plan, error) {
	raw, err := readAsset(assets, filename, MaxManifestBytes)
	if err != nil {
		return Plan{}, manifestError()
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var node yaml.Node
	if decoder.Decode(&node) != nil || len(node.Content) != 1 {
		return Plan{}, manifestError()
	}
	var extra yaml.Node
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return Plan{}, manifestError()
	}
	count := 0
	if !validNode(&node, 0, &count) {
		return Plan{}, manifestError()
	}
	var value any
	if node.Decode(&value) != nil {
		return Plan{}, manifestError()
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return Plan{}, manifestError()
	}
	var m Manifest
	if jsondoc.Decode(encoded, &m) != nil {
		return Plan{}, manifestError()
	}
	return prepareManifest(assets, m, profile, origin)
}
func validNode(n *yaml.Node, depth int, count *int) bool {
	*count++
	if depth > 32 || *count > 20000 || n.Anchor != "" || n.Kind == yaml.AliasNode || n.Tag == "!!null" || n.Style&yaml.TaggedStyle != 0 {
		return false
	}
	switch n.Kind {
	case yaml.AliasNode:
		return false
	case yaml.DocumentNode:
	case yaml.MappingNode:
		if n.Tag != "!!map" {
			return false
		}
		seen := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode || k.Tag != "!!str" || k.Value == "<<" || seen[k.Value] {
				return false
			}
			seen[k.Value] = true
		}
	case yaml.SequenceNode:
		if n.Tag != "!!seq" {
			return false
		}
	case yaml.ScalarNode:
		if n.Tag != "!!str" && n.Tag != "!!int" {
			return false
		}
		if !utf8.ValidString(n.Value) || strings.ContainsRune(n.Value, 0) {
			return false
		}
	default:
		return false
	}
	for _, child := range n.Content {
		if !validNode(child, depth+1, count) {
			return false
		}
	}
	return true
}

func readAsset(assets fs.FS, name string, limit int) ([]byte, error) {
	if !safePath(name) {
		return nil, manifestError()
	}
	parts := strings.Split(name, "/")
	for i := range parts {
		info, e := fs.Lstat(assets, strings.Join(parts[:i+1], "/"))
		if e != nil || info.Mode()&os.ModeSymlink != 0 || i < len(parts)-1 && !info.IsDir() || i == len(parts)-1 && (!info.Mode().IsRegular() || info.Size() > int64(limit)) {
			return nil, manifestError()
		}

	}
	file, err := assets.Open(name)
	if err != nil {
		return nil, manifestError()
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return nil, manifestError()
	}
	raw, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil || len(raw) > limit {
		return nil, manifestError()
	}
	return raw, nil
}
func safePath(name string) bool {
	return fs.ValidPath(name) && name != "." && len(name) <= 1024 && !strings.ContainsAny(name, "\\:\x00") && len(strings.Split(name, "/")) <= 16
}
func resolveBody(assets fs.FS, b Body) ([]byte, error) {
	if (b.Inline == nil) == (b.File == "") {
		return nil, manifestError()
	}
	if b.Inline != nil {
		raw := []byte(*b.Inline)
		if len(raw) > MaxAssetBytes || !utf8.Valid(raw) || bytes.ContainsRune(raw, 0) {
			return nil, manifestError()
		}
		return raw, nil
	}
	return readAsset(assets, b.File, MaxAssetBytes)
}

func prepareManifest(assets fs.FS, m Manifest, profile, origin string) (Plan, error) {
	if m.Format != 1 || !validKey(m.Dataset) || !boundedText(m.Revision, 64) || len(m.Resources) == 0 || len(m.Resources) > MaxResources || len(m.Namespaces) == 0 || len(m.Namespaces) > 8 || len(m.Publishers) == 0 || len(m.Publishers) > 8 || len(m.Profiles) == 0 || len(m.Profiles) > 8 {
		return Plan{}, manifestError()
	}
	ns := map[string]bool{}
	pub := map[string]Publisher{}
	pubNames := map[string]bool{}
	for _, n := range m.Namespaces {
		if !validKey(n.Key) || !boundedText(n.DisplayName, 256) || ns[n.Key] {
			return Plan{}, manifestError()
		}
		ns[n.Key] = true
	}
	for _, p := range m.Publishers {
		if !validKey(p.Key) || !boundedText(p.DisplayName, 256) || len(p.Domains) == 0 || len(p.Domains) > 8 || pub[p.Key].Key != "" || pubNames[p.DisplayName] {
			return Plan{}, manifestError()
		}
		seenDomains := map[string]bool{}
		for _, d := range p.Domains {
			if !validDomain(d) || seenDomains[d] {
				return Plan{}, manifestError()
			}
			seenDomains[d] = true
		}
		pubNames[p.DisplayName] = true
		pub[p.Key] = p
	}
	all := map[string]PreparedResource{}
	ids := map[string]bool{}
	total := 0
	for _, r := range m.Resources {
		if !validKey(r.Key) || all[r.Key].Resource.Key != "" || !r.Kind.Valid() || !boundedText(r.Name, 256) || !boundedText(r.Description, 4000) || !ns[r.Namespace] || pub[r.Publisher].Key == "" || !boundedText(r.Version, 128) || !slices.Contains([]string{"published", "draft", "pending_review"}, r.State) || !slices.Contains([]string{"synthetic", "adapted", "upstream"}, r.Origin) || !r.Listing.Visibility.Valid() || !boundedText(r.Listing.License, 256) || len(r.Listing.Tags) > 32 || len(r.Listing.RepresentativeQueries) > 8 || ids[r.Identifier] {
			return Plan{}, manifestError()
		}
		identifier, e := catalog.ParseAIRIdentifier(r.Identifier)
		if e != nil || !slices.Contains(pub[r.Publisher].Domains, identifier.Publisher) {
			return Plan{}, manifestError()
		}
		ids[r.Identifier] = true
		if r.State == "published" && r.Listing.Visibility == catalog.VisibilityPublic && (len(r.Listing.RepresentativeQueries) < 2 || len(r.Listing.RepresentativeQueries) > 5) {
			return Plan{}, manifestError()
		}
		if r.Listing.DocumentationURL != "" && !catalog.ValidExternalHTTPSURL(r.Listing.DocumentationURL) {
			return Plan{}, manifestError()
		}
		if r.Origin != "synthetic" && r.Provenance == nil {
			return Plan{}, manifestError()
		}
		if p := r.Provenance; p != nil {
			localSource := strings.HasPrefix(p.Source, "local:") && p.Source == "local:"+p.Path && safePath(p.Path)
			if (!catalog.ValidExternalHTTPSURL(p.Source) && !localSource) || !boundedText(p.Revision, 128) || !boundedText(p.Attribution, 1000) || !slices.Contains([]string{"local-client", "source-reviewed", "provider-docs", "package-validated"}, p.Qualification) || len(p.Prerequisites) == 0 || len(p.Prerequisites) > 8 || len(p.Path) > 1024 {
				return Plan{}, manifestError()
			}
			for _, prerequisite := range p.Prerequisites {
				if !boundedText(prerequisite, 500) {
					return Plan{}, manifestError()
				}
			}
		}
		for _, v := range r.Listing.Tags {
			if !boundedText(v, 128) {
				return Plan{}, manifestError()
			}
		}
		if len(r.Listing.Categories) > 16 {
			return Plan{}, manifestError()
		}
		for _, value := range r.Listing.Categories {
			if !utf8.ValidString(value) || strings.TrimSpace(value) == "" || utf8.RuneCountInString(value) > 80 || strings.ContainsRune(value, 0) {
				return Plan{}, manifestError()
			}
		}
		for _, v := range r.Listing.RepresentativeQueries {
			if !boundedText(v, 500) {
				return Plan{}, manifestError()
			}
		}
		if r.Listing.Artwork != nil {
			input := *r.Listing.Artwork
			input.Screenshots = slices.Clone(input.Screenshots)
			resolve := func(asset artwork.AssetInput) artwork.AssetInput {
				if origin != "" {
					asset.URL = strings.Replace(asset.URL, "https://127.0.0.1:18443/", origin+"/", 1)
				}
				return asset
			}
			if input.Icon != nil {
				icon := resolve(*input.Icon)
				input.Icon = &icon
			}
			for i := range input.Screenshots {
				input.Screenshots[i] = resolve(input.Screenshots[i])
			}
			if artwork.ValidateInput(input, artwork.DefaultLimits()) != nil {
				return Plan{}, manifestError()
			}
			r.Listing.Artwork = &input
		}
		item, e := prepareResource(assets, r, origin)
		if e != nil {
			return Plan{}, fmt.Errorf("sample %s: %w", r.Key, manifestError())
		}
		total += len(item.Archive) + len(item.Representation.Data) + len(item.Representation.Content) + len(item.Representation.PackageData)
		if total > MaxPlanBytes {
			return Plan{}, manifestError()
		}
		all[r.Key] = item
	}
	for key, keys := range m.Profiles {
		seen := map[string]bool{}
		if !validKey(key) || len(keys) == 0 || len(keys) > MaxResources {
			return Plan{}, manifestError()
		}
		for _, key := range keys {
			if all[key].Resource.Key == "" || seen[key] {
				return Plan{}, manifestError()
			}
			seen[key] = true
		}
	}
	keys, ok := m.Profiles[profile]
	if !ok {
		return Plan{}, manifestError()
	}
	result := Plan{Format: 1, Dataset: m.Dataset, Revision: m.Revision, Profile: profile}
	selectedNS := map[string]bool{}
	selectedPub := map[string]bool{}
	for _, key := range keys {
		item := all[key]
		result.Resources = append(result.Resources, item)
		selectedNS[item.Resource.Namespace] = true
		selectedPub[item.Resource.Publisher] = true
	}
	for _, n := range m.Namespaces {
		if selectedNS[n.Key] {
			result.Namespaces = append(result.Namespaces, n)
		}
	}
	for _, p := range m.Publishers {
		if selectedPub[p.Key] {
			result.Publishers = append(result.Publishers, p)
		}
	}
	canonicalizePlanDigest(&result)
	return result, nil
}

func canonicalizePlanDigest(result *Plan) {
	for i := range result.Resources {
		item := &result.Resources[i]
		identity := *item
		identity.Digest = ""
		identity.Resource.Representation = RepresentationSource{}
		raw, err := json.Marshal(identity)
		if err != nil {
			panic("invalid prepared sample identity")
		}
		item.Digest = digest(raw)
	}
	canonical := *result
	canonical.Digest = ""
	canonical.Namespaces = slices.Clone(result.Namespaces)
	canonical.Publishers = slices.Clone(result.Publishers)
	canonical.Resources = slices.Clone(result.Resources)
	slices.SortFunc(canonical.Namespaces, func(a, b Namespace) int { return strings.Compare(a.Key, b.Key) })
	slices.SortFunc(canonical.Publishers, func(a, b Publisher) int { return strings.Compare(a.Key, b.Key) })
	slices.SortFunc(canonical.Resources, func(a, b PreparedResource) int { return strings.Compare(a.Resource.Key, b.Resource.Key) })
	for i := range canonical.Resources {
		canonical.Resources[i].Resource.Representation = RepresentationSource{}
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		panic("invalid prepared sample identity")
	}
	result.Digest = digest(raw)
}

func prepareResource(assets fs.FS, r Resource, origin string) (PreparedResource, error) {
	source := r.Representation
	rep := catalog.RepresentationInput{Role: catalog.RepresentationPrimary, MediaType: source.MediaType, Handoff: source.Handoff}
	packageInput := source.PackageDirectory != "" || len(source.Files) > 0
	if packageInput && (source.Inline != nil || source.File != "") || source.PackageDirectory != "" && len(source.Files) > 0 {
		return PreparedResource{}, manifestError()
	}
	var raw []byte
	var err error
	if packageInput {
		raw, err = packageBytes(assets, source)
	} else {
		raw, err = resolveBody(assets, Body{Inline: source.Inline, File: source.File})
	}
	if err != nil || len(raw) == 0 {
		return PreparedResource{}, manifestError()
	}
	result := PreparedResource{Resource: r, BodyDigest: digest(raw)}
	if r.Kind == catalog.KindPlugin {
		if !packageInput || source.MediaType != catalog.PluginMediaZIP || len(raw) > demoarchive.MaxArchiveBytes || source.Handoff != nil {
			return PreparedResource{}, manifestError()
		}
		inspection := adapter.InspectPlugin(context.Background(), source.MediaType, raw, catalog.DefaultDiagnosticLimits())
		for _, f := range inspection.Findings {
			if f.Severity == catalog.SeverityError {
				return PreparedResource{}, manifestError()
			}
			result.Warnings = append(result.Warnings, f.Code)
		}
		if origin != "" && demoarchive.ValidateOrigin(origin) != nil {
			return PreparedResource{}, manifestError()
		}
		rep.URL = strings.TrimSuffix(origin, "/") + "/" + r.Key + ".zip"
		rep.ExpectedDigest = &result.BodyDigest
		result.Archive = raw
	} else {
		if packageInput {
			if r.Kind != catalog.KindSkill || source.MediaType != catalog.SkillPackageMediaZIP || source.Handoff == nil {
				return PreparedResource{}, manifestError()
			}
			rep.PackageData = raw
		} else {
			if source.Handoff != nil {
				return PreparedResource{}, manifestError()
			}
			if strings.HasSuffix(source.MediaType, "+json") || source.MediaType == "application/json" {
				if jsondoc.Validate(raw) != nil {
					return PreparedResource{}, manifestError()
				}
				rep.Data = raw
			} else {
				if !utf8.Valid(raw) || bytes.ContainsRune(raw, 0) {
					return PreparedResource{}, manifestError()
				}
				rep.Content = string(raw)
			}
		}
		validation := adapter.NewRegistry().Validate(context.Background(), r.Kind, rep)
		for _, f := range validation.Findings {
			if f.Severity == catalog.SeverityError {
				return PreparedResource{}, manifestError()
			}
			result.Warnings = append(result.Warnings, f.Code)
		}
	}
	result.Representation = rep
	// Check the actual base64/JSON envelope, not just unencoded native bytes.
	encoded, err := json.Marshal(catalog.CreateVersionInput{Version: r.Version, Representations: []catalog.RepresentationInput{rep}})
	if err != nil || len(encoded) > MaxEncodedBodyBytes {
		return PreparedResource{}, manifestError()
	}

	return result, nil
}

func validDomain(d string) bool {
	id, e := catalog.ParseAIRIdentifier("urn:air:" + d + ":example")
	return e == nil && id.Publisher == d
}
