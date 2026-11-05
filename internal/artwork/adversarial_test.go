package artwork

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestSealedEvidenceCannotClaimInvalidFacts(t *testing.T) {
	service := testService(t, fetchFunc(func(context.Context, FetchRequest) (FetchResult, error) {
		return FetchResult{Body: testImage(t, "png", 2, 2)}, nil
	}), DefaultLimits())
	descriptor, err := service.Seal(t.Context(), "tenant", testInput())
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Asset){
		"digest case":         func(a *Asset) { a.Digest = "sha256:" + strings.Repeat("A", 64) },
		"digest absent":       func(a *Asset) { a.Digest = "" },
		"size zero":           func(a *Asset) { a.Size = 0 },
		"unsupported MIME":    func(a *Asset) { a.MediaType = "image/svg+xml" },
		"width zero":          func(a *Asset) { a.Width = 0 },
		"height zero":         func(a *Asset) { a.Height = 0 },
		"width cap":           func(a *Asset) { a.Width = 513 },
		"pixel cap":           func(a *Asset) { a.Width = 512; a.Height = 512 },
		"invented assessment": func(a *Asset) { a.RightsAssessment = "approved" },
	} {
		t.Run(name, func(t *testing.T) {
			asset := *descriptor.Icon
			mutate(&asset)
			limits := DefaultLimits()
			if name == "pixel cap" {
				limits.MaxIconPixels = 262143
			}
			if err := ValidateAsset(asset, limits, true, false); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid retained facts accepted: %v", err)
			}
		})
	}
	invalid := DefaultLimits()
	invalid.MaxConcurrent = 0
	if _, err := New(service.fetcher, invalid); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid service limits accepted")
	}
	if ValidateInput(testInput(), invalid) == nil || ValidateDescriptor(descriptor, invalid) == nil || ValidateAsset(*descriptor.Icon, invalid, true, false) == nil {
		t.Fatal("invalid validation budget accepted")
	}
	invalid = DefaultLimits()
	invalid.MaxScreenshots = -1
	if invalid.Validate() == nil {
		t.Fatal("negative screenshot ceiling accepted")
	}
	input := testInput()
	input.Screenshots = []AssetInput{{URL: "http://unsafe.example", Alt: "x"}}
	if ValidateInput(input, DefaultLimits()) == nil {
		t.Fatal("invalid screenshot accepted")
	}
	descriptor.Screenshots = []Asset{{}}
	if ValidateDescriptor(descriptor, DefaultLimits()) == nil {
		t.Fatal("unsealed screenshot accepted")
	}
	descriptor.Screenshots = nil
	descriptor.SchemaVersion = 2
	if ValidateDescriptor(descriptor, DefaultLimits()) == nil {
		t.Fatal("future sealed descriptor accepted as current")
	}
	limitsCopy := service.Limits()
	limitsCopy.MaxIconBytes = 1
	if service.Limits().MaxIconBytes != DefaultLimits().MaxIconBytes {
		t.Fatal("caller changed service limits")
	}
}

