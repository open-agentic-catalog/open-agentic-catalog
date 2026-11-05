package artwork

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"image"
	_ "image/jpeg" // Register only the supported static raster decoders.
	_ "image/png"
	"mime"
	"strings"
)

func (s *Service) retrieve(ctx context.Context, tenant, url string, icon bool) (Delivery, int, int, error) {
	if tenant == "" {
		return Delivery{}, 0, 0, fmt.Errorf("%w: tenant required", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return Delivery{}, 0, 0, err
	}
	select {
	case s.slots <- struct{}{}:
	default:
		return Delivery{}, 0, 0, ErrBusy
	}
	defer func() { <-s.slots }()
	ctx, cancel := context.WithTimeout(ctx, s.limits.FetchTimeout)
	defer cancel()
	maximum, edge, pixels := assetLimits(s.limits, icon)
	response, err := s.fetcher.Fetch(ctx, FetchRequest{TenantID: tenant, URL: url, MaxBytes: maximum, Timeout: s.limits.FetchTimeout})
	if err != nil {
		if ctx.Err() != nil {
			return Delivery{}, 0, 0, ctx.Err()
		}
		return Delivery{}, 0, 0, ErrUnavailable
	}
	if err = ctx.Err(); err != nil {
		return Delivery{}, 0, 0, err
	}
	if len(response.Body) == 0 || int64(len(response.Body)) > maximum {
		return Delivery{}, 0, 0, fmt.Errorf("%w: encoded image size", ErrInvalid)
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(response.Body))
	if err != nil || (format != "png" && format != "jpeg") {
		return Delivery{}, 0, 0, fmt.Errorf("%w: unsupported image", ErrInvalid)
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > edge || config.Height > edge || int64(config.Width) > pixels/int64(config.Height) {
		return Delivery{}, 0, 0, fmt.Errorf("%w: image dimensions", ErrInvalid)
	}
	actualType := "image/" + format
	if response.MediaType != "" {
		declared, _, parseErr := mime.ParseMediaType(response.MediaType)
		declared = strings.ToLower(declared)
		if parseErr != nil || (declared != "application/octet-stream" && declared != actualType) {
			return Delivery{}, 0, 0, fmt.Errorf("%w: contradictory image media type", ErrInvalid)
		}
	}
	if format == "png" {
		err = validatePNG(response.Body)
	} else {
		err = validateJPEG(response.Body)
	}
	if err != nil {
		return Delivery{}, 0, 0, err
	}
	if err = ctx.Err(); err != nil {
		return Delivery{}, 0, 0, err
	}
	decoded, decodedFormat, err := image.Decode(bytes.NewReader(response.Body))
	if err != nil || decodedFormat != format || decoded.Bounds().Dx() != config.Width || decoded.Bounds().Dy() != config.Height {
		return Delivery{}, 0, 0, fmt.Errorf("%w: image decoding", ErrInvalid)
	}
	if err = ctx.Err(); err != nil {
		return Delivery{}, 0, 0, err
	}
	digest := sha256.Sum256(response.Body)
	return Delivery{Body: response.Body, MediaType: actualType, Digest: "sha256:" + hex.EncodeToString(digest[:])}, config.Width, config.Height, nil
}

// validatePNG walks complete chunks rather than searching compressed bytes for
// marker text. CRC validation includes unknown ancillary chunks; no animation
// chunk or bytes after the unique terminal IEND are accepted.
func validatePNG(body []byte) error {
	if len(body) < 8 || !bytes.Equal(body[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
		return fmt.Errorf("%w: PNG signature", ErrInvalid)
	}
	for offset := 8; offset < len(body); {
		if len(body)-offset < 12 {
			return fmt.Errorf("%w: truncated PNG chunk", ErrInvalid)
		}
		length := binary.BigEndian.Uint32(body[offset : offset+4])
		if length > 2<<20 || int64(length) > int64(len(body)-offset-12) {
			return fmt.Errorf("%w: PNG chunk length", ErrInvalid)
		}
		end := offset + 12 + int(length)
		kind := string(body[offset+4 : offset+8])
		if kind == "acTL" || kind == "fcTL" || kind == "fdAT" {
			return fmt.Errorf("%w: animated PNG", ErrInvalid)
		}
		if crc32.ChecksumIEEE(body[offset+4:end-4]) != binary.BigEndian.Uint32(body[end-4:end]) {
			return fmt.Errorf("%w: PNG chunk checksum", ErrInvalid)
		}
		if kind == "IEND" {
			if length != 0 || end != len(body) {
				return fmt.Errorf("%w: PNG trailing bytes", ErrInvalid)
			}
			return nil
		}
		offset = end
	}
	return fmt.Errorf("%w: missing PNG end", ErrInvalid)
}

// validateJPEG bounds marker framing and requires EOI to terminate the encoded
// document. Entropy-coded scans allow stuffed FF bytes and restart markers.
func validateJPEG(body []byte) error {
	if len(body) < 4 || body[0] != 0xff || body[1] != 0xd8 {
		return fmt.Errorf("%w: JPEG signature", ErrInvalid)
	}
	position := 2
	for position < len(body) {
		if body[position] != 0xff {
			return fmt.Errorf("%w: JPEG marker", ErrInvalid)
		}
		for position < len(body) && body[position] == 0xff {
			position++
		}
		if position == len(body) {
			break
		}
		marker := body[position]
		position++
		if marker == 0xd9 {
			if position != len(body) {
				return fmt.Errorf("%w: JPEG trailing bytes", ErrInvalid)
			}
			return nil
		}
		if marker == 0 || marker == 0xd8 || marker == 0x01 || marker >= 0xd0 && marker <= 0xd7 {
			return fmt.Errorf("%w: JPEG marker sequence", ErrInvalid)
		}
		if len(body)-position < 2 {
			return fmt.Errorf("%w: JPEG segment", ErrInvalid)
		}
		length := int(binary.BigEndian.Uint16(body[position : position+2]))
		if length < 2 || length > len(body)-position {
			return fmt.Errorf("%w: JPEG segment length", ErrInvalid)
		}
		position += length
		if marker == 0xda {
			for position < len(body) {
				if body[position] != 0xff {
					position++
					continue
				}
				start := position
				for position < len(body) && body[position] == 0xff {
					position++
				}
				if position == len(body) {
					break
				}
				code := body[position]
				if code == 0 || code >= 0xd0 && code <= 0xd7 {
					position++
					continue
				}
				position = start
				break
			}
		}
	}
	return fmt.Errorf("%w: missing JPEG end", ErrInvalid)
}
