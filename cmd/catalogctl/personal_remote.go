// Personal remote reads stay bound to one explicit origin and authenticated human.
package main

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"unicode/utf8"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jsondoc"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/personal"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

// personalPrincipal checks current human authority, not role names or file-supplied owners.
func personalPrincipal(ctx context.Context, session *remoteSession, options remoteOptions, write bool) (importPrincipal, error) {
	var self struct {
		importPrincipal
		ActorType        string   `json:"actorType"`
		Permissions      []string `json:"permissions"`
		ScopeEnforcement *bool    `json:"scopeEnforcement"`
		Scopes           []string `json:"scopes"`
	}
	response, err := importGET(ctx, session, options, "/api/v1/me")
	if err != nil {
		return importPrincipal{}, err
	}
	scope := "catalog.read"
	if write {
		scope = "personal.write"
	}
	if importResponse(response.Data, &self) != nil || self.ActorType != "human" || !validImportID(self.TenantID) || !validImportID(self.RealmID) || !validImportID(self.MembershipID) || self.Subject == "" || len(self.Subject) > 512 || !utf8.ValidString(self.Subject) || self.ScopeEnforcement == nil || !slices.Contains(self.Permissions, "library.own") || *self.ScopeEnforcement && !slices.Contains(self.Scopes, scope) {
		return importPrincipal{}, safeerr.New("personal: requires a current human membership, library.own and the applicable catalog.read/personal.write scope")
	}
	return self.importPrincipal, nil
}

// exportPersonal enumerates at most two pages; failure never returns a completed prefix.
func exportPersonal(ctx context.Context, options remoteOptions, kind string) (personalFile, error) {
	session, err := newRemoteSession(ctx, options)
	if err != nil {
		return personalFile{}, err
	}
	defer session.close()
	return readPersonalExport(ctx, session, options, kind)
}

// readPersonalExport keeps enumeration and its failure rules independent of credential loading.
func readPersonalExport(ctx context.Context, session *remoteSession, options remoteOptions, kind string) (personalFile, error) {
	file := personalFile{Format: personalFileFormat, Origin: session.origin, Consistency: "observed-pages", Items: []personal.Input{}}
	var principal importPrincipal
	cursor, lastID := "", ""
	seen := map[string]bool{}
	for pageIndex := 0; pageIndex < 2; pageIndex++ {
		current, err := personalPrincipal(ctx, session, options, false)
		if err != nil {
			return personalFile{}, err
		}
		if pageIndex == 0 {
			principal, file.TenantID = current, current.TenantID
		} else if principal != current {
			return personalFile{}, safeerr.New("personal: identity changed during export; no completed file is available")
		}
		query := url.Values{"limit": {"100"}}
		if kind != "" {
			query.Set("type", kind)
		}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		response, err := importGET(ctx, session, options, "/api/v1/personal/items?"+query.Encode())
		if err != nil {
			return personalFile{}, err
		}
		var page struct {
			Items      []personal.Item `json:"items"`
			NextCursor string          `json:"nextCursor,omitempty"`
		}
		if jsondoc.Decode(response.Data, &page) != nil || page.Items == nil || len(page.Items) > 100 || len(page.NextCursor) > 2048 || page.NextCursor != "" && (page.NextCursor == cursor || len(page.Items) == 0) {
			return personalFile{}, safeerr.New("personal: invalid library page; no completed file is available")
		}
		for _, item := range page.Items {
			if !validImportID(item.ID) || seen[item.ID] || lastID != "" && item.ID <= lastID || item.Revision < 1 || item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() || kind != "" && item.Type != kind {
				return personalFile{}, safeerr.New("personal: invalid or repeated saved item; no completed file is available")
			}
			lastID, seen[item.ID] = item.ID, true
			file.Items = append(file.Items, item.Input)
		}
		raw, err := json.Marshal(file)
		if err != nil {
			return personalFile{}, safeerr.New("personal: cannot encode observed library")
		}
		if _, err = decodePersonalFile(raw); err != nil {
			return personalFile{}, err
		}
		if page.NextCursor == "" {
			return file, nil
		}
		cursor = page.NextCursor
	}
	return personalFile{}, safeerr.New("personal: export exceeded its two-page budget; start a fresh export rather than using a partial file")
}
