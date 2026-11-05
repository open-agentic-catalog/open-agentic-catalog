// Request-envelope probes exercise wire forms that a normal JSON client cannot emit.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/getkin/kin-openapi/openapi3"
)

// requestEnvelopes tests raw wire inputs that the normal JSON client cannot
// express. It uses only the owned listener and the existing bounded transport.
func requestEnvelopes(ctx context.Context, client *http.Client, base string, document *openapi3.T, observe func(observation, int) error) error {
	const path = "/api/v1/namespaces"
	exchange := func(method, body, media, key string, expected int) (exchange, error) {
		req, err := http.NewRequestWithContext(ctx, method, base+path+"?limit=100", nil)
		if method == http.MethodPost {
			req, err = http.NewRequestWithContext(ctx, method, base+path, bytes.NewBufferString(body))
		}
		if err != nil {
			return exchange{}, errTransport
		}
		req.Header.Set("Authorization", "Bearer local-admin")
		if media != "" {
			req.Header.Set("Content-Type", media)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		_, operation, err := matchOperation(document, method, path)
		if err != nil {
			return exchange{}, errContract
		}
		result, err := send(client, req)
		if err != nil {
			if observe(observation{operation.OperationID, 0, "not-checked"}, expected) != nil {
				return exchange{}, errContract
			}
			return exchange{}, err
		}
		state := "passed"
		if validateMethod(document, method, path, result, result.status) != nil {
			state = "failed"
		}
		if observe(observation{operation.OperationID, result.status, state}, expected) != nil || state != "passed" || result.status != expected {
			return exchange{}, errContract
		}
		return result, nil
	}
	before, err := exchange(http.MethodGet, "", "", "", 200)
	if err != nil {
		return err
	}
	const valid = `{"slug":"functional-json-envelope","displayName":"Owned raw JSON contract"}`
	for i, input := range []struct{ body, media string }{
		{valid, ""}, {valid, "text/plain"}, {"null", "application/json"}, {"[]", "application/json"},
		{`{"slug":"one","slug":"two","displayName":"Owned raw JSON contract"}`, "application/json"}, {valid + " {}", "application/json"},
	} {
		// Exact repeated rejections must not become a successful cached mutation.
		for range 2 {
			result, err := exchange(http.MethodPost, input.body, input.media, "functional-json-invalid-"+strconv.Itoa(i), 400)
			if err != nil {
				return err
			}
			var problem struct {
				Code string `json:"code"`
			}
			if json.Unmarshal(result.body, &problem) != nil || problem.Code != "INVALID_ARGUMENT" {
				return errContract
			}
		}
	}
	after, err := exchange(http.MethodGet, "", "", "", 200)
	if err != nil {
		return err
	}
	if !bytes.Equal(before.body, after.body) {
		return errContract
	}
	created, err := exchange(http.MethodPost, valid, "application/json; charset=utf-8", "functional-json-valid", 201)
	if err != nil {
		return err
	}
	replayed, err := exchange(http.MethodPost, valid, "application/json; charset=utf-8", "functional-json-valid", 201)
	if err != nil {
		return err
	}
	if !bytes.Equal(created.body, replayed.body) || created.header.Get("ETag") != replayed.header.Get("ETag") || replayed.header.Get("Idempotency-Replayed") != "true" {
		return errContract
	}
	retained, err := exchange(http.MethodGet, "", "", "", 200)
	if err != nil {
		return err
	}
	var page struct {
		Items []struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		} `json:"items"`
		NextCursor string `json:"nextCursor"`
	}
	var saved struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(retained.body, &page) != nil || json.Unmarshal(created.body, &saved) != nil || saved.ID == "" || page.NextCursor != "" {
		return errContract
	}
	count := 0
	for _, item := range page.Items {
		if item.Slug == "functional-json-envelope" {
			if item.ID != saved.ID {
				return errContract
			}
			count++
		}
	}
	if count != 1 {
		return errContract
	}
	return nil
}
