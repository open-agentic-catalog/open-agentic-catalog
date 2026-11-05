package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

const commandTestID = "01234567-89ab-cdef-0123-456789abcdef"

func TestServiceCommandValidation(t *testing.T) {
	valid := serviceCommandInput{Address: "10.96.1.1:8080", Token: "private-token", Operation: "read", Path: "/api/v1/jobs/" + commandTestID}
	if err := validateServiceCommand(valid); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*serviceCommandInput){
		func(in *serviceCommandInput) { in.Address = "127.0.0.1:8080" }, func(in *serviceCommandInput) { in.Address = "example.com:8080" },
		func(in *serviceCommandInput) { in.Address = "10.96.1.1:443" }, func(in *serviceCommandInput) { in.Token = "secret\r\nheader" },
		func(in *serviceCommandInput) { in.Path = "/api/v1/jobs/" + commandTestID + "?limit=100" }, func(in *serviceCommandInput) { in.Path = "/api/v1/access/roles" },
		func(in *serviceCommandInput) { in.Key = "unexpected" }, func(in *serviceCommandInput) { in.Operation = "invoke" },
	} {
		in := valid
		mutate(&in)
		if err := validateServiceCommand(in); err == nil || strings.Contains(err.Error(), "private-token") {
			t.Fatal("invalid input accepted or disclosed")
		}
	}
	for _, input := range []string{`{}`, `{"address":"secret","extra":true}`, `{} {}`, `null`} {
		var output bytes.Buffer
		if serviceCommand(strings.NewReader(input), &output) == nil || output.Len() != 0 {
			t.Fatal("invalid command reached execution")
		}
	}
}

func TestServiceResponseProjectionKeepsPrivateBodyOutOfResults(t *testing.T) {
	for _, etag := range []string{`"rev-1"`, `"rev-private-sentinel"`, `"rev-01"`, `"rev-0"`, `private-sentinel`} {
		response := &http.Response{StatusCode: http.StatusForbidden, Header: http.Header{"Etag": {etag}}, Body: io.NopCloser(strings.NewReader(`{"code":"FORBIDDEN","detail":"private-sentinel"}`))}
		got := projectServiceResponse(response)
		if etag == `"rev-1"` {
			if got.Status != 403 || got.Code != "FORBIDDEN" || len(got.Digest) != 64 {
				t.Fatalf("unexpected valid result: %+v", got)
			}
		} else if got.Outcome != "invalid_response" {
			t.Fatal("unbounded ETag accepted")
		}
		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "private-sentinel") {
			t.Fatal("private response leaked")
		}
	}
	response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", (1<<20)+1)))}
	if projectServiceResponse(response).Outcome != "invalid_response" {
		t.Fatal("over-limit body accepted")
	}
}