func TestStrictArtworkParsingRejectsBrokenOptionalContracts(t *testing.T) {
	for _, raw := range []string{`[]`, `{}`, `{"schemaVersion":"1"}`, `{"schemaVersion":1,"screenshots":{}}`, `{"schemaVersion":1,"icon":{"alt":"x","rights":{"basis":"owned","attribution":"A"}}}`, `{"schemaVersion":1,"icon":{"url":4,"alt":"x","rights":{"basis":"owned","attribution":"A"}}}`, `{"schemaVersion":1,"icon":{"url":"https://media.example/i","alt":"x","rights":{}}}`, `{"schemaVersion":1,"icon":{"url":"https://media.example/i","alt":"x","rights":{"basis":"owned"}}}`, `{"schemaVersion":1,"screenshots":[{}]}`} {
		if _, err := DecodeInput([]byte(raw), DefaultLimits()); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid authoring JSON accepted: %s", raw)
		}
		if _, err := DecodeDescriptor([]byte(raw), DefaultLimits()); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid sealed JSON accepted: %s", raw)
		}
	}
	service := testService(t, fetchFunc(func(context.Context, FetchRequest) (FetchResult, error) {
		return FetchResult{Body: testImage(t, "png", 2, 2)}, nil
	}), DefaultLimits())
	descriptor, err := service.Seal(t.Context(), "tenant", testInput())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(encoded, &doc); err != nil {
		t.Fatal(err)
	}
	icon, ok := doc["icon"].(map[string]any)
	if !ok {
		t.Fatal("sealed icon is not an object")
	}
	delete(icon, "digest")
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeDescriptor(raw, DefaultLimits()); err == nil {
		t.Fatal("missing facts accepted")
	}
	icon["digest"] = strings.Repeat("a", 71)
	raw, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeDescriptor(raw, DefaultLimits()); err == nil {
		t.Fatal("noncanonical digest accepted")
	}
	icon["digest"] = descriptor.Icon.Digest
	icon["width"] = "two"
	raw, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeDescriptor(raw, DefaultLimits()); err == nil {
		t.Fatal("wrong fact type accepted")
	}
	for _, entry := range []string{`[]`, `null`, `{"extensions":null}`, `{"extensions":[]}`, `{"extensions":{"` + IRI + `":null}}`, `{"extensions":{"` + IRI + `":{"schemaVersion":0}}}`, `{"extensions":{"` + IRI + `":{"schemaVersion":1,"extra":true}}}`, `{"extensions":{},"extensions":{}}`} {
		if _, err := ExtractAI([]byte(entry), DefaultLimits()); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid AI extension accepted: %s", entry)
		}
	}
	for _, entry := range []string{`{}`, `{"extensions":{"unrelated":true}}`, `{"extensions":{"` + IRI + `":{"schemaVersion":7}}}`} {
		if d, err := ExtractAI([]byte(entry), DefaultLimits()); err != nil || d != nil {
			t.Fatalf("absent/future optional term rejected: %s %v", entry, err)
		}
	}
	for _, entry := range []string{`[]`, `null`, `{"` + IRI + `":{"@type":"@json"}}`, `{"` + IRI + `":{"@type":"@json","@value":{"schemaVersion":null}}}`, `{"` + IRI + `":{"@type":"@json","@value":{"schemaVersion":1,"extra":true}}}`, `{"` + IRI + `":{"@type":"@json","@value":[]}}`} {
		if _, err := Extract([]byte(entry), DefaultLimits()); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid JSON literal accepted: %s", entry)
		}
	}
	invalid := DefaultLimits()
	invalid.MaxDescriptorBytes = 0
	if _, err := DecodeInput([]byte(`{"schemaVersion":1}`), invalid); err == nil {
		t.Fatal("invalid decoder budget accepted")
	}
	if _, err := ExtractAI([]byte(`{}`), invalid); err == nil {
		t.Fatal("invalid AI extraction budget accepted")
	}
	if _, err := Extract([]byte(`{}`), invalid); err == nil {
		t.Fatal("invalid JSONLD extraction budget accepted")
	}
	tiny := DefaultLimits()
	tiny.MaxDescriptorBytes = 1
	if _, err := DecodeDescriptor(encoded, tiny); err == nil {
		t.Fatal("oversized sealed JSON accepted")
	}
	if ValidateInput(testInput(), tiny) == nil {
		t.Fatal("oversized authoring object accepted")
	}
	if !ValidIconSource("native") || ValidScreenshotsSource("native") || ValidIconSource("unknown") || !ValidScreenshotsSource("publisher") {
		t.Fatal("source authority vocabulary changed")
	}
}

func TestJPEGDimensionsBoundDecodedMemory(t *testing.T) {
	// A compact highly compressible JPEG still requires its full decoded pixel
	// allocation. Pixel admission must use decoded dimensions, not wire size.
	body := testImage(t, "jpeg", 512, 512)
	limits := DefaultLimits()
	limits.MaxIconPixels = 262143
	service := testService(t, fetchFunc(func(context.Context, FetchRequest) (FetchResult, error) {
		return FetchResult{Body: body, MediaType: "image/jpeg"}, nil
	}), limits)
	asset := Asset{AssetInput: AssetInput{URL: "https://media.example/icon.jpg"}}
	if _, err := service.Deliver(t.Context(), "tenant", asset, true, true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("compressed JPEG exceeded pixel budget: %v", err)
	}
	limits.MaxIconPixels = 262144
	service = testService(t, service.fetcher, limits)
	if _, err := service.Deliver(t.Context(), "tenant", asset, true, true); err != nil {
		t.Fatalf("exact JPEG pixel/edge boundary refused: %v", err)
	}
}
