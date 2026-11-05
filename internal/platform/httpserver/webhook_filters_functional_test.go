//go:build functional

package httpserver

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/webhook"
)

func TestHTTPWebhookFiltersAndCursorBinding(t *testing.T) {
	server := newWebhookHTTPServer(t)
	defer server.Close()
	endpoints := make([]webhook.Endpoint, 0, 4)
	for i, name := range []string{"Alpha", "Exact", "Exact", "Zulu"} {
		r := requestJSON(t, server.URL, http.MethodPost, "/api/v1/webhooks", "admin", fmt.Sprintf("filter-create-%d", i), "", map[string]any{"name": name, "url": fmt.Sprintf("https://receiver.example/%d", i), "eventTypes": []string{"*"}})
		requireStatus(t, r, 201)
		var created webhook.CreateResult
		decodeResult(t, r, &created)
		endpoints = append(endpoints, created.Endpoint)
	}
	base := "/api/v1/webhooks?name=Exact&status=active&limit=1"
	r := requestJSON(t, server.URL, http.MethodGet, base, "admin", "", "", nil)
	requireStatus(t, r, 200)
	requireControlSchema(t, "WebhookPage", r.body)
	var page cursorPage[webhook.Endpoint]
	decodeResult(t, r, &page)
	if len(page.Items) != 1 || page.Items[0].Name != "Exact" || page.NextCursor == "" {
		t.Fatal("filtered first page lost match or continuation")
	}
	firstID := page.Items[0].ID
	cursor := "&cursor=" + url.QueryEscape(page.NextCursor)
	r = requestJSON(t, server.URL, http.MethodGet, base+cursor, "admin", "", "", nil)
	requireStatus(t, r, 200)
	page = cursorPage[webhook.Endpoint]{}
	decodeResult(t, r, &page)
	if len(page.Items) != 1 || page.Items[0].ID == firstID || page.Items[0].Name != "Exact" || page.NextCursor != "" {
		t.Fatal("duplicate-name continuation failed")
	}
	for _, target := range []string{"/api/v1/webhooks?name=exact&status=active&limit=1" + cursor, "/api/v1/webhooks?name=Exact&status=disabled&limit=1" + cursor} {
		requireStatus(t, requestJSON(t, server.URL, http.MethodGet, target, "admin", "", "", nil), 400)
	}
	requireStatus(t, requestJSON(t, server.URL, http.MethodGet, base+cursor, "other-admin", "", "", nil), 400)
	for _, q := range []string{"name=", "status=", "name=%20", "status=ACTIVE", "name=Exact&name=Exact", "unexpected=true"} {
		requireStatus(t, requestJSON(t, server.URL, http.MethodGet, "/api/v1/webhooks?"+q, "admin", "", "", nil), 400)
	}
	requireStatus(t, requestJSON(t, server.URL, http.MethodGet, base, "reader", "", "", nil), 403)
	requireStatus(t, requestJSON(t, server.URL, http.MethodDelete, "/api/v1/webhooks/"+endpoints[2].ID, "admin", "filter-disable", `"rev-1"`, nil), 204)
	for _, query := range []string{"status=disabled", "name=Exact&status=disabled"} {
		r = requestJSON(t, server.URL, http.MethodGet, "/api/v1/webhooks?limit=1&"+query, "admin", "", "", nil)
		requireStatus(t, r, 200)
		var disabled cursorPage[webhook.Endpoint]
		decodeResult(t, r, &disabled)
		if len(disabled.Items) != 1 || disabled.Items[0].ID != endpoints[2].ID || disabled.NextCursor != "" {
			t.Fatal("disabled endpoint filter did not select before the limit")
		}
	}
	path := "/api/v1/webhooks/" + endpoints[1].ID
	deliveries := make([]webhook.Delivery, 0, 3)
	for i := range 3 {
		r = requestJSON(t, server.URL, http.MethodPost, path+":test", "admin", fmt.Sprintf("filter-test-%d", i), `"rev-1"`, map[string]any{})
		requireStatus(t, r, 200)
		var d webhook.Delivery
		decodeResult(t, r, &d)
		deliveries = append(deliveries, d)
	}
	old := deliveries[0]
	for _, query := range []string{"deliveryId=" + old.ID, "eventId=" + old.EventID, "deliveryId=" + old.ID + "&eventId=" + old.EventID + "&state=succeeded", "deliveryId=" + strings.ToUpper(old.ID), "eventId=" + strings.ToUpper(old.EventID)} {
		r = requestJSON(t, server.URL, http.MethodGet, path+"/deliveries?limit=1&"+query, "admin", "", "", nil)
		requireStatus(t, r, 200)
		requireControlSchema(t, "WebhookDeliveryPage", r.body)
		var d cursorPage[webhook.Delivery]
		decodeResult(t, r, &d)
		if len(d.Items) != 1 || d.Items[0].ID != old.ID || d.NextCursor != "" {
			t.Fatal("off-page exact delivery missing")
		}
	}
	r = requestJSON(t, server.URL, http.MethodGet, path+"/deliveries?limit=1&state=succeeded", "admin", "", "", nil)
	requireStatus(t, r, 200)
	var dp cursorPage[webhook.Delivery]
	decodeResult(t, r, &dp)
	if dp.NextCursor == "" {
		t.Fatal("delivery continuation missing")
	}
	requireStatus(t, requestJSON(t, server.URL, http.MethodGet, "/api/v1/webhooks/"+endpoints[0].ID+"/deliveries?limit=1&state=succeeded&cursor="+url.QueryEscape(dp.NextCursor), "admin", "", "", nil), 400)
	for _, query := range []string{"state=waiting", "state=succeeded&eventId=" + old.EventID, "state=succeeded&deliveryId=" + old.ID} {
		requireStatus(t, requestJSON(t, server.URL, http.MethodGet, path+"/deliveries?limit=1&"+query+"&cursor="+url.QueryEscape(dp.NextCursor), "admin", "", "", nil), 400)
	}
	for _, q := range []string{"deliveryId=", "deliveryId=bad", "eventId=bad", "state=failed", "state=", "state=succeeded&state=succeeded"} {
		requireStatus(t, requestJSON(t, server.URL, http.MethodGet, path+"/deliveries?"+q, "admin", "", "", nil), 400)
	}
	requireStatus(t, requestJSON(t, server.URL, http.MethodGet, path+"/deliveries?eventId="+old.EventID, "other-admin", "", "", nil), 404)
	r = requestJSON(t, server.URL, http.MethodGet, "/api/v1/webhooks/"+endpoints[0].ID+"/deliveries?deliveryId="+old.ID, "admin", "", "", nil)
	requireStatus(t, r, 200)
	dp = cursorPage[webhook.Delivery]{}
	decodeResult(t, r, &dp)
	if len(dp.Items) != 0 {
		t.Fatal("filtered delivery crossed endpoint")
	}
}
