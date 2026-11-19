// Maintenance handler units use scripted metadata without a running application.
package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/clock"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/id"
)

type maintenanceHandlerRepository struct {
	catalog.Repository
	list func(catalog.ValidationMaintenanceRequest) ([]catalog.ValidationMaintenanceItem, error)
}

func (r maintenanceHandlerRepository) ListValidationMaintenance(_ context.Context, _ string, q catalog.ValidationMaintenanceRequest) ([]catalog.ValidationMaintenanceItem, error) {
	return r.list(q)
}

func TestValidationMaintenanceHandlerBindsFiltersTimeAndPage(t *testing.T) {
	s, request := personalHandlerFixture(t, personalHandlerStore{})
	at := s.now()
	now := at
	s.now = func() time.Time { return now }
	calls := 0
	repo := maintenanceHandlerRepository{list: func(q catalog.ValidationMaintenanceRequest) ([]catalog.ValidationMaintenanceItem, error) {
		calls++
		if q.Limit != 2 || q.HorizonHours != 24 || q.State != "expired" || !q.ObservedAt.Equal(at) || q.Policy.Profile != "catalog-validation-v7" {
			t.Fatal(q)
		}
		if q.AfterID == "" {
			return []catalog.ValidationMaintenanceItem{{RevisionID: projectionItem}, {RevisionID: projectionNext}}, nil
		}
		if q.AfterID != projectionItem {
			t.Fatal(q)
		}
		return []catalog.ValidationMaintenanceItem{{RevisionID: projectionNext}}, nil
	}}
	s.catalog = projectionCatalog(t, repo, nil)
	base := "/api/v1/validation-maintenance?state=expired&limit=1&horizonHours=24"
	w := httptest.NewRecorder()
	s.handleValidationMaintenance(w, request("GET", base, ""))
	var page struct {
		Items        []catalog.ValidationMaintenanceItem `json:"items"`
		NextCursor   string                              `json:"nextCursor"`
		ObservedAt   time.Time                           `json:"observedAt"`
		HorizonHours int                                 `json:"horizonHours"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != 200 || len(page.Items) != 1 || page.NextCursor == "" || !page.ObservedAt.Equal(at) || page.HorizonHours != 24 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String(), err)
	}
	cursor := url.QueryEscape(page.NextCursor)
	now = now.Add(time.Minute)
	w = httptest.NewRecorder()
	s.handleValidationMaintenance(w, request("GET", base+"&cursor="+cursor, ""))
	if w.Code != 200 || calls != 2 {
		t.Fatal(w.Code, w.Body.String(), calls)
	}
	for _, query := range []string{"state=blocked&limit=1&horizonHours=24", "state=expired&limit=2&horizonHours=24", "state=expired&limit=1&horizonHours=48"} {
		w = httptest.NewRecorder()
		s.handleValidationMaintenance(w, request("GET", "/api/v1/validation-maintenance?"+query+"&cursor="+cursor, ""))
		requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
	}
	if calls != 2 {
		t.Fatal("substituted criteria reached the repository")
	}
	// Deployment policy changes fence the old cursor independently of access.
	changed, err := catalog.NewService(catalog.Options{Repository: repo, Clock: clock.NewFake(at), IDs: id.Generator{}, Validator: projectionValidator{}, CursorKey: make([]byte, 32), Policy: "replacement-policy"})
	if err != nil {
		t.Fatal(err)
	}
	s.catalog = changed
	w = httptest.NewRecorder()
	s.handleValidationMaintenance(w, request("GET", base+"&cursor="+cursor, ""))
	requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
	if calls != 2 {
		t.Fatal("old deployment policy reached selection")
	}
}

func TestValidationMaintenanceHandlerRefusesMalformedSignedPositions(t *testing.T) {
	s, request := personalHandlerFixture(t, personalHandlerStore{})
	s.catalog = projectionCatalog(t, maintenanceHandlerRepository{list: func(catalog.ValidationMaintenanceRequest) ([]catalog.ValidationMaintenanceItem, error) {
		t.Fatal("invalid position reached selection")
		return nil, nil
	}}, nil)
	p := s.catalog.ValidationMaintenancePolicy()
	page, err := s.parseAdminPage(request("GET", "/api/v1/validation-maintenance", ""), "validation-maintenance", fmt.Sprintf("%s:%s:%s", p.Profile, p.Version, p.ExternalDigest), "revision-id", map[string]string{"state": "attention", "horizonHours": "24"})
	if err != nil {
		t.Fatal(err)
	}
	for _, position := range [][]string{{projectionItem}, {projectionItem, "bad"}, {projectionItem, s.now().Add(time.Minute).Format(time.RFC3339Nano)}, {"not-a-uuid", s.now().Format(time.RFC3339Nano)}, {projectionItem, "0001-01-01T00:00:00Z"}, {projectionItem, s.now().Format(time.RFC3339Nano), "extra"}} {
		token, err := s.encodeAdminCursor(page, position)
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		s.handleValidationMaintenance(w, request("GET", "/api/v1/validation-maintenance?cursor="+url.QueryEscape(token), ""))
		requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
	}
}

func TestValidationMaintenanceHandlerRefusesClosedInvalidInputs(t *testing.T) {
	for _, query := range []string{"state=", "state=all", "state=expired&state=blocked", "horizonHours=", "horizonHours=0", "horizonHours=169", "horizonHours=1.5", "horizonHours=x", "horizonHours=24&horizonHours=48", "limit=0", "limit=101", "cursor=bad", "owner=forged"} {
		t.Run(query, func(t *testing.T) {
			s, request := personalHandlerFixture(t, personalHandlerStore{})
			s.catalog = projectionCatalog(t, maintenanceHandlerRepository{list: func(catalog.ValidationMaintenanceRequest) ([]catalog.ValidationMaintenanceItem, error) {
				t.Fatal("invalid input reached selection")
				return nil, nil
			}}, nil)
			w := httptest.NewRecorder()
			s.handleValidationMaintenance(w, request("GET", "/api/v1/validation-maintenance?"+query, ""))
			requireProjectionProblem(t, w, 400, catalog.CodeInvalidArgument)
		})
	}
}

func TestValidationMaintenanceHandlerFailsWithoutPartialDisclosure(t *testing.T) {
	s, request := personalHandlerFixture(t, personalHandlerStore{})
	s.catalog = projectionCatalog(t, maintenanceHandlerRepository{list: func(catalog.ValidationMaintenanceRequest) ([]catalog.ValidationMaintenanceItem, error) {
		return []catalog.ValidationMaintenanceItem{{RevisionID: "private-sentinel"}}, catalog.E(catalog.CodeDependencyUnavailable, "private integrity sentinel")
	}}, nil)
	w := httptest.NewRecorder()
	s.handleValidationMaintenance(w, request("GET", "/api/v1/validation-maintenance", ""))
	requireProjectionProblem(t, w, 503, catalog.CodeDependencyUnavailable)
}
