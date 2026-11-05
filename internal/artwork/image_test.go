package artwork

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/jpeg"
	"testing"
)

func TestRasterFramingRejectsMalformedDocuments(t *testing.T) {
	png := testImage(t, "png", 2, 3)
	jpg := testImage(t, "jpeg", 2, 3)
	// Marker bytes inside an ordinary JPEG comment are data, not an early EOI.
	comment := append([]byte{0xff, 0xd8, 0xff, 0xfe, 0, 4, 0xff, 0xd9}, jpg[2:]...)
	if _, err := jpeg.Decode(bytes.NewReader(comment)); err != nil {
		t.Fatalf("invalid JPEG comment control: %v", err)
	}
	for name, control := range map[string]struct {
		body     []byte
		validate func([]byte) error
	}{"PNG": {png, validatePNG}, "JPEG": {jpg, validateJPEG}, "JPEG comment": {comment, validateJPEG}} {
		if err := control.validate(control.body); err != nil {
			t.Fatalf("valid %s rejected: %v", name, err)
		}
	}
	jpegPrefix := func(segment ...byte) []byte {
		body := append([]byte{0xff, 0xd8}, segment...)
		return append(body, jpg[2:]...)
	}
	for _, tc := range []struct {
		name     string
		body     []byte
		validate func([]byte) error
	}{
		{"PNG signature", []byte("not a PNG"), validatePNG},
		{"PNG truncated chunk header", png[:len(png)-5], validatePNG},
		{"PNG missing IEND", png[:len(png)-12], validatePNG},
		{"PNG nonempty IEND", append(bytes.Clone(png[:len(png)-12]), pngChunk("IEND", []byte{1})...), validatePNG},
		{"JPEG signature", []byte{0, 0, 0, 0}, validateJPEG},
		{"JPEG unframed byte", jpegPrefix(0), validateJPEG},
		{"JPEG stuffed byte outside scan", jpegPrefix(0xff, 0), validateJPEG},
		{"JPEG duplicate SOI", jpegPrefix(0xff, 0xd8), validateJPEG},
		{"JPEG temporary marker", jpegPrefix(0xff, 1), validateJPEG},
		{"JPEG restart outside scan", jpegPrefix(0xff, 0xd0), validateJPEG},
		{"JPEG truncated segment length", []byte{0xff, 0xd8, 0xff, 0xfe, 0}, validateJPEG},
		{"JPEG short segment", jpegPrefix(0xff, 0xfe, 0, 1), validateJPEG},
		{"JPEG segment overrun", jpegPrefix(0xff, 0xfe, 0xff, 0xff), validateJPEG},
		{"JPEG missing EOI", jpg[:len(jpg)-2], validateJPEG},
		{"JPEG unfinished marker", append(bytes.Clone(jpg[:len(jpg)-2]), 0xff), validateJPEG},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.validate(tc.body); !errors.Is(err, ErrInvalid) {
				t.Fatalf("malformed raster framing accepted: %v", err)
			}
		})
	}
}

func TestRasterRequiresCompleteDecodeAfterValidHeaderAndFraming(t *testing.T) {
	body := bytes.Clone(testImage(t, "png", 2, 3))
	// Keep the IHDR, chunk lengths and CRCs valid, but corrupt the compressed
	// pixels. Header and framing checks alone must not seal an undecodable image.
	found := false
	for offset := 8; offset < len(body); {
		length := int(binary.BigEndian.Uint32(body[offset : offset+4]))
		end := offset + length + 12
		if string(body[offset+4:offset+8]) == "IDAT" {
			clear(body[offset+8 : end-4])
			binary.BigEndian.PutUint32(body[end-4:end], crc32.ChecksumIEEE(body[offset+4:end-4]))
			found = true
			break
		}
		offset = end
	}
	if !found {
		t.Fatal("control contains no image data")
	}
	if _, _, err := image.DecodeConfig(bytes.NewReader(body)); err != nil {
		t.Fatalf("corruption changed header admission: %v", err)
	}
	if err := validatePNG(body); err != nil {
		t.Fatalf("corruption changed framing admission: %v", err)
	}
	if _, _, err := image.Decode(bytes.NewReader(body)); err == nil {
		t.Fatal("corruption did not reach the complete decoder")
	}
	calls := 0
	service := testService(t, fetchFunc(func(context.Context, FetchRequest) (FetchResult, error) {
		calls++
		return FetchResult{Body: body, MediaType: "image/png"}, nil
	}), DefaultLimits())
	if descriptor, err := service.Seal(t.Context(), "tenant", testInput()); !errors.Is(err, ErrInvalid) || descriptor.Icon != nil || calls != 1 {
		t.Fatalf("undecodable image sealed: %#v %v, requests=%d", descriptor, err, calls)
	}
}

func TestSealingFailureDoesNotReturnPartialEvidenceOrFetchLaterScreenshots(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(map[int]string{1: "icon", 2: "first screenshot"}[failAt], func(t *testing.T) {
			body := testImage(t, "png", 2, 3)
			calls := 0
			service := testService(t, fetchFunc(func(context.Context, FetchRequest) (FetchResult, error) {
				calls++
				if calls == failAt {
					return FetchResult{}, errors.New("synthetic private upstream failure")
				}
				return FetchResult{Body: body}, nil
			}), DefaultLimits())
			input := testInput()
			input.Screenshots = []AssetInput{*input.Icon, *input.Icon}
			got, err := service.Seal(t.Context(), "tenant", input)
			if !errors.Is(err, ErrUnavailable) || got.Icon != nil || len(got.Screenshots) != 0 || calls != failAt {
				t.Fatalf("partial validation escaped or continued: %#v %v, requests=%d", got, err, calls)
			}
		})
	}
}

func TestRasterDiscardsSuccessfulFetchAfterCancellation(t *testing.T) {
	body := testImage(t, "png", 2, 3)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	service := testService(t, fetchFunc(func(context.Context, FetchRequest) (FetchResult, error) {
		calls++
		cancel()
		return FetchResult{Body: body}, nil
	}), DefaultLimits())
	if got, err := service.Seal(ctx, "tenant", testInput()); !errors.Is(err, context.Canceled) || got.Icon != nil || calls != 1 {
		t.Fatalf("late fetch response escaped cancellation: %#v %v, requests=%d", got, err, calls)
	}
}
