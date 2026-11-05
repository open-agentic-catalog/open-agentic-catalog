package httpserver

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
)

func TestWebhookPageFiltersPreserveNamesAndNormalizeUUIDs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		names []string
		want  map[string]string
	}{
		{"absent endpoint filters", "", []string{"name", "status"}, map[string]string{"name": "", "status": ""}},
		{"literal saved name", "name=Case+%26+Value&status=disabled", []string{"name", "status"}, map[string]string{"name": "Case & Value", "status": "disabled"}},
		{"absent delivery filters", "", []string{"deliveryId", "eventId", "state"}, map[string]string{"deliveryId": "", "eventId": "", "state": ""}},
		{"UUID letter case", "deliveryId=ABCDEFAB-CDEF-7ABC-8ABC-000000000001&eventId=ABCDEFAB-CDEF-7ABC-8ABC-000000000002&state=waiting", []string{"deliveryId", "eventId", "state"}, map[string]string{"deliveryId": "abcdefab-cdef-7abc-8abc-000000000001", "eventId": "abcdefab-cdef-7abc-8abc-000000000002", "state": "waiting"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/webhooks", nil)
			request.URL.RawQuery = tc.query
			got, err := webhookPageFilters(request, tc.names...)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("filters=%v want=%v err=%v", got, tc.want, err)
			}
		})
	}
	for _, query := range []string{"name=", "status=", "deliveryId=", "eventId=", "state=", "name=%zz"} {
		t.Run(query, func(t *testing.T) {
			request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/webhooks", nil)
			request.URL.RawQuery = query
			got, err := webhookPageFilters(request, "name", "status", "deliveryId", "eventId", "state")
			if catalog.ErrorCodeOf(err) != catalog.CodeInvalidArgument || got != nil {
				t.Fatalf("malformed filter returned data: %v, %v", got, err)
			}
		})
	}
}
