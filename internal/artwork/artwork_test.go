package artwork

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type fetchFunc func(context.Context, FetchRequest) (FetchResult, error)

func (f fetchFunc) Fetch(ctx context.Context, r FetchRequest) (FetchResult, error) { return f(ctx, r) }

func testImage(t *testing.T, format string, width, height int) []byte {
	t.Helper()
	picture := image.NewNRGBA(image.Rect(0, 0, width, height))
	picture.Set(0, 0, color.NRGBA{R: 40, G: 120, B: 230, A: 255})
	var out bytes.Buffer
	var err error
	if format == "png" {
		err = png.Encode(&out, picture)
	} else {
		err = jpeg.Encode(&out, picture, &jpeg.Options{Quality: 80})
	}
	if err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func testInput() Input {
	return Input{SchemaVersion: 1, Icon: &AssetInput{URL: "https://media.example/icon.png", Alt: "Catalog icon", Rights: &Rights{Basis: "owned", Attribution: "Example author"}}}
}

func testService(t *testing.T, f Fetcher, l Limits) *Service {
	t.Helper()
	s, err := New(f, l)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSealAndDeliverExactEvidence(t *testing.T) {
	for _, format := range []string{"png", "jpeg"} {
		t.Run(format, func(t *testing.T) {
			body := testImage(t, format, 3, 2)
			var calls int
			service := testService(t, fetchFunc(func(_ context.Context, r FetchRequest) (FetchResult, error) {
				calls++
				if r.TenantID != "tenant-a" || r.URL != "https://media.example/icon.png" || r.MaxBytes != 256<<10 || r.Timeout != 3*time.Second {
					t.Fatalf("wrong fetch envelope: %#v", r)
				}
				return FetchResult{Body: body, MediaType: "application/octet-stream"}, nil
			}), DefaultLimits())
			input := testInput()
			d, err := service.Seal(context.Background(), "tenant-a", input)
			if err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256(body)
			if d.Icon == nil || d.Icon.Digest != "sha256:"+hex.EncodeToString(hash[:]) || d.Icon.Size != int64(len(body)) || d.Icon.Width != 3 || d.Icon.Height != 2 || d.Icon.MediaType != "image/"+format || d.Icon.RightsAssessment != NotAssessed {
				t.Fatalf("wrong evidence: %#v", d.Icon)
			}
			input.Icon.Rights.Attribution = "changed caller data"
			if d.Icon.Rights.Attribution != "Example author" {
				t.Fatal("input aliases retained rights")
			}
			delivery, err := service.Deliver(context.Background(), "tenant-a", *d.Icon, true, false)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(delivery.Body, body) || delivery.Digest != d.Icon.Digest || delivery.MediaType != d.Icon.MediaType || calls != 2 {
				t.Fatalf("incorrect delivery: %#v, calls %d", delivery, calls)
			}
			changed := *d.Icon
			changed.Digest = "sha256:" + strings.Repeat("0", 64)
			if _, err = service.Deliver(context.Background(), "tenant-a", changed, true, false); !errors.Is(err, ErrDrift) {
				t.Fatalf("digest drift accepted: %v", err)
			}
			changed = *d.Icon
			changed.Width = 4
			if _, err = service.Deliver(context.Background(), "tenant-a", changed, true, false); !errors.Is(err, ErrDrift) {
				t.Fatalf("dimension drift accepted: %v", err)
			}
		})
	}
}

func TestInvalidAuthoringDoesNotFetch(t *testing.T) {
	var calls int
	service := testService(t, fetchFunc(func(context.Context, FetchRequest) (FetchResult, error) { calls++; return FetchResult{}, nil }), DefaultLimits())
	cases := map[string]func(*Input){
		"future version":           func(i *Input) { i.SchemaVersion = 2 },
		"absent rights":            func(i *Input) { i.Icon.Rights = nil },
		"invented verified rights": func(i *Input) { i.Icon.Rights.Basis = "verified" },
		"empty attribution":        func(i *Input) { i.Icon.Rights.Attribution = " " },
		"missing alt":              func(i *Input) { i.Icon.Alt = "" },
		"alt codepoint bound":      func(i *Input) { i.Icon.Alt = strings.Repeat("é", 161) },
		"plain HTTP":               func(i *Input) { i.Icon.URL = "http://media.example/icon.png" },
		"userinfo":                 func(i *Input) { i.Icon.URL = "https://secret@media.example/icon.png" },
		"fragment":                 func(i *Input) { i.Icon.URL = "https://media.example/icon.png#secret" },
		"url bytes":                func(i *Input) { i.Icon.URL = "https://media.example/" + strings.Repeat("é", 1024) },
		"rights reference":         func(i *Input) { i.Icon.Rights.Reference = "data:image/png,bytes" },
		"screenshot count":         func(i *Input) { i.Screenshots = make([]AssetInput, 5) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			input := testInput()
			change(&input)
			if _, err := service.Seal(context.Background(), "tenant", input); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid authoring accepted: %v", err)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("invalid authoring contacted fetcher %d times", calls)
	}
	input := testInput()
	input.Icon.Alt = strings.Repeat("é", 160)
	if err := ValidateInput(input, DefaultLimits()); err != nil {
		t.Fatalf("Unicode codepoint control rejected: %v", err)
	}
}

func TestImageValidationAndNativeMutation(t *testing.T) {
	validPNG := testImage(t, "png", 2, 2)
	validJPEG := testImage(t, "jpeg", 2, 2)
	cases := []struct {
		name   string
		body   []byte
		media  string
		change func(*Limits)
		valid  bool
	}{
		{"PNG concrete", validPNG, "image/png; charset=binary", nil, true},
		{"JPEG missing type", validJPEG, "", nil, true},
		{"JPEG generic", validJPEG, "application/octet-stream", nil, true},
		{"contradictory type", validPNG, "image/jpeg", nil, false},
		{"malformed media type", validPNG, "image/png; !!!", nil, false},
		{"SVG payload", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), "image/png", nil, false},
		{"PNG tail", append(bytes.Clone(validPNG), 0), "image/png", nil, false},
		{"JPEG tail", append(bytes.Clone(validJPEG), 0), "image/jpeg", nil, false},
		{"truncated PNG", validPNG[:len(validPNG)-1], "image/png", nil, false},
		{"truncated JPEG", validJPEG[:len(validJPEG)-1], "image/jpeg", nil, false},
		{"edge bound", validPNG, "image/png", func(l *Limits) { l.MaxIconEdge = 1 }, false},
		{"pixel bound", validPNG, "image/png", func(l *Limits) { l.MaxIconPixels = 3 }, false},
		{"pixel boundary", validPNG, "image/png", func(l *Limits) { l.MaxIconPixels = 4 }, true},
		{"encoded boundary", validPNG, "image/png", func(l *Limits) { l.MaxIconBytes = int64(len(validPNG)) }, true},
		{"encoded size plus one", validPNG, "image/png", func(l *Limits) { l.MaxIconBytes = int64(len(validPNG) - 1) }, false},
		{"empty image", nil, "image/png", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := DefaultLimits()
			if tc.change != nil {
				tc.change(&l)
			}
			s := testService(t, fetchFunc(func(context.Context, FetchRequest) (FetchResult, error) {
				return FetchResult{Body: tc.body, MediaType: tc.media}, nil
			}), l)
			a := Asset{AssetInput: AssetInput{URL: "https://media.example/icon"}}
			_, err := s.Deliver(context.Background(), "tenant", a, true, true)
			if tc.valid && err != nil || !tc.valid && !errors.Is(err, ErrInvalid) {
				t.Fatalf("valid=%v, err=%v", tc.valid, err)
			}
		})
	}
	var body = []byte{}
	s := testService(t, fetchFunc(func(context.Context, FetchRequest) (FetchResult, error) { return FetchResult{Body: body}, nil }), DefaultLimits())
	a := Asset{AssetInput: AssetInput{URL: "https://media.example/icon"}}
	body = validPNG
	first, err := s.Deliver(context.Background(), "tenant", a, true, true)
	if err != nil {
		t.Fatal(err)
	}
	body = validJPEG
	second, err := s.Deliver(context.Background(), "tenant", a, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest == second.Digest || second.MediaType != "image/jpeg" {
		t.Fatal("mutable native icon did not reflect current validated bytes")
	}
	if _, err = s.Deliver(context.Background(), "tenant", a, true, false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsealed native accepted as sealed evidence: %v", err)
	}
}

func pngChunk(kind string, data []byte) []byte {
	length := len(data)
	if length > 2<<20 {
		panic("test PNG chunk exceeds media hard ceiling")
	}
	body := make([]byte, 12+length)
	binary.BigEndian.PutUint32(body, uint32(length))
	copy(body[4:], kind)
	copy(body[8:], data)
	binary.BigEndian.PutUint32(body[len(body)-4:], crc32.ChecksumIEEE(body[4:len(body)-4]))
	return body
}

func TestRejectAnimationAndMalformedChunks(t *testing.T) {
	original := testImage(t, "png", 2, 2)
	for _, kind := range []string{"acTL", "fcTL", "fdAT"} {
		t.Run(kind, func(t *testing.T) {
			candidate := append(bytes.Clone(original[:33]), pngChunk(kind, make([]byte, 8))...)
			candidate = append(candidate, original[33:]...)
			// The standard static decoder accepts this ancillary chunk; the media
			// contract must independently reject it rather than rely on Decode.
			if _, err := png.Decode(bytes.NewReader(candidate)); err != nil {
				t.Fatalf("invalid control fixture: %v", err)
			}
			if err := validatePNG(candidate); !errors.Is(err, ErrInvalid) {
				t.Fatalf("animation chunk accepted: %v", err)
			}
		})
	}
	ancillary := pngChunk("tEXt", []byte("note\x00acTL fcTL fdAT"))
	control := append(bytes.Clone(original[:33]), ancillary...)
	control = append(control, original[33:]...)
	if err := validatePNG(control); err != nil {
		t.Fatalf("marker text confused with chunk: %v", err)
	}
	corrupt := bytes.Clone(control)
	corrupt[41] ^= 1
	if err := validatePNG(corrupt); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ancillary CRC ignored: %v", err)
	}
	huge := bytes.Clone(original)
	binary.BigEndian.PutUint32(huge[8:12], ^uint32(0))
	if err := validatePNG(huge); !errors.Is(err, ErrInvalid) {
		t.Fatalf("overflow chunk accepted: %v", err)
	}
}

func TestDecodeClosedDescriptorsAndJSONLiterals(t *testing.T) {
	input := testInput()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodeInput(raw, DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	cases := []string{
		`{"schemaVersion":1,"schemaVersion":1}`,
		`{"SchemaVersion":1}`,
		`{"schemaVersion":1,"icon":null}`,
		`{"schemaVersion":1,"screenshots":null}`,
		`{"schemaVersion":1,"extra":true}`,
		`{"schemaVersion":1} {}`,
		`{"schemaVersion":1,"icon":{"url":"https://x.example/i","alt":"\ud800","rights":{"basis":"owned","attribution":"A"}}}`,
		strings.Replace(string(raw), `"rights":`, `"Rights":`, 1),
		strings.Replace(string(raw), `"attribution":`, `"Attribution":`, 1),
		strings.Replace(string(raw), `"basis":"owned"`, `"basis":"owned","basis":"permission"`, 1),
	}
	for _, candidate := range cases {
		if _, err := DecodeInput([]byte(candidate), DefaultLimits()); !errors.Is(err, ErrInvalid) {
			t.Fatalf("ambiguous input accepted: %q, %v", candidate, err)
		}
	}
	body := testImage(t, "png", 2, 3)
	s := testService(t, fetchFunc(func(context.Context, FetchRequest) (FetchResult, error) { return FetchResult{Body: body}, nil }), DefaultLimits())
	d, err := s.Seal(context.Background(), "tenant", input)
	if err != nil {
		t.Fatal(err)
	}
	d.Screenshots = []Asset{*d.Icon, *d.Icon}
	d.Screenshots[0].Alt = "First"
	d.Screenshots[1].Alt = "Second"
	sealed, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeDescriptor(sealed, DefaultLimits())
	if err != nil || decoded.Icon.Digest != d.Icon.Digest {
		t.Fatalf("sealed decode: %#v %v", decoded, err)
	}
	for _, wrapper := range []any{map[string]any{"@type": "@json", "@value": d}, []any{map[string]any{"@type": "@json", "@value": d}}} {
		entry, marshalErr := json.Marshal(map[string]any{IRI: wrapper, "@id": "urn:example:entry"})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		got, err := Extract(entry, DefaultLimits())
		if err != nil || got == nil || len(got.Screenshots) != 2 || got.Screenshots[0].Alt != "First" || got.Screenshots[1].Alt != "Second" {
			t.Fatalf("JSON literal/order lost: %#v %v", got, err)
		}
	}
	unknown, marshalErr := json.Marshal(map[string]any{IRI: map[string]any{"@type": "@json", "@value": map[string]any{"schemaVersion": 2, "futureField": true}}})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if got, err := Extract(unknown, DefaultLimits()); err != nil || got != nil {
		t.Fatalf("unknown version not ignored: %#v %v", got, err)
	}
	if got, err := Extract([]byte(`{"unrelated":true}`), DefaultLimits()); err != nil || got != nil {
		t.Fatalf("absent term: %#v %v", got, err)
	}
	for _, wrapper := range []any{d, map[string]any{"@type": "@id", "@value": d}, []any{}, []any{map[string]any{"@type": "@json", "@value": d}, map[string]any{"@type": "@json", "@value": d}}, map[string]any{"@type": "@json", "@value": d, "extra": true}} {
		entry, marshalErr := json.Marshal(map[string]any{IRI: wrapper})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if _, err := Extract(entry, DefaultLimits()); !errors.Is(err, ErrInvalid) {
			t.Fatalf("malformed wrapper accepted: %#v %v", wrapper, err)
		}
	}
}

func TestBoundedAdmissionAndCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		body := testImage(t, "png", 1, 1)
		entered := make(chan struct{}, 4)
		release := make(chan struct{})
		var count atomic.Int32
		s := testService(t, fetchFunc(func(ctx context.Context, _ FetchRequest) (FetchResult, error) {
			count.Add(1)
			entered <- struct{}{}
			select {
			case <-release:
				return FetchResult{Body: body}, nil
			case <-ctx.Done():
				return FetchResult{}, ctx.Err()
			}
		}), DefaultLimits())
		a := Asset{AssetInput: AssetInput{URL: "https://media.example/icon"}}
		done := make(chan error, 4)
		for range 4 {
			go func() { _, err := s.Deliver(context.Background(), "tenant", a, true, true); done <- err }()
		}
		for range 4 {
			<-entered
		}
		if _, err := s.Deliver(context.Background(), "tenant", a, true, true); !errors.Is(err, ErrBusy) {
			t.Fatalf("capacity admitted excess fetch: %v", err)
		}
		if count.Load() != 4 {
			t.Fatal("excess operation reached fetcher")
		}
		close(release)
		for range 4 {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := s.Deliver(ctx, "tenant", a, true, true); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled fetch: %v", err)
		}
		if count.Load() != 4 {
			t.Fatal("canceled request reached fetcher")
		}
		ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		if _, err := s.Seal(ctx, "tenant", testInput()); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("response reserve not enforced: %v", err)
		}
		if count.Load() != 4 {
			t.Fatal("exhausted operation reached fetcher")
		}
	})
	synctest.Test(t, func(t *testing.T) {
		s := testService(t, fetchFunc(func(ctx context.Context, _ FetchRequest) (FetchResult, error) {
			<-ctx.Done()
			return FetchResult{}, ctx.Err()
		}), DefaultLimits())
		start := time.Now()
		_, err := s.Deliver(context.Background(), "tenant", Asset{AssetInput: AssetInput{URL: "https://media.example/icon"}}, true, true)
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != 3*time.Second {
			t.Fatalf("fetch timeout: %s %v", time.Since(start), err)
		}
	})
}

func TestLimitsAndSafeFetcherFailure(t *testing.T) {
	if err := DefaultLimits().Validate(); err != nil {
		t.Fatal(err)
	}
	if err := HardLimits().Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := New(nil, DefaultLimits()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing fetcher: %v", err)
	}
	limits := HardLimits()
	limits.MaxConcurrent++
	if err := limits.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unbounded concurrency: %v", err)
	}
	limits = DefaultLimits()
	limits.MaxScreenshotPixels = 0
	if err := limits.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero pixel limit: %v", err)
	}
	const secret = "secret upstream URL?token=synthetic"
	s := testService(t, fetchFunc(func(context.Context, FetchRequest) (FetchResult, error) { return FetchResult{}, errors.New(secret) }), DefaultLimits())
	_, err := s.Seal(context.Background(), "tenant", testInput())
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), secret) {
		t.Fatalf("unsafe upstream failure: %v", err)
	}
}
