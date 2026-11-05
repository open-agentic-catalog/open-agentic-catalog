// Package events owns the small public notification contract used by optional
// webhook delivery. It deliberately exposes references, not domain documents.
package events

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/open-agentic-catalog/open-agentic-catalog/api"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
)

const (
	DataSchema = "https://github.com/open-agentic-catalog/open-agentic-catalog/schemas/event/v1"
	typePrefix = "com.open.agentic.catalog."
	typeSuffix = ".v1"
)

var publicActions = []string{
	"catalog.listing.created",
	"catalog.listing.revised",
	"identity.membership.offboarded",
	"identity.membership.provisioned",
	"identity.membership.reactivated",
	"identity.membership.suspended",
	"identity.realm.reactivated",
	"identity.realm.suspended",
	"identity.role.assignment.granted",
	"identity.role.assignment.revoked",
	"listing.release.deprecated",
	"listing.release.published",
	"listing.release.retired",
	"listing.release.revoked",
	"listing.release.superseded",
	"namespace.created",
	"namespace.updated",
	"observation.adopted",
	"publication.activated",
	"publication.unpublished",
	"publisher.created",
	"publisher.publication.domain.approved",
	"publisher.publication.domain.revoked",
	"publisher.updated",
	"resource.created",
	"resource.deleted",
	"resource.version.created",
	"resource.version.revised",
	"review.decision.recorded",
	"review.submission.created",
	"source.connected",
	"source.paused",
	"source.removed",
	"source.resumed",
	"source.revision.activated",
	"source.sync.completed",
	"source.sync.failed",
	"version.revision.validated",
	"webhook.test",
}

var (
	publicTypes = makePublicTypes()
	digestRE    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	uuidRE      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	schemaOnce  sync.Once
	eventSchema *jsonschema.Schema
	errSchema   error
)

// NotificationReference is the only public webhook data payload. Consumers
// use the authenticated control API to read an authorized current object.
type NotificationReference struct {
	Action     string `json:"action"`
	TargetType string `json:"targetType"`
	TargetID   string `json:"targetId"`
	Digest     string `json:"digest,omitempty"`
	Host       string `json:"host,omitempty"`
}

type CloudEvent struct {
	SpecVersion     string                `json:"specversion"`
	ID              string                `json:"id"`
	Source          string                `json:"source"`
	Type            string                `json:"type"`
	Subject         string                `json:"subject"`
	Time            string                `json:"time"`
	DataContentType string                `json:"datacontenttype"`
	DataSchema      string                `json:"dataschema"`
	Data            NotificationReference `json:"data"`
}

// makePublicTypes builds the fixed canonical event-type map from reviewed
// domain actions.
func makePublicTypes() map[string]string {
	result := make(map[string]string, len(publicActions))
	for _, action := range publicActions {
		result[typePrefix+action+typeSuffix] = action
	}
	return result
}

// CanonicalType maps internal action spelling to the stable public type.
func CanonicalType(action string) string {
	action = strings.ToLower(strings.TrimSpace(action))
	action = strings.TrimPrefix(action, typePrefix)
	action = strings.TrimSuffix(action, typeSuffix)
	switch action {
	case "review_submission.submitted":
		action = "review.submission.created"
	case "review_submission.approve", "review_submission.request_changes", "review_submission.reject":
		action = "review.decision.recorded"
	default:
		action = strings.ReplaceAll(action, "_", ".")
	}
	return typePrefix + action + typeSuffix
}

// IsPublicType reports whether eventType is registered in the public
// notification vocabulary.
func IsPublicType(eventType string) bool {
	_, ok := publicTypes[eventType]
	return ok
}

// TenantSource returns the tenant-scoped CloudEvents source identifier.
func TenantSource(tenantID string) string {
	return "https://github.com/open-agentic-catalog/open-agentic-catalog/tenants/" + strings.ToLower(tenantID)
}

// Reference creates the public projection for a domain event. The digest is
// optional and is omitted if the caller does not have a canonical digest.
func Reference(action, targetType, targetID, digest string) (NotificationReference, error) {
	return ReferenceWithHost(action, targetType, targetID, digest, "")
}

// ReferenceWithHost creates a reference for host-bound publication events.
// Non-publication events reject a host so the common payload stays minimal.
func ReferenceWithHost(action, targetType, targetID, digest, host string) (NotificationReference, error) {
	eventType := CanonicalType(action)
	publicAction, ok := publicTypes[eventType]
	if !ok {
		return NotificationReference{}, fmt.Errorf("event action %q is not in the public registry", action)
	}
	targetType = strings.TrimSpace(targetType)
	targetID = strings.TrimSpace(targetID)
	host = strings.TrimSpace(host)
	publicationAction := strings.HasPrefix(publicAction, "publication.")
	if targetType != targetTypeForAction(publicAction) || !uuidRE.MatchString(targetID) || digest != "" && !digestRE.MatchString(digest) || publicationAction && !validHost(host) || !publicationAction && host != "" {
		return NotificationReference{}, errors.New("event reference is invalid")
	}
	return NotificationReference{Action: publicAction, TargetType: targetType, TargetID: targetID, Digest: digest, Host: host}, nil
}

// MarshalReference returns a closed JSON data object suitable for outbox
// persistence.
func MarshalReference(action, targetType, targetID, digest string) ([]byte, error) {
	reference, err := Reference(action, targetType, targetID, digest)
	if err != nil {
		return nil, err
	}
	return json.Marshal(reference)
}

