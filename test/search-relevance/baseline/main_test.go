// Baseline command tests cover actual scorer differences and input failures.
package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestScorers(t *testing.T) {
	var out bytes.Buffer
	err := run(strings.NewReader(`{"documents":[{"id":"d","identifier":"air:agent:example.com:demo:invoice@1.0.0","name":"Invoice processing","description":"","tags":[],"representativeQueries":[]}],"queries":[{"id":"q","text":"invoice"},{"id":"substring","text":"invo"}]}`), &out)
	if err != nil {
		t.Fatal(err)
	}
	var scores map[string]map[string]map[string]int
	if err := json.Unmarshal(out.Bytes(), &scores); err != nil {
		t.Fatal(err)
	}
	if scores["control"]["q"]["d"] != 65 || scores["public"]["q"]["d"] != 100 || scores["control"]["substring"]["d"] != 0 || scores["public"]["substring"]["d"] != 100 {
		t.Fatalf("scorer drift: %v", scores)
	}
}

func TestInvalidInput(t *testing.T) {
	for _, value := range []string{`{`, `{}`, `{"unknown":true}`, `{} {}`, `{"documents":[{"id":"d"},{"id":"d"}],"queries":[{"id":"q","text":"x"}]}`, `{"documents":[{"id":"d"}],"queries":[{"id":"q","text":" "}]}`} {
		if err := run(strings.NewReader(value), &bytes.Buffer{}); err == nil {
			t.Errorf("accepted %s", value)
		}
	}
	if err := run(strings.NewReader(strings.Repeat(" ", (2<<20)+1)), &bytes.Buffer{}); err == nil {
		t.Fatal("accepted overlong input")
	}
}
