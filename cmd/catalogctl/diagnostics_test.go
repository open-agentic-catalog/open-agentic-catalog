package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
)

func TestCommandDiagnosticKeepsCodesAndRemoteContract(t *testing.T) {
	var output bytes.Buffer
	reportError(&output, catalog.Wrap(catalog.CodeDependencyUnavailable, "private message", errors.New("private cause")))
	if !strings.Contains(output.String(), "DEPENDENCY_UNAVAILABLE") || strings.Contains(output.String(), "private") {
		t.Fatalf("domain diagnostic=%q", output.String())
	}
	remote := remoteAPIError{Status: 403, Code: "FORBIDDEN", Detail: "request forbidden", RequestID: "safe-request"}
	output.Reset()
	reportError(&output, remote)
	if output.String() != remote.Error()+"\n" {
		t.Fatalf("remote diagnostic changed: %q", output.String())
	}
	output.Reset()
	reportError(&output, errors.Join(remote, safeerr.ErrPanic))
	if !strings.Contains(output.String(), "INTERNAL_PANIC") {
		t.Fatal("remote result hid panic")
	}
}
