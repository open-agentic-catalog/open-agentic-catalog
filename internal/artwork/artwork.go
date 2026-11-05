// Package artwork validates optional presentation metadata and bounded image
// bytes. Network admission and authorization belong to its caller.
package artwork

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	IRI           = "https://github.com/open-agentic-catalog/open-agentic-catalog/schemas/artwork/v1#descriptor"
	SchemaVersion = 1
	NotAssessed   = "not_assessed"
)

var (
	ErrInvalid     = errors.New("invalid artwork")
	ErrUnavailable = errors.New("artwork unavailable")
	ErrBusy        = errors.New("artwork capacity unavailable")
	ErrDrift       = errors.New("artwork bytes changed")
)

// Rights is an author's declaration, never a verified legal assessment.
type Rights struct {
	Basis       string `json:"basis"`
	Attribution string `json:"attribution"`
	Reference   string `json:"reference,omitempty"`
}

type AssetInput struct {
	URL     string  `json:"url"`
	Alt     string  `json:"alt"`
	Caption string  `json:"caption,omitempty"`
	Rights  *Rights `json:"rights,omitempty"`
}

type Input struct {
	SchemaVersion int          `json:"schemaVersion"`
	Icon          *AssetInput  `json:"icon,omitempty"`
	Screenshots   []AssetInput `json:"screenshots,omitempty"`
}

// Asset binds reviewed presentation to exact encoded bytes. Native display
// hints may omit the facts and rights; only mutable delivery accepts those.
type Asset struct {
	AssetInput
	Digest           string `json:"digest"`
	MediaType        string `json:"mediaType"`
	Size             int64  `json:"size"`
	Width            int    `json:"width"`
	Height           int    `json:"height"`
	RightsAssessment string `json:"rightsAssessment"`
}

type Descriptor struct {
	SchemaVersion int     `json:"schemaVersion"`
	Icon          *Asset  `json:"icon,omitempty"`
	Screenshots   []Asset `json:"screenshots,omitempty"`
}

// Limits are application work limits, not permission to contact an origin.
type Limits struct {
	MaxScreenshots      int
	MaxURLBytes         int
	MaxAltRunes         int
	MaxCaptionRunes     int
	MaxAttributionRunes int
	MaxDescriptorBytes  int
	MaxIconBytes        int64
	MaxScreenshotBytes  int64
	MaxIconEdge         int
	MaxIconPixels       int64
	MaxScreenshotEdge   int
	MaxScreenshotPixels int64
	FetchTimeout        time.Duration
	ValidationTimeout   time.Duration
	MaxConcurrent       int
}

func DefaultLimits() Limits {
	return Limits{4, 2048, 160, 256, 512, 16 << 10, 256 << 10, 1 << 20, 512, 262144, 2048, 2097152, 3 * time.Second, 8 * time.Second, 4}
}

func HardLimits() Limits {
	return Limits{8, 2048, 256, 512, 1024, 32 << 10, 1 << 20, 2 << 20, 1024, 1048576, 4096, 4194304, 5 * time.Second, 15 * time.Second, 8}
}

func (l Limits) Validate() error {
	h := HardLimits()
	values := [][2]int64{{int64(l.MaxURLBytes), int64(h.MaxURLBytes)}, {int64(l.MaxAltRunes), int64(h.MaxAltRunes)}, {int64(l.MaxCaptionRunes), int64(h.MaxCaptionRunes)}, {int64(l.MaxAttributionRunes), int64(h.MaxAttributionRunes)}, {int64(l.MaxDescriptorBytes), int64(h.MaxDescriptorBytes)}, {l.MaxIconBytes, h.MaxIconBytes}, {l.MaxScreenshotBytes, h.MaxScreenshotBytes}, {int64(l.MaxIconEdge), int64(h.MaxIconEdge)}, {l.MaxIconPixels, h.MaxIconPixels}, {int64(l.MaxScreenshotEdge), int64(h.MaxScreenshotEdge)}, {l.MaxScreenshotPixels, h.MaxScreenshotPixels}, {int64(l.FetchTimeout), int64(h.FetchTimeout)}, {int64(l.ValidationTimeout), int64(h.ValidationTimeout)}, {int64(l.MaxConcurrent), int64(h.MaxConcurrent)}}
	for _, v := range values {
		if v[0] <= 0 || v[0] > v[1] {
			return fmt.Errorf("%w: limits exceed supported bounds", ErrInvalid)
		}
	}
	if l.MaxScreenshots < 0 || l.MaxScreenshots > h.MaxScreenshots {
		return fmt.Errorf("%w: screenshot count", ErrInvalid)
	}
	return nil
}

