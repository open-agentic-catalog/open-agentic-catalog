//go:build integration

package functional_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

// pgbouncerHTTPReadBenchmark reuses the maintained workload client and retained
// smoke records. It is a short closed-loop functional diagnostic, not a timing
// gate or production capacity qualification. One additional direct API process
// supplies the baseline inside the same disposable database fixture.
func (f processFixture) pgbouncerHTTPReadBenchmark(ctx context.Context, root string, cfg config.Config, directDSN string) {
	f.t.Helper()
	binary := filepath.Join(f.directory, "performance")
	build := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-trimpath", "-o", binary, "./test/performance") // #nosec G204,G702 -- fixed source and invocation-owned output.
	build.Dir = root
	if err := build.Run(); err != nil {
		f.t.Fatal("build maintained performance client")
	}
	direct := cfg
	direct.HTTP.Address = processAddress(ctx, f.t)
	direct.HTTP.PublicBaseURL = "http://" + direct.HTTP.Address
	stopDirect := f.start(ctx, "api", directDSN, f.configuration("benchmark-direct", direct), direct.HTTP.Address)
	defer stopDirect()
	expectedDigest := ""
	for trial := 1; trial <= 3; trial++ {
		routes := []string{"direct", "pooled"}
		if trial%2 == 0 {
			routes[0], routes[1] = routes[1], routes[0]
		}
		for _, route := range routes {
			address := cfg.HTTP.Address
			if route == "direct" {
				address = direct.HTTP.Address
			}
			_, rawPort, err := net.SplitHostPort(address)
			if err != nil {
				f.t.Fatal("invalid owned benchmark address")
			}
			port, err := strconv.Atoi(rawPort)
			if err != nil {
				f.t.Fatal("invalid owned benchmark port")
			}
			profile, err := json.Marshal(map[string]any{"scenario": "local", "port": port, "warmupSeconds": 1, "seconds": 3, "workers": 2, "maxRequests": 100000})
			if err != nil {
				f.t.Fatal("encode fixed benchmark profile")
			}
			path := filepath.Join(f.directory, "benchmark-profile.json")
			if err := os.WriteFile(path, profile, 0600); err != nil {
				f.t.Fatal("write private benchmark profile")
			}
			command := exec.CommandContext(ctx, binary, path) // #nosec G204,G702 -- invocation-owned binary and fixed local workload profile.
			command.Env = []string{}
			raw := f.run(ctx, command, 0)
			var report struct {
				SchemaVersion   int    `json:"schemaVersion"`
				Verdict         string `json:"verdict"`
				ResponseDigest  string `json:"responseDigest"`
				BaselineResults int    `json:"baselineResults"`
				Warmup          struct {
					Complete bool `json:"complete"`
					Failed   int  `json:"failed"`
				} `json:"warmup"`
				Measured struct {
					Complete bool `json:"complete"`
					Count    int  `json:"count"`
					Failed   int  `json:"failed"`
				} `json:"measured"`
			}
			decodeProcessJSON(f.t, raw, &report)
			// Keep the safe native report even if its contract assertion fails.
			f.t.Logf("matched HTTP benchmark route=%s trial=%d report=%s", route, trial, raw)
			if expectedDigest == "" {
				expectedDigest = report.ResponseDigest
			}
			if report.SchemaVersion != 3 || report.Verdict != "passed" || report.BaselineResults != 3 || report.ResponseDigest == "" || report.ResponseDigest != expectedDigest || !report.Warmup.Complete || report.Warmup.Failed != 0 || !report.Measured.Complete || report.Measured.Count < 1 || report.Measured.Failed != 0 {
				f.t.Fatal("matched HTTP benchmark lost complete, identical, error-free results")
			}
		}
	}
}
