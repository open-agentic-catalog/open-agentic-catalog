// Personal file fuzzing exercises the strict private codec without I/O or service dependencies.
package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/personal"
)

func FuzzPersonalLibraryFile(f *testing.F) {
	raw, err := json.Marshal(personalTestFile())
	if err != nil {
		f.Fatal(err)
	}
	f.Add(raw)
	f.Add([]byte(`{"format":"oac-personal-library-v1","origin":"https://catalog.example.test","tenantId":"00000000-0000-7000-8000-000000000001","consistency":"observed-pages","items":[{"type":"search","label":"Empty optional filters","search":{"text":"","kinds":[],"tags":[],"categories":[]}}]}`))
	f.Add([]byte(`{"format":"oac-personal-library-v1","origin":"https://catalog.example.test","tenantId":"00000000-0000-7000-8000-000000000001","consistency":"observed-pages","items":[{"type":"search","label":"Empty filters","search":{"text":"","origins":[],"kinds":[],"tags":[],"categories":[]}}]}`))
	f.Add([]byte(`{"format":"oac-personal-library-v1","items":null}`))
	f.Add([]byte(`{"format":"one","format":"two"}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		file, err := decodePersonalFile(raw)
		if err != nil {
			return
		}
		if file.Format != personalFileFormat || len(file.Items) > personal.MaxItems || len(raw) > personalFileLimit {
			t.Fatal("accepted an unbounded or unsupported file")
		}
		for _, item := range file.Items {
			if personal.Validate(item) != nil {
				t.Fatal("codec admitted an invalid save")
			}
		}
		encoded, err := json.Marshal(file)
		if err != nil {
			t.Fatal(err)
		}
		again, err := decodePersonalFile(encoded)
		if err != nil {
			t.Fatal("serialized valid file was refused", err)
		}
		// Optional empty filters are omitted by save requests; their exact wire input must remain stable.
		reencoded, err := json.Marshal(again)
		if err != nil || !bytes.Equal(encoded, reencoded) {
			t.Fatal("accepted intent changed on serialization", err)
		}
	})
}
