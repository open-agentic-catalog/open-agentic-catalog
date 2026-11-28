//go:build functional

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

const scannerDiagnosticMarker = "private-scanner-fixture-diagnostic"

// This independent executable double exercises the actual Go/Python process
// boundary. It does not import MCP Scanner or claim native YARA qualification.
const scannerDouble = `import argparse,hashlib,json,os,pathlib,sys,time
here=pathlib.Path(__file__).resolve().parent
identity=json.loads((here/"identity.json").read_text())
mode=(here/"mode").read_text().strip()
assert not any(name in os.environ for name in ("OPENAI_API_KEY","CATALOG_TEST_DATABASE_DSN","PGPASSWORD","HTTP_PROXY"))
def overflow_stderr():
    sys.stderr.buffer.write(b"` + scannerDiagnosticMarker + `".ljust(65_537, b"e"))
if sys.argv[1] == "identity":
    print(json.dumps(identity), flush=True)
    if mode == "identity-stderr-overflow":
        (here / "identity-overflow-started").touch()
        overflow_stderr()
    raise SystemExit(0)
p=argparse.ArgumentParser()
for flag in ("input","output","source","captured-at"):p.add_argument("--"+flag,required=True)
a=p.parse_args(sys.argv[2:])
content=pathlib.Path(a.input).read_bytes();tools=json.loads(content)["tools"]
(here/"started").write_text("started")
if mode=="hold":time.sleep(90)
count=int((here/"calls").read_text())+1;(here/"calls").write_text(str(count))
results=[{"tool_name":t["name"],"status":"completed","analyzers":["YARA"],"analyzer_errors":[],"findings":[]} for t in tools]
status="completed" if tools else "skipped"
if "nextCursor" in json.loads(content):status="partial"
report={"schemaVersion":1,"status":status,"capture":{"inputSha256":hashlib.sha256(content).hexdigest(),"source":a.source,"capturedAt":a.captured_at,"hasNextCursor":"nextCursor" in json.loads(content),"scope":"supplied_page"},"callerContext":{},"contextSha256":hashlib.sha256(b"{}").hexdigest(),"scanner":{"name":"cisco-ai-mcp-scanner","version":identity["version"],"imageId":identity["imageId"]},"rulesSha256":identity["rulesSha256"],"configurationSha256":identity["configurationSha256"],"plannedTools":len(tools),"completedTools":len(tools),"cleanup":"passed","results":results}
if mode=="mismatch":report["capture"]["inputSha256"]="0"*64
if mode=="failed":report.update(status="failed",error="synthetic_failure")
pathlib.Path(a.output).write_text(json.dumps(report));os.chmod(a.output,0o600)
if mode == "scan-stdout-overflow":
    sys.stdout.buffer.write(b"o" * 4_194_305)
if mode == "scan-stderr-overflow":
    overflow_stderr()
raise SystemExit(0 if report["status"]=="completed" else 2)
`

func writeScannerDouble(t *testing.T, dir, mode string) string {
	t.Helper()
	for name, raw := range map[string][]byte{
		"scanner-double.py": []byte(scannerDouble),
		"identity.json":     fixtureJSON(testIdentity),
		"mode":              []byte(mode),
		"calls":             []byte("0"),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "scanner-double.py")
}

func scannerCalls(t *testing.T, dir string) int {
	t.Helper()
	raw, err := privateFile(filepath.Join(dir, "calls"), 32)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	return n
}
