// Package personal owns a private human library, not resource or search authority.
package personal

import (
	"context"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/audit"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
	"time"
)

const (
	MaxItems      = 200
	MaxLabelRunes = 120
	MaxBodyBytes  = 16 << 10
)

// Reference never contains a document, credential or network destination.
type Reference struct {
	Origin                 string       `json:"origin"`
	Kind                   catalog.Kind `json:"kind,omitempty"`
	ReleaseID              string       `json:"releaseId,omitempty"`
	VersionRevisionID      string       `json:"versionRevisionId,omitempty"`
	SourceID               string       `json:"sourceId,omitempty"`
	PresentationGeneration int64        `json:"presentationGeneration,omitempty"`
	Identifier             string       `json:"identifier,omitempty"`
	VersionPresent         *bool        `json:"versionPresent,omitempty"`
	Version                string       `json:"version,omitempty"`
}

// Criteria deliberately excludes cursors, page size and arbitrary source URLs.
type Criteria struct {
	Order       catalog.SearchOrder        `json:"order,omitempty"`
	Text        string                     `json:"text"`
	Origins     []catalog.SearchResultType `json:"origins,omitempty"`
	NamespaceID string                     `json:"namespaceId,omitempty"`
	Kinds       []catalog.Kind             `json:"kinds,omitempty"`
	Tags        []string                   `json:"tags,omitempty"`
	Categories  []string                   `json:"categories,omitempty"`
	License     string                     `json:"license,omitempty"`
	Publisher   string                     `json:"publisher,omitempty"`
	Visibility  catalog.Visibility         `json:"visibility,omitempty"`
}

func (c Criteria) Query() catalog.SearchQuery {
	return catalog.SearchQuery{Order: c.Order, Text: c.Text, Origins: c.Origins, NamespaceID: c.NamespaceID, Kinds: c.Kinds, Tags: c.Tags, Categories: c.Categories, License: c.License, Publisher: c.Publisher, Visibility: c.Visibility}
}

type Input struct {
	Type      string     `json:"type"`
	Label     string     `json:"label"`
	Reference *Reference `json:"reference,omitempty"`
	Search    *Criteria  `json:"search,omitempty"`
}
type Item struct {
	Input
	ID        string    `json:"id"`
	Revision  int64     `json:"revision"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
type ListRequest struct {
	Type, AfterID string
	// Nil omits the filter; a supplied label is matched literally and must be valid.
	Label *string
	Limit int
}
type Store interface {
	Get(context.Context, identity.Actor, string) (Item, error)
	List(context.Context, identity.Actor, ListRequest) ([]Item, error)
	Save(context.Context, identity.Actor, Item, int64, bool, audit.Record) (Item, error)
}
type Authority interface {
	Admit(context.Context, Reference) error
}
type AuditSink interface {
	AppendAuditEvent(context.Context, audit.Record) error
}