func validURL(value string, maximum int) bool {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return false
	}
	u, err := url.ParseRequestURI(value)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == "" && u.Opaque == "" && !strings.ContainsRune(value, '#')
}

func validText(value string, maximum int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= maximum && strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) < 0
}

func validateInput(a AssetInput, l Limits, mutable bool) error {
	if !validURL(a.URL, l.MaxURLBytes) || !validText(a.Alt, l.MaxAltRunes) || !validText(a.Caption, l.MaxCaptionRunes) || (!mutable && strings.TrimSpace(a.Alt) == "") {
		return fmt.Errorf("%w: asset reference or description", ErrInvalid)
	}
	if a.Rights == nil {
		if mutable {
			return nil
		}
		return fmt.Errorf("%w: rights declaration required", ErrInvalid)
	}
	r := a.Rights
	if r.Basis != "owned" && r.Basis != "licensed" && r.Basis != "permission" {
		return fmt.Errorf("%w: rights basis", ErrInvalid)
	}
	if strings.TrimSpace(r.Attribution) == "" || !validText(r.Attribution, l.MaxAttributionRunes) || (r.Reference != "" && !validURL(r.Reference, l.MaxURLBytes)) {
		return fmt.Errorf("%w: rights attribution", ErrInvalid)
	}
	return nil
}

func ValidateInput(input Input, l Limits) error {
	if err := l.Validate(); err != nil {
		return err
	}
	if input.SchemaVersion != SchemaVersion || len(input.Screenshots) > l.MaxScreenshots {
		return fmt.Errorf("%w: descriptor version or count", ErrInvalid)
	}
	if input.Icon != nil {
		if err := validateInput(*input.Icon, l, false); err != nil {
			return err
		}
	}
	for _, a := range input.Screenshots {
		if err := validateInput(a, l, false); err != nil {
			return err
		}
	}
	return checkEncodedSize(input, l)
}

func ValidateAsset(a Asset, l Limits, icon, mutable bool) error {
	if err := l.Validate(); err != nil {
		return err
	}
	if err := validateInput(a.AssetInput, l, mutable); err != nil {
		return err
	}
	if mutable {
		return nil
	}
	maxBytes, maxEdge, maxPixels := assetLimits(l, icon)
	if len(a.Digest) != 71 || !strings.HasPrefix(a.Digest, "sha256:") {
		return fmt.Errorf("%w: digest", ErrInvalid)
	}
	for _, c := range a.Digest[7:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return fmt.Errorf("%w: digest", ErrInvalid)
		}
	}
	if (a.MediaType != "image/png" && a.MediaType != "image/jpeg") || a.Size <= 0 || a.Size > maxBytes || a.Width <= 0 || a.Height <= 0 || a.Width > maxEdge || a.Height > maxEdge || int64(a.Width) > maxPixels/int64(a.Height) || a.RightsAssessment != NotAssessed {
		return fmt.Errorf("%w: retained image facts", ErrInvalid)
	}
	return nil
}

func ValidateDescriptor(d Descriptor, l Limits) error {
	if err := l.Validate(); err != nil {
		return err
	}
	if d.SchemaVersion != SchemaVersion || len(d.Screenshots) > l.MaxScreenshots {
		return fmt.Errorf("%w: descriptor version or count", ErrInvalid)
	}
	if d.Icon != nil {
		if err := ValidateAsset(*d.Icon, l, true, false); err != nil {
			return err
		}
	}
	for _, a := range d.Screenshots {
		if err := ValidateAsset(a, l, false, false); err != nil {
			return err
		}
	}
	return checkEncodedSize(d, l)
}