// MarshalReferenceWithHost encodes one host-bound publication reference.
func MarshalReferenceWithHost(action, targetType, targetID, digest, host string) ([]byte, error) {
	reference, err := ReferenceWithHost(action, targetType, targetID, digest, host)
	if err != nil {
		return nil, err
	}
	return json.Marshal(reference)
}

// ValidateReferenceData proves that an outbox data object is the exact closed
// reference expected for its independently classified public event. Object
// member order and insignificant JSON whitespace are deliberately irrelevant
// because PostgreSQL stores the value as jsonb.
func ValidateReferenceData(raw []byte, action, targetType, targetID, digest, host string) error {
	if err := jsondoc.Validate(raw); err != nil {
		return fmt.Errorf("event reference data is invalid: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var actual NotificationReference
	if err := decoder.Decode(&actual); err != nil {
		return fmt.Errorf("decode event reference data: %w", err)
	}
	expected, err := ReferenceWithHost(action, targetType, targetID, digest, host)
	if err != nil {
		return err
	}
	if actual != expected {
		return errors.New("event reference data does not match its audited transition")
	}
	return nil
}

// MarshalCloudEvent validates the persisted closed reference before projection.
// Incomplete, obsolete or contradictory internal state is never reconstructed.
func MarshalCloudEvent(id, tenantID, eventType, subject string, createdAt time.Time, rawData []byte) ([]byte, error) {
	action, ok := publicTypes[eventType]
	if !ok {
		return nil, errors.New("outbox event type is not public")
	}
	var reference NotificationReference
	if err := jsondoc.Validate(rawData); err != nil {
		return nil, fmt.Errorf("validate outbox event data: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(rawData))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reference); err != nil {
		return nil, fmt.Errorf("decode outbox event data: %w", err)
	}
	if err := ValidateReferenceData(rawData, action, targetTypeForAction(action), subject, reference.Digest, reference.Host); err != nil {
		return nil, err
	}
	event := CloudEvent{SpecVersion: "1.0", ID: id, Source: TenantSource(tenantID), Type: eventType, Subject: subject, Time: createdAt.UTC().Format(time.RFC3339Nano), DataContentType: "application/json", DataSchema: DataSchema, Data: reference}
	raw, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	if err := Validate(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// Validate checks the closed CloudEvents envelope and reference payload against
// the embedded schema and semantic event rules.
func Validate(raw []byte) error {
	schemaOnce.Do(func() {
		compiler := jsonschema.NewCompiler()
		compiler.AssertFormat()
		schemaRaw, err := api.Files.ReadFile("schemas/catalog-event-v1.schema.json")
		if err != nil {
			errSchema = fmt.Errorf("read embedded event schema: %w", err)
			return
		}
		document, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaRaw))
		if err != nil {
			errSchema = fmt.Errorf("decode embedded event schema: %w", err)
			return
		}
		if err := compiler.AddResource(DataSchema, document); err != nil {
			errSchema = fmt.Errorf("register embedded event schema: %w", err)
			return
		}
		eventSchema, errSchema = compiler.Compile(DataSchema)
	})
	if errSchema != nil {
		return errSchema
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("decode event: %w", err)
	}
	if err := eventSchema.Validate(instance); err != nil {
		return fmt.Errorf("validate event: %w", err)
	}
	var event CloudEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return fmt.Errorf("decode typed event: %w", err)
	}
	action, ok := publicTypes[event.Type]
	if !ok || action != event.Data.Action || event.Subject != event.Data.TargetID {
		return errors.New("event reference does not match its envelope")
	}
	if _, err := ReferenceWithHost(event.Data.Action, event.Data.TargetType, event.Data.TargetID, event.Data.Digest, event.Data.Host); err != nil {
		return fmt.Errorf("validate event reference: %w", err)
	}
	return nil
}

// targetTypeForAction maps a public action to the reference target vocabulary.
func targetTypeForAction(action string) string {
	switch {
	case strings.HasPrefix(action, "namespace."):
		return "namespace"
	case strings.HasPrefix(action, "publisher."):
		return "publisher"
	case strings.HasPrefix(action, "resource.version."):
		return "resource_version"
	case strings.HasPrefix(action, "resource."), action == "observation.adopted":
		return "resource"
	case strings.HasPrefix(action, "version.revision."):
		return "version_revision"
	case strings.HasPrefix(action, "catalog.listing."):
		return "catalog_listing"
	case strings.HasPrefix(action, "review."):
		return "review_submission"
	case strings.HasPrefix(action, "listing.release."):
		return "listing_release"
	case action == "source.sync.completed", action == "source.sync.failed":
		return "source_snapshot"
	case strings.HasPrefix(action, "source."):
		return "federation_source"
	case strings.HasPrefix(action, "publication."):
		return "publication_snapshot"
	case strings.HasPrefix(action, "identity.membership."):
		return "tenant_membership"
	case strings.HasPrefix(action, "identity.role.assignment."):
		return "role_assignment"
	case strings.HasPrefix(action, "identity.realm."):
		return "tenant_identity_realm"
	case action == "webhook.test":
		return "webhook_endpoint"
	default:
		return ""
	}
}

// validHost reports whether a publication host satisfies the event contract's
// bounded hostname syntax.
func validHost(host string) bool {
	if host == "" || len(host) > 253 || host != strings.ToLower(host) || strings.HasSuffix(host, ".") {
		return false
	}
	if address := net.ParseIP(host); address != nil {
		return host == address.String()
	}
	if strings.IndexFunc(host, func(character rune) bool { return character != '.' && (character < '0' || character > '9') }) == -1 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if character < 'a' || character > 'z' {
				if character < '0' || character > '9' {
					if character != '-' {
						return false
					}
				}
			}
		}
	}
	return true
}
