// Unit tests exercise isolated validation and controlled dependencies.
package clientcompat_test

import (
	"encoding/json"
	"testing"
)

const tenant = "00000000-0000-7000-8000-000000000001"

const otherTenant = "00000000-0000-7000-8000-000000000002"

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func decode[T any](t *testing.T, data []byte) T {
	t.Helper()
	var value T
	must(t, json.Unmarshal(data, &value))
	return value
}

func ptr[T any](value T) *T { return &value }