func assetLimits(l Limits, icon bool) (int64, int, int64) {
	if icon {
		return l.MaxIconBytes, l.MaxIconEdge, l.MaxIconPixels
	}
	return l.MaxScreenshotBytes, l.MaxScreenshotEdge, l.MaxScreenshotPixels
}

type FetchRequest struct {
	TenantID string
	URL      string
	MaxBytes int64
	Timeout  time.Duration
}
type FetchResult struct {
	Body      []byte
	MediaType string
}
type Fetcher interface {
	Fetch(context.Context, FetchRequest) (FetchResult, error)
}
type Delivery struct {
	Body      []byte
	MediaType string
	Digest    string
}

type Service struct {
	fetcher Fetcher
	limits  Limits
	slots   chan struct{}
}

// Limits returns an immutable value copy of this service's work policy.
func (s *Service) Limits() Limits { return s.limits }

func New(fetcher Fetcher, limits Limits) (*Service, error) {
	if fetcher == nil {
		return nil, fmt.Errorf("%w: fetcher required", ErrInvalid)
	}
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	return &Service{fetcher: fetcher, limits: limits, slots: make(chan struct{}, limits.MaxConcurrent)}, nil
}

// Seal validates explicit authoring input without retaining or modifying bytes.
// It does no database work; callers commit the returned facts atomically.
func (s *Service) Seal(ctx context.Context, tenant string, input Input) (Descriptor, error) {
	if err := ValidateInput(input, s.limits); err != nil {
		return Descriptor{}, err
	}
	deadline := time.Now().Add(s.limits.ValidationTimeout)
	if outer, ok := ctx.Deadline(); ok && outer.Add(-250*time.Millisecond).Before(deadline) {
		deadline = outer.Add(-250 * time.Millisecond)
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Descriptor{}, err
	}
	d := Descriptor{SchemaVersion: SchemaVersion}
	if input.Icon != nil {
		a, err := s.sealAsset(ctx, tenant, *input.Icon, true)
		if err != nil {
			return Descriptor{}, err
		}
		d.Icon = &a
	}
	for _, inputAsset := range input.Screenshots {
		a, err := s.sealAsset(ctx, tenant, inputAsset, false)
		if err != nil {
			return Descriptor{}, err
		}
		d.Screenshots = append(d.Screenshots, a)
	}
	if err := ValidateDescriptor(d, s.limits); err != nil {
		return Descriptor{}, err
	}
	return d, nil
}

func (s *Service) sealAsset(ctx context.Context, tenant string, input AssetInput, icon bool) (Asset, error) {
	delivery, width, height, err := s.retrieve(ctx, tenant, input.URL, icon)
	if err != nil {
		return Asset{}, err
	}
	// Copy nested declarations so a caller cannot change retained facts by
	// subsequently modifying its submitted input.
	rights := *input.Rights
	input.Rights = &rights
	return Asset{AssetInput: input, Digest: delivery.Digest, MediaType: delivery.MediaType, Size: int64(len(delivery.Body)), Width: width, Height: height, RightsAssessment: NotAssessed}, nil
}

// Deliver checks current fetched bytes against sealed facts, or validates a
// mutable native display hint. The caller admits its tenant and target first.
func (s *Service) Deliver(ctx context.Context, tenant string, a Asset, icon, mutable bool) (Delivery, error) {
	if err := ValidateAsset(a, s.limits, icon, mutable); err != nil {
		return Delivery{}, err
	}
	d, w, h, err := s.retrieve(ctx, tenant, a.URL, icon)
	if err != nil {
		return Delivery{}, err
	}
	if !mutable && (d.Digest != a.Digest || d.MediaType != a.MediaType || int64(len(d.Body)) != a.Size || w != a.Width || h != a.Height) {
		return Delivery{}, ErrDrift
	}
	return d, nil
}
