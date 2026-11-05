package runtime

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"testing"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

// TestOptionalEmbeddingComposeSecurity keeps the offline model unprivileged.
// The owned Compose qualification separately proves real inference works.
func TestOptionalEmbeddingComposeSecurity(t *testing.T) {
	t.Parallel()
	repository := openTestRepository(t)
	content := string(readTestFile(t, repository, "compose.embeddings.yaml"))
	requireTextFragments(t, content, []string{
		`user: "10001:10001"`,
		"read_only: true",
		"cap_drop: [ALL]",
		`security_opt: ["no-new-privileges:true"]`,
		`HF_HUB_OFFLINE: "1"`,
		"pull_policy: never",
		"target: /model",
		"mem_limit: 4g",
		"pids_limit: 256",
	})
	if strings.Contains(content, "ports:") || strings.Contains(content, "privileged:") {
		t.Fatal("offline model must not publish host ports or request privileged mode")
	}
	makefile := string(readTestFile(t, repository, "Makefile"))
	requireTextFragments(t, makefile, []string{
		`$(PYTHON) -B test/packaging/data_checks.py yaml --yamllint "$(YAMLLINT)"`,
		"test/interoperability/Containerfile test/embeddings/Containerfile",
		"-f compose.yaml -f compose.embeddings.yaml -f test/embeddings/semantic.compose.yaml -f test/embeddings/semantic_restore.compose.yaml --profile semantic-restore -p open-agentic-catalog-semantic-check config --quiet",
	})
}

func TestContainerBuildContract(t *testing.T) {
	t.Parallel()

	repository := openTestRepository(t)
	containerfile := readTestFile(t, repository, "Containerfile")
	compatibilityFile := readTestFile(t, repository, "deployments/container/Dockerfile")
	if !bytes.Equal(containerfile, compatibilityFile) {
		t.Fatal("compatibility Dockerfile must be byte-identical to Containerfile")
	}

	content := string(containerfile)
	requireTextFragments(t, content, []string{
		"# syntax=docker/dockerfile:1.28.0@sha256:",
		"FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.27.3-alpine3.24@sha256:",
		"FROM scratch AS runtime",
		"ENV GOTOOLCHAIN=local",
		"ARG TARGETOS",
		"ARG TARGETARCH",
		"go mod download",
		"go mod verify",
		"COPY api ./api",
		"COPY cmd ./cmd",
		"COPY internal ./internal",
		"COPY migrations ./migrations",
		"COPY schemas ./schemas",
		"COPY samples ./samples",
		"RUN --network=none",
		"CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH}",
		"go build -mod=readonly -trimpath -buildvcs=false -tags=netgo,osusergo",
		"-ldflags=\"-s -w -buildid=",
		"-X main.version=${VERSION}",
		"-X main.commit=${COMMIT}",
		"-o /out/ ./cmd/catalogd ./cmd/catalogctl",
		"org.opencontainers.image.version=\"${VERSION}\"",
		"org.opencontainers.image.revision=\"${COMMIT}\"",
		"COPY --from=build --chmod=0555 /out/catalogd /usr/local/bin/catalogd",
		"COPY --from=build --chmod=0555 /out/catalogctl /usr/local/bin/catalogctl",
		"USER 65532:65532",
		"EXPOSE 8080 9090",
		"STOPSIGNAL SIGTERM",
		"ENTRYPOINT [\"/usr/local/bin/catalogd\"]",
		"CMD [\"api\"]",
	})

	syntaxLine, _, _ := strings.Cut(content, "\n")
	syntaxDigest, found := strings.CutPrefix(syntaxLine, "# syntax=docker/dockerfile:1.28.0@sha256:")
	if !found || len(syntaxDigest) != 64 || strings.Trim(syntaxDigest, "0123456789abcdef") != "" {
		t.Errorf("Dockerfile frontend must be pinned to a full lowercase sha256 digest: %q", syntaxLine)
	}

	for _, forbidden := range []string{"COPY . .", "ADD ", "USER root", "apk add", "apt-get", "COPY config", "distroless", "ubi10", "ca-certificates", "mkdir -p /out/objects", "/var/lib/open-agentic-catalog/objects"} {
		if strings.Contains(content, forbidden) {
			t.Errorf("Containerfile contains forbidden build pattern %q", forbidden)
		}
	}

	scratchStages := 0
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(line, "FROM ") {
			continue
		}
		fields := strings.Fields(line)
		imageIndex := 1
		for imageIndex < len(fields) && strings.HasPrefix(fields[imageIndex], "--") {
			imageIndex++
		}
		if imageIndex >= len(fields) {
			t.Errorf("container stage has no image: %q", line)
			continue
		}
		image := fields[imageIndex]
		if image == "scratch" {
			scratchStages++
			continue
		}
		parts := strings.SplitN(image, "@sha256:", 2)
		if len(parts) != 2 || len(parts[1]) != 64 {
			t.Errorf("container base image is not pinned to a full sha256 digest: %q", image)
		}
	}
	if scratchStages != 1 {
		t.Errorf("Containerfile must contain exactly one scratch runtime stage, found %d", scratchStages)
	}
	_, runtimeStage, found := strings.Cut(content, "FROM scratch AS runtime")
	if !found {
		t.Fatal("Containerfile has no named scratch runtime stage")
	}
	if count := strings.Count(runtimeStage, "\nCOPY "); count != 3 {
		t.Errorf("scratch runtime must copy the two service binaries and licenses, found %d COPY instructions", count)
	}
	for _, forbidden := range []string{"\nADD ", "\nRUN ", "\nENV ", "\nVOLUME ", "\nHEALTHCHECK "} {
		if strings.Contains(runtimeStage, forbidden) {
			t.Errorf("scratch runtime contains forbidden instruction %q", strings.TrimSpace(forbidden))
		}
	}

	for _, mainFile := range []string{"cmd/catalogd/main.go", "cmd/catalogctl/main.go"} {
		mainContent := string(readTestFile(t, repository, mainFile))
		if !strings.Contains(mainContent, `_ "golang.org/x/crypto/x509roots/fallback"`) {
			t.Errorf("%s must embed the maintained fallback X.509 root set", mainFile)
		}
	}

	makefile := string(readTestFile(t, repository, "Makefile"))
	requireTextFragments(t, makefile, []string{
		"GO_BUILD_FLAGS := -mod=readonly -trimpath -buildvcs=false -tags=netgo,osusergo",
		"LDFLAGS = -s -w -buildid=",
		"binary-check: build",
		"$(GO) tool buildid",
		"$(GO) tool nm",
		"container-build: container-sync-check",
		"CONTAINER_PLATFORM ?= linux/$(HOST_GOARCH)",
		"linux/amd64|linux/arm64",
		"$(CONTAINER_ENGINE) build --platform \"$(CONTAINER_PLATFORM)\" --target runtime",
		"container-verify: container-build",
		"$(PYTHON) test/packaging/image.py verify",
		"$(PYTHON) test/packaging/image.py inspect",
		"$(PYTHON) test/packaging/image.py sbom",
		"$(PYTHON) test/packaging/charts.py",
	})
	requireTextFragments(t, content, []string{"go install github.com/google/go-licenses@v1.6.0", "COPY --from=build /out/licenses/ /licenses/"})
}

func TestContainerIgnoreContract(t *testing.T) {
	t.Parallel()

	repository := openTestRepository(t)
	canonical := readTestFile(t, repository, ".containerignore")
	compatibility := readTestFile(t, repository, ".dockerignore")
	if !bytes.Equal(canonical, compatibility) {
		t.Fatal(".dockerignore must be byte-identical to .containerignore")
	}

	var entries []string
	for _, line := range strings.Split(string(canonical), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			entries = append(entries, line)
		}
	}
	expected := []string{
		"**",
		"!go.mod",
		"!go.sum",
		"!Containerfile",
		"!LICENSE",
		"!catalog-ui/",
		"!catalog-ui/Containerfile",
		"!catalog-ui/package.json",
		"!catalog-ui/package-lock.json",
		"!catalog-ui/index.html",
		"!catalog-ui/tsconfig.json",
		"!catalog-ui/tsconfig.build.json",
		"!catalog-ui/vite.config.ts",
		"!catalog-ui/src/",
		"!catalog-ui/src/**",
		"!catalog-ui/cmd/",
		"!catalog-ui/cmd/**",
		"!catalog-ui/internal/",
		"!catalog-ui/internal/**",
		"!catalog-ui/scripts/",
		"!catalog-ui/scripts/licenses.mjs",
		"!catalog-ui/scripts/e2e-instrument.mjs",
		"!documentation/",
		"!documentation/package.json",
		"!documentation/package-lock.json",
		"!documentation/astro.config.mjs",
		"!documentation/content-map.json",
		"!documentation/tsconfig.json",
		"!documentation/src/",
		"!documentation/src/**",
		"documentation/src/content/docs/generated/**",
		"!documentation/assets/",
		"!documentation/assets/**",
		"!documentation/scripts/",
		"!documentation/scripts/**",
		"!sdk/",
		"!sdk/README.md",
		"!sdk/go/",
		"!sdk/go/README.md",
		"!sdk/python/",
		"!sdk/python/README.md",
		"!sdk/typescript/",
		"!sdk/typescript/README.md",
		"!sdk/rust/",
		"!sdk/rust/README.md",
		"!extensions/",
		"extensions/*",
		"!extensions/vscode/",
		"extensions/vscode/*",
		"!extensions/vscode/README.md",
		"!extensions/vscode/media/",
		"extensions/vscode/media/*",
		"!extensions/vscode/media/catalog-preview.png",
		"!extensions/vscode/media/agent-conversation.png",
		"!extensions/vscode/media/mcp-selection-preview.png",
		"!extensions/vscode/media/my-tools-installed.png",
		"!CONTRIBUTING.md",
		"!SECURITY.md",
		"!config/",
		"!config/local.json",
		"!config/postgres.json",
		"!config/production.example.json",
		"!config/federation-policy.local.json",
		"!config/federation-policy.production.example.json",
		"!deployments/",
		"!deployments/helm/",
		"!deployments/helm/open-agentic-catalog/",
		"!deployments/helm/open-agentic-catalog/README.md",
		"!deployments/container/",
		"!deployments/container/Dockerfile",
		"!deployments/container/licenses.sh",
		"!api/",
		"!api/**",
		"!cmd/",
		"!cmd/**",
		"!internal/",
		"!internal/**",
		"!migrations/",
		"!migrations/**",
		"!schemas/",
		"!schemas/**",
		"!samples/",
		"!samples/**",
		"!test/",
		"!test/minikube/",
		"!test/minikube/fixtures/",
		"!test/minikube/fixtures/vault/",
		"!test/minikube/fixtures/vault/catalog-sync.yaml",
		"!test/minikube/fixtures/vault/runtime-policy.hcl",
		"!test/minikube/fixtures/vault/owner-policy.hcl",
		"!test/functional/",
		"!test/functional/Containerfile",
		"!test/functional/fixture/",
		"!test/functional/fixture/**",
		"!examples/",
		"!examples/langgraph/",
		"!examples/langgraph/README.md",
		"!examples/rust-catalog-report/",
		"!examples/rust-catalog-report/README.md",
		"!examples/publication-report/",
		"!examples/publication-report/README.md",
		"!examples/typescript-ard-list/",
		"!examples/typescript-ard-list/README.md",
		"!examples/webhookreceiver/",
		"!examples/webhookreceiver/verify.go",
		"!examples/webhookreceiver/README.md",
		"!examples/native-import/",
		"!examples/native-import/mcp-registry-response.json",
		"!examples/native-import/mcp-registry-import.yaml",
		"!examples/native-import/drafts/",
		"!examples/native-import/drafts/agent.json",
		"!examples/native-import/drafts/mcp-server.json",
		"!examples/native-import/drafts/skill.json",
		"!examples/native-import/drafts/plugin-archive.json",
		"!examples/native-import/drafts/skill-package.json",
		"**/*_test.go",
		"**/.env",
		"**/.env.*",
		".tools/dev/",
	}
	if strings.Join(entries, "\n") != strings.Join(expected, "\n") {
		t.Errorf("container build-context allowlist differs from the reviewed inputs\ngot:\n%s\nwant:\n%s", strings.Join(entries, "\n"), strings.Join(expected, "\n"))
	}
}

func TestMakefilePublicTargetContract(t *testing.T) {
	t.Parallel()

	repository := openTestRepository(t)
	content := string(readTestFile(t, repository, "Makefile"))
	if !strings.Contains(content, ".DEFAULT_GOAL := help") {
		t.Fatal("Makefile default goal must remain help")
	}

	phony := make(map[string]struct{})
	public := make(map[string]struct{})
	for lineNumber, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, ".PHONY:") {
			for _, target := range strings.Fields(strings.TrimPrefix(line, ".PHONY:")) {
				if _, duplicate := phony[target]; duplicate {
					t.Errorf("Makefile .PHONY lists %q more than once", target)
				}
				phony[target] = struct{}{}
			}
		}
		separator := strings.Index(line, ":")
		help := strings.Index(line, "##")
		if separator <= 0 || help < separator || strings.ContainsAny(line[:separator], " \t") {
			continue
		}
		target := line[:separator]
		if strings.TrimSpace(line[help+2:]) == "" {
			t.Errorf("Makefile line %d gives %q an empty help description", lineNumber+1, target)
		}
		if _, duplicate := public[target]; duplicate {
			t.Errorf("Makefile advertises %q more than once", target)
		}
		public[target] = struct{}{}
	}
	for target := range public {
		if _, exists := phony[target]; !exists {
			t.Errorf("advertised Make target %q is not phony", target)
		}
	}
	// The functional supervisor and ordered demo phases have owning public
	// launchers. Keep their exact internal helpers phony and out of public help.
	for _, privateWorkflow := range []string{
		"test-functional-workflow",
		"demo-up-validate", "demo-up-check", "demo-up-archives",
		"demo-up-compose", "demo-up-ready", "demo-up-seed",
	} {
		if _, exists := phony[privateWorkflow]; !exists {
			t.Errorf("internal workflow %q must remain phony", privateWorkflow)
		}
		if _, exists := public[privateWorkflow]; exists {
			t.Errorf("internal workflow %q must not be advertised as standalone", privateWorkflow)
		}
		delete(phony, privateWorkflow)
	}
	if !strings.Contains(content, `test -n "$${CATALOG_FUNCTIONAL_STATE_DIRECTORY:-}"`) {
		t.Fatal("internal functional workflow must require its owning supervisor")
	}
	for target := range phony {
		if _, exists := public[target]; !exists {
			t.Errorf("phony Make target %q has no help entry", target)
		}
	}
	if len(public) == 0 || len(public) != len(phony) {
		t.Errorf("Makefile public and phony target counts differ: public=%d phony=%d", len(public), len(phony))
	}
	for _, forbidden := range []string{"\nexport CATALOG_TEST_", "\nexport CATALOG_SMOKE_"} {
		if strings.Contains(content, forbidden) {
			t.Errorf("Makefile globally exports scoped credential input %q", strings.TrimSpace(forbidden))
		}
	}
}

func TestComposeUsesHardenedCanonicalContainerBuild(t *testing.T) {
	t.Parallel()

	repository := openTestRepository(t)
	content := string(readTestFile(t, repository, "compose.yaml"))
	requireTextFragments(t, content, []string{
		"image: pgvector/pgvector:pg18@sha256:",
		"image: ${IMAGE:-open-agentic-catalog:dev}",
		"pull_policy: never",
		"dockerfile: Containerfile",
		"target: runtime",
		"VERSION: ${VERSION:-dev}",
		"COMMIT: ${COMMIT:-unknown}",
		"127.0.0.1:${CATALOG_COMPOSE_HTTP_PORT:-8080}:8080",
		"127.0.0.1:${CATALOG_COMPOSE_POSTGRES_PORT:-55432}:5432",
		"./deployments/postgres/init.sql:/docker-entrypoint-initdb.d/010-catalog-roles.sql:ro",
		"catalog-postgres-18:/var/lib/postgresql",
		"entrypoint: [\"/usr/local/bin/catalogctl\"]",
		"command: [\"migrate\", \"apply\", \"--config\", \"/config/config.json\", \"--api-role\", \"catalog_api\", \"--worker-role\", \"catalog_worker\"]",
		"command: [\"api\", \"--config\", \"/config/config.json\"]",
		"command: [\"worker\", \"--config\", \"/config/config.json\"]",
		"CATALOG_DATABASE_DSN: postgres://catalog_api:catalog-api@postgres:5432/catalog?sslmode=disable",
		"CATALOG_DATABASE_DSN: postgres://catalog_worker:catalog-worker@postgres:5432/catalog?sslmode=disable",
		"CATALOG_DATABASE_DRIVER: postgres",
		"condition: service_completed_successfully",
		"read_only: true",
		"/tmp:size=64m,noexec,nosuid,nodev",
		"cap_drop:\n      - ALL",
		"no-new-privileges:true",
		"./config/postgres.json:/config/config.json:ro",
		"x-catalog-runtime-environment: &catalog-runtime-environment",
		"<<: *catalog-runtime-environment",
		"CATALOG_HTTP_ADDRESS: 0.0.0.0:8080",
		"CATALOG_WORKER_OPERATIONS_ADDRESS: 0.0.0.0:9090",
		`test: ["CMD", "/usr/local/bin/catalogctl", "probe", "--url", "http://127.0.0.1:8080/readyz"]`,
		`test: ["CMD", "/usr/local/bin/catalogctl", "probe", "--url", "http://127.0.0.1:9090/readyz"]`,
		"CATALOG_FEDERATION_POLICY_FILE: /config/federation-policy.json",
		"CATALOG_COMPOSE_FEDERATION_POLICY_FILE:-./config/federation-policy.local.json",
		"CATALOG_COMPOSE_FEDERATION_CREDENTIALS_DIRECTORY:-./config/federation-credentials.example",
		"CATALOG_COMPOSE_FEDERATION_TRUST_DIRECTORY:-./config/federation-trust.example",
		"subnet: ${CATALOG_COMPOSE_SUBNET:-172.30.255.0/24}",
	})
	if count := strings.Count(content, "\n    build:\n"); count != 1 {
		t.Errorf("Compose must build the shared application image exactly once, found %d build definitions", count)
	}
	if count := strings.Count(content, "\n    image: ${IMAGE:-open-agentic-catalog:dev}\n"); count != 4 {
		t.Errorf("Compose must use one configured application image for all four service roles, found %d references", count)
	}
	if count := strings.Count(content, "\n    pull_policy: never\n"); count != 4 {
		t.Errorf("Compose application roles must refuse an unintended registry pull, found %d policies", count)
	}
	if strings.Contains(content, "catalog-objects") || strings.Contains(content, "/var/lib/open-agentic-catalog/objects") {
		t.Fatal("Compose must not require a compatibility object volume")
	}
	if strings.Contains(content, "dockerfile: deployments/container/Dockerfile") {
		t.Fatal("Compose must build from the canonical root Containerfile")
	}
	if strings.Contains(content, `command: ["all"`) {
		t.Fatal("Compose must not combine PostgreSQL API and worker roles")
	}
	if strings.Contains(content, "${CATALOG_WEBHOOK_ENCRYPTION_KEY:-}") {
		t.Fatal("Compose must leave an omitted webhook key unset so development key derivation can apply")
	}
	runtimeEnvironment, _, found := strings.Cut(content, "\nservices:")
	if !found {
		t.Fatal("Compose runtime environment anchor is missing")
	}
	composeOwned := map[string]struct{}{
		"CATALOG_DATABASE_DSN":      {},
		"CATALOG_DATABASE_DSN_FILE": {},
	}
	for _, name := range config.EnvironmentVariables() {
		if _, owned := composeOwned[name]; owned {
			continue
		}
		if !strings.Contains(runtimeEnvironment, "\n  "+name+":") {
			t.Errorf("Compose runtime environment omits allowlisted variable %s", name)
		}
	}
	apiSection, workerSection, found := strings.Cut(content, "\n  worker:")
	if !found {
		t.Fatal("Compose worker section is missing")
	}
	for _, name := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_REGION", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ROLE_ARN", "AWS_ROLE_SESSION_NAME"} {
		if strings.Contains(apiSection, "\n      "+name+":") {
			t.Errorf("Compose API receives obsolete object provider variable %s", name)
		}
		if strings.Contains(workerSection, "\n      "+name+":") {
			t.Errorf("Compose worker receives obsolete object provider variable %s", name)
		}
	}
	if strings.Contains(workerSection, "\n    ports:") {
		t.Fatal("Compose must keep the worker operations listener off the host")
	}
	makefile := string(readTestFile(t, repository, "Makefile"))
	launcher := string(readTestFile(t, repository, "test/monitoring/launcher.py"))
	if !strings.Contains(makefile, "$(PYTHON) test/monitoring/launcher.py up") || !strings.Contains(launcher, `command(compose + ["build"], environment, seconds=600)`) || !strings.Contains(launcher, `"up", "--no-build", "--force-recreate", "--wait", "--wait-timeout", "180"`) {
		t.Fatal("compose-up must build images, recreate services and wait for healthy startup")
	}
	requireTextFragments(t, makefile, []string{
		"COMPOSE ?= docker compose",
		"COMPOSE_PROJECT ?= open-agentic-catalog",
		"compose-check compose-up: export VERSION = $(word 1,$(BUILD_IDENTITY))",
		"compose-check compose-up: export COMMIT = $(word 2,$(BUILD_IDENTITY))",
		"compose-check compose-up: export IMAGE := $(IMAGE)",
		"compose-check:",
		"$(COMPOSE) --env-file /dev/null -f compose.yaml -p open-agentic-catalog-compose-check config --quiet",
		"$(COMPOSE) --env-file /dev/null -f compose.integration.yaml -p open-agentic-catalog-integration-check config --quiet",
		"-f compose.yaml -f test/functional/compose.yaml -p open-agentic-catalog-functional-check config --quiet",
		"check: lint docs-check yaml-check container-check compose-check",
	})

	roles := string(readTestFile(t, repository, "deployments/postgres/init.sql"))
	requireTextFragments(t, roles, []string{
		"CREATE EXTENSION IF NOT EXISTS vector",
		"CREATE ROLE catalog_api",
		"NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS",
		"CREATE ROLE catalog_worker",
		"NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION BYPASSRLS",
		"GRANT CONNECT ON DATABASE catalog TO catalog_api, catalog_worker",
		"GRANT USAGE ON SCHEMA public TO catalog_api, catalog_worker",
	})
	if strings.Contains(roles, "ALTER DEFAULT PRIVILEGES") || strings.Contains(roles, "ON ALL TABLES") {
		t.Fatal("PostgreSQL bootstrap must leave runtime object and future-object grants to migration reconciliation")
	}
}

func TestPostgresIntegrationComposeIsIsolatedAndDisposable(t *testing.T) {
	t.Parallel()

	repository := openTestRepository(t)
	compose := string(readTestFile(t, repository, "compose.integration.yaml"))
	makefile := string(readTestFile(t, repository, "Makefile"))
	requireTextFragments(t, compose, []string{
		"image: pgvector/pgvector:pg18@sha256:",
		"POSTGRES_DB: catalog_test",
		"POSTGRES_USER: catalog",
		"pg_isready -U catalog -d catalog_test",
		"127.0.0.1::5432",
		"postgres-data:/var/lib/postgresql",
		"restart: \"no\"",
	})
	for _, forbidden := range []string{"build:", "api:", "worker:", "migrate:", "provision:", "minio", "catalog-postgres-18"} {
		if strings.Contains(compose, forbidden) {
			t.Errorf("integration Compose contains forbidden development-stack fragment %q", forbidden)
		}
	}
	requireTextFragments(t, makefile, []string{
		"project=\"open-agentic-catalog-integration-$${GITHUB_RUN_ID:+$${GITHUB_RUN_ID}-}$$$$\"",
		"$(COMPOSE) --env-file /dev/null -f compose.integration.yaml -p \"$$project\" up --detach --wait --wait-timeout 60 postgres",
		"$(COMPOSE) --env-file /dev/null -f compose.integration.yaml -p \"$$project\" down --volumes --remove-orphans",
		"PostgreSQL integration cleanup failed.",
		"$(MAKE) --no-print-directory $(1) CATALOG_TEST_DATABASE_DSN=\"postgres://catalog:catalog@127.0.0.1:$$port/catalog_test?sslmode=disable\"",
		"test-integration-dsn:",
		"coverage-postgres-dsn-profile:",
		"coverage-postgres-dsn-check:",
		"coverage-postgres-summary:",
	})
	if strings.Contains(makefile, "test-all: test-integration") || strings.Contains(makefile, "verify: test-integration") {
		t.Fatal("service-free verification must not start PostgreSQL integration")
	}
}

func TestFunctionalComposeWorkflowIsIsolatedAndDisposable(t *testing.T) {
	t.Parallel()

	repository := openTestRepository(t)
	makefile := string(readTestFile(t, repository, "Makefile"))
	requireTextFragments(t, makefile, []string{
		"test-functional: test-functional-default-off test-functional-webhooks",
		"test-functional-default-off:",
		"test-functional-webhooks:",
		"$(PYTHON) test/functional/runner.py run-webhooks",
		"$(PYTHON) test/functional/runner.py run",
		"test-functional-workflow:",
		"$(PYTHON) test/functional/runner.py identity",
		"$(PYTHON) test/functional/runner.py mark packaged-smoke",
		"project=\"open-agentic-catalog-functional-$$suffix\"",
		"image=\"$${CATALOG_FUNCTIONAL_CANDIDATE_IMAGE:-open-agentic-catalog:functional-$$suffix}\"",
		"catalog_compose pull --policy missing postgres",
		"catalog_compose build federation-fixture",
		"up --no-build --pull never --force-recreate --detach",
		"fixture_image=\"open-agentic-catalog:functional-$$suffix-fixture\"",
		"run_id=\"functional-$$suffix\"",
		"CATALOG_COMPOSE_HTTP_PORT=\"$$http_port\" CATALOG_COMPOSE_POSTGRES_PORT=0 CATALOG_COMPOSE_FIXTURE_PORT=\"$$fixture_port\" CATALOG_COMPOSE_SUBNET=\"$$subnet\"",
		"CATALOG_COMPOSE_FEDERATION_POLICY_FILE=\"$$fixture_directory/policy.json\"",
		"CATALOG_COMPOSE_FEDERATION_TRUST_DIRECTORY=\"$$fixture_directory/trust\"",
		"CATALOG_WORKER_SOURCE_MAINTENANCE_INTERVAL=1m",
		"CATALOG_HTTP_REQUEST_TIMEOUT=3s",
		"wait_worker_status()",
		"wait_worker_status 200 startupz 'Functional worker startup'",
		"wait_worker_status 200 livez 'Functional worker liveness'",
		"wait_worker_status 503 readyz 'Functional worker dependency readiness'",
		"wait_worker_status 200 livez 'Functional worker liveness during PostgreSQL outage'",
		"--env-file /dev/null -f compose.yaml -f test/functional/compose.yaml $$failover_overlay $$backup_overlay $$peer_overlay $$security_overlay $$generated_overlay -p \"$$project\"",
		"$(PYTHON) test/functional/runner.py run-peers",
		"test-backup-restore:",
		"$(PYTHON) test/functional/runner.py run-backup-restore",
		"failover_overlay=\"\"",
		"failover-planned|failover-abrupt) failover_overlay='-f test/functional/failover.compose.yaml'",
		"test-postgres-failover:",
		"$(PYTHON) test/functional/runner.py run-failover",
		"up --build --force-recreate --detach",
		"port api 8080",
		"port postgres 5432",
		"port federation-fixture 8443",
		"$(GO) run ./test/functional/fixture generate --directory \"$$fixture_directory\" --subnet \"$$subnet\"",
		"$(PYTHON) test/functional/federation.py --base-url \"$$base_url\" --fixture-url \"$$fixture_url\" --ca-file \"$$fixture_directory/trust/functional-ca.pem\" --run-id \"$$run_id\"",
		"run_smoke()",
		"catalog_compose exec -T api /usr/local/bin/catalogctl smoke --base-url http://127.0.0.1:8080 --run-id \"$$run_id\"",
		"$(PYTHON) test/functional/audit.py --base-url \"$$base_url\" --output \"$$audit_export_file\" --run-id \"$$run_id\"",
		"$(GO) run ./cmd/catalogctl audit verify-export --file \"$$audit_export_file\"",
		"LOCK TABLE namespaces IN ACCESS EXCLUSIVE MODE",
		"start_namespace_lock()",
		"wait_for_api_database_lock()",
		"$(PYTHON) test/functional/deadline.py --base-url \"$$base_url\" --operation read --expect deadline",
		"$(PYTHON) test/functional/deadline.py --base-url \"$$base_url\" --operation read --expect success",
		"$(PYTHON) test/functional/deadline.py --base-url \"$$base_url\" --operation command --expect deadline --run-id \"$$run_id\"",
		"Functional API did not reach the controlled database wait.",
		"Functional API remained ready during drain.",
		"Functional API exited with status %s during bounded in-flight drain.",
		"$(PYTHON) test/functional/deadline.py --base-url \"$$base_url\" --operation command --expect deadline-replay --run-id \"$$run_id\"",
		"$(PYTHON) test/functional/deadline.py --base-url \"$$base_url\" --operation command --expect success --run-id \"$$run_id\" --key-variant success",
		"$(PYTHON) test/functional/deadline.py --base-url \"$$base_url\" --operation command --expect success-replay --run-id \"$$run_id\" --key-variant success",
		"--admin-token local-admin",
		"--other-tenant-token other-tenant-admin",
		"json.load(sys.stdin)[\"resources\"]",
		"$$base_url/version",
		"$$base_url/openapi/control-v1.yaml\" | cmp - api/openapi/control-v1.yaml",
		"ALTER ROLE catalog_api SET default_transaction_read_only=on; ALTER ROLE catalog_worker SET default_transaction_read_only=on",
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename IN ('catalog_api','catalog_worker')",
		"wait_worker_status 503 readyz 'Functional worker read-only readiness'",
		"wait_worker_status 200 livez 'Functional worker liveness during read-only PostgreSQL sessions'",
		"ALTER ROLE catalog_api RESET default_transaction_read_only; ALTER ROLE catalog_worker RESET default_transaction_read_only",
		"stop --timeout 10 postgres",
		"wait_http_status 503 \"$$base_url/readyz\"",
		"$$base_url/livez",
		"start postgres",
		"up --no-build --force-recreate --detach",
		"Functional PostgreSQL container was not recreated.",
		"Functional worker container was not recreated.",
		"Functional migration job was not recreated.",
		"Functional identity-provisioning job was not recreated.",
		"replayed_smoke_report=\"$$(run_smoke)\"",
		"Durable workflow replay changed after stack re-creation.",
		"stop --timeout 20 api worker",
		"container inspect --format '{{.State.ExitCode}}'",
		"Functional API exited with status %s after SIGTERM.",
		"Functional worker exited with status %s after SIGTERM.",
		"up --no-deps --no-build --no-recreate --detach worker api",
		"$$base_url/.well-known/ard.json",
		"$$base_url/public/v1/artifacts/$$document_id",
		"CATALOG_FUNCTIONAL_RECOVERY_POINT=\"$$recovery_point\"",
		"sh test/functional/restore.sh",
		"test/functional/oidc-trust.compose.yaml",
		// Recreated fixture ports can accept and reset TLS before the listener is ready.
		// Retry startup transport failures without disabling CA validation or deadlines.
		`$(CURL) --fail --silent --show-error --retry 20 --retry-all-errors --retry-max-time 30 --retry-delay 1 --connect-timeout 2 --max-time 5 --cacert "$$fixture_directory/trust/functional-ca.pem" "https://127.0.0.1:$$fixture_port/.well-known/openid-configuration"`,
		"CATALOG_AUTH_TRUST_BUNDLE_FILE=/not-mounted/ca.pem",
		"OIDC trust bundle must be a readable regular file",
		"Functional Compose workflow, bounded request and atomic-command drain, exact replay and retry, audit export and restore qualification, selective repair, read/write readiness, dependency recovery, re-creation persistence, clean role restart, and scoped OIDC private-HTTPS discovery checks passed.",
	})
	if strings.Contains(makefile, "catalog_compose restart api worker") {
		t.Fatal("functional persistence must recreate one-shot jobs and process containers, not only restart API and worker")
	}
	if strings.Contains(makefile, "test-all: test-functional") || strings.Contains(makefile, "verify: test-functional") {
		t.Fatal("service-free verification must not start the functional Compose stack")
	}
}

func TestDevelopmentProfilesSupportSmokeIdentities(t *testing.T) {
	t.Parallel()

	repository := openTestRepository(t)
	for _, profile := range []string{"config/local.json", "config/postgres.json"} {
		content := string(readTestFile(t, repository, profile))
		for _, token := range []string{"local-admin", "local-contributor", "local-reviewer", "local-publisher", "local-reader", "other-tenant-admin"} {
			if !strings.Contains(content, `"`+token+`"`) {
				t.Errorf("%s does not define the %s smoke identity", profile, token)
			}
		}
	}
}

func TestEnvironmentTemplateContract(t *testing.T) {
	t.Parallel()

	repository := openTestRepository(t)
	environment := string(readTestFile(t, repository, ".env.example"))
	makefile := string(readTestFile(t, repository, "Makefile"))
	gitignore := string(readTestFile(t, repository, ".gitignore"))
	requireTextFragments(t, environment, []string{
		"# CATALOG_PUBLICATION_ARD_PROFILE=ard-0.91-aa3e598",
		"# CATALOG_PUBLICATION_AI_CATALOG_PROFILE=ai-catalog-1.0-b062278",
	})

	requireTextFragments(t, makefile, []string{
		"CATALOG_ENV_FILE ?= .env",
		`-include $(subst $(workflow-tab),\$(workflow-tab),$(subst $(workflow-space),\$(workflow-space),$(CATALOG_ENV_FILE)))`,
		"CATALOG_CONFIG_FILE ?= config/local.json",
		"CATALOG_CONFIG_ROLE ?= all",
		"CATALOG_POSTGRES_CONFIG_FILE ?= config/postgres.json",
		"CATALOG_CONTAINER_CONFIG_FILE ?= config/local.json",
		"CATALOG_MIGRATION_DATABASE_DSN ?= postgres://catalog:catalog@127.0.0.1:55432/catalog?sslmode=disable",
		"config validate --config \"$(CATALOG_CONFIG_FILE)\" --role \"$(CATALOG_CONFIG_ROLE)\"",
		"CATALOG_RUNTIME_ENV_TARGETS := run config-check env-check diagnostics compose-up",
		"$(1): export $(2) := $$($(2))",
		"$(foreach name,$(CATALOG_RUNTIME_ENV_VARS),$(eval $(call export_to_targets_if_defined,$(CATALOG_RUNTIME_ENV_TARGETS),$(name))))",
		"unexport $(CATALOG_RUNTIME_ENV_VARS)",
		"CATALOG_COMPOSE_ENV_VARS := CATALOG_COMPOSE_HTTP_PORT CATALOG_COMPOSE_POSTGRES_PORT",
		"CATALOG_COMPOSE_SUBNET",
		"CATALOG_COMPOSE_FEDERATION_POLICY_FILE CATALOG_COMPOSE_FEDERATION_CREDENTIALS_DIRECTORY",
		"CATALOG_COMPOSE_FEDERATION_TRUST_DIRECTORY CATALOG_COMPOSE_TRUST_VERIFICATION_DIRECTORY",
		"$(foreach name,$(CATALOG_COMPOSE_ENV_VARS),$(eval $(call export_to_targets_if_defined,compose-up,$(name))))",
		"unexport $(CATALOG_COMPOSE_ENV_VARS)",
		"CATALOG_POSTGRES_TEST_ENV_TARGETS := test-integration-dsn coverage-postgres-dsn-profile coverage-postgres-dsn-check",
		"$(foreach name,$(CATALOG_SMOKE_ENV_VARS),$(eval $(call export_to_targets_if_defined,smoke,$(name))))",
		"unexport DEMO_BASE_URL DEMO_STATE_FILE",
		"demo-seed: export DEMO_BASE_URL := $(DEMO_BASE_URL)",
		"demo-seed: export DEMO_STATE_FILE := $(DEMO_STATE_FILE)",
	})
	exportIndex := strings.Index(makefile, "$(foreach name,$(CATALOG_RUNTIME_ENV_VARS),$(eval $(call export_to_targets_if_defined,$(CATALOG_RUNTIME_ENV_TARGETS),$(name))))")
	unexportIndex := strings.Index(makefile, "unexport $(CATALOG_RUNTIME_ENV_VARS)")
	if exportIndex < 0 || unexportIndex <= exportIndex {
		t.Fatal("runtime variables must be captured for configuration targets before they are unexported from verification targets")
	}
	// Environment files are private at every depth; example templates remain visible.
	requireTextFragments(t, gitignore, []string{"/objects/", "/objects-*/", "\n.env\n", "\n.env.*\n", "\n!.env.example\n"})

	allowed := map[string]bool{
		"COMPOSE":         false,
		"COMPOSE_PROJECT": false,
		"DEMO_BASE_URL":   false,
		"DEMO_DATA_FILE":  false, "DEMO_SAMPLE_SET": false, "DEMO_EXTEND": false, "DEMO_ARCHIVE_ORIGIN": false, "DEMO_ARCHIVE_TRUST_FILE": false,
		"DEMO_STATE_FILE":                                  false,
		"CONTAINER_PLATFORM":                               false,
		"ARTIFACT_OUTPUT":                                  false,
		"CHART_VERSION":                                    false,
		"APP_VERSION":                                      false,
		"MINIKUBE_PRELOAD_ARCHIVE":                         false,
		"MINIKUBE_TIMEOUT":                                 false,
		"CATALOG_CONFIG_FILE":                              false,
		"CATALOG_CONFIG_ROLE":                              false,
		"CATALOG_POSTGRES_CONFIG_FILE":                     false,
		"CATALOG_CONTAINER_CONFIG_FILE":                    false,
		"CATALOG_COMPOSE_HTTP_PORT":                        false,
		"CATALOG_COMPOSE_POSTGRES_PORT":                    false,
		"CATALOG_COMPOSE_SUBNET":                           false,
		"CATALOG_COMPOSE_FEDERATION_POLICY_FILE":           false,
		"CATALOG_COMPOSE_WEBHOOK_DIRECTORY":                false,
		"CATALOG_COMPOSE_FEDERATION_CREDENTIALS_DIRECTORY": false,
		"CATALOG_COMPOSE_FEDERATION_TRUST_DIRECTORY":       false,
		"CATALOG_COMPOSE_TRUST_VERIFICATION_DIRECTORY":     false,
		"CATALOG_MIGRATION_DATABASE_DSN":                   false,
		"CATALOG_DATABASE_API_ROLE":                        false,
		"CATALOG_DATABASE_WORKER_ROLE":                     false,
		"CATALOG_TEST_DATABASE_DSN":                        false,
		"VERSION":                                          false,
		"COMMIT":                                           false,
		"IMAGE":                                            false,
		"LOCAL_MONITORING":                                 false,
		"LOCAL_DEMO_ARCHIVES":                              false,
		"LOCAL_DEMO_RUNTIMES":                              false,
		"DEMO_RUNTIME_DIR":                                 false,
		"DEMO_A2A_PORT":                                    false,
		"DEMO_MCP_PORT":                                    false,
		"DEMO_ARCHIVE_DIR":                                 false,
		"DEMO_ARCHIVE_IP":                                  false,
		"DEMO_ARCHIVE_PORT":                                false,
		"LOCAL_UI":                                         false,
		"LOCAL_EMBEDDINGS":                                 false,
		"LOCAL_WEBHOOKS":                                   false,
		"UI_IMAGE":                                         false,
		"CATALOG_UI_LOG_LEVEL":                             false, "CATALOG_UI_LOG_FORMAT": false,
		"CATALOG_UI_PORT":                    false,
		"CATALOG_UI_DOCUMENTATION_URL":       false,
		"UI_DOCUMENTATION_DIRECTORY":         false,
		"TRACE_VIEWER_UI_PORT":               false,
		"TRACE_VIEWER_OTLP_PORT":             false,
		"CATALOG_MONITORING_PROMETHEUS_PORT": false,
		"CATALOG_MONITORING_GRAFANA_PORT":    false,
		"CATALOG_MONITORING_RETENTION_HOURS": false,
		"CATALOG_MONITORING_RETENTION_MB":    false,
		"COVERAGE_MIN":                       false,
		"CORE_COVERAGE_MIN":                  false,
		"CRITICAL_PACKAGE_COVERAGE_MIN":      false,
		"POSTGRES_COVERAGE_MIN":              false,
		"FUZZ_TIME":                          false,
		"SEARCH_PROJECTION_REPAIR_LIMIT":     false,
		"RESTORE_VERIFY_LIMIT":               false,
		"ARD_MANIFEST_URL":                   false,
		"ARD_PUBLISHER_URL":                  false,
		"ARD_REGISTRY_URL":                   false,
		"MONITORING_CONTEXT":                 false,
		"MONITORING_NAMESPACE":               false,
		"MONITORING_RELEASE":                 false,
		"MONITORING_VALUES":                  false,
		"MONITORING_COMPONENT":               false,
		"MONITORING_PORT":                    false,
	}
	runtimeAllowed := make(map[string]struct{})
	for _, name := range config.EnvironmentVariables() {
		allowed[name] = false
		runtimeAllowed[name] = struct{}{}
	}
	makeRuntimeVariables := makeVariableWords(makefile, "CATALOG_RUNTIME_ENV_VARS")
	makeRuntimeSeen := make(map[string]struct{}, len(makeRuntimeVariables))
	for _, name := range makeRuntimeVariables {
		if _, duplicate := makeRuntimeSeen[name]; duplicate {
			t.Errorf("CATALOG_RUNTIME_ENV_VARS lists %q more than once", name)
		}
		makeRuntimeSeen[name] = struct{}{}
		if _, exists := runtimeAllowed[name]; !exists {
			t.Errorf("CATALOG_RUNTIME_ENV_VARS exports unsupported variable %q", name)
		}
	}
	for _, name := range config.EnvironmentVariables() {
		if _, exists := makeRuntimeSeen[name]; !exists {
			t.Errorf("CATALOG_RUNTIME_ENV_VARS omits loader variable %q", name)
		}
	}
	secretBearing := map[string]struct{}{
		"CATALOG_DATABASE_DSN":                {},
		"CATALOG_DATABASE_DSN_FILE":           {},
		"CATALOG_MIGRATION_DATABASE_DSN":      {},
		"CATALOG_WEBHOOK_ENCRYPTION_KEY":      {},
		"CATALOG_WEBHOOK_ENCRYPTION_KEY_FILE": {},
		"CATALOG_CURSOR_SIGNING_KEY":          {},
		"CATALOG_CURSOR_SIGNING_KEY_FILE":     {},
		"CATALOG_TEST_DATABASE_DSN":           {},
	}

	for lineNumber, line := range strings.Split(environment, "\n") {
		candidate := strings.TrimSpace(line)
		commented := strings.HasPrefix(candidate, "#")
		candidate = strings.TrimSpace(strings.TrimPrefix(candidate, "#"))
		name, _, assignment := strings.Cut(candidate, "=")
		if !assignment || strings.ContainsAny(name, " \t") {
			continue
		}
		seen, known := allowed[name]
		if !known {
			t.Errorf(".env.example line %d declares unsupported variable %q", lineNumber+1, name)
			continue
		}
		if seen {
			t.Errorf(".env.example declares %q more than once", name)
		}
		allowed[name] = true
		if _, sensitive := secretBearing[name]; sensitive && !commented {
			t.Errorf(".env.example must not assign secret-bearing variable %q", name)
		}
	}
	for name, seen := range allowed {
		if !seen {
			t.Errorf(".env.example does not document %q", name)
		}
	}
}

func TestHelmConfigurationUsesValidatedEnvironmentAndConditionalSecrets(t *testing.T) {
	t.Parallel()

	repository := openTestRepository(t)
	values := string(readTestFile(t, repository, "deployments/helm/open-agentic-catalog/values.yaml"))
	schema := readTestFile(t, repository, "deployments/helm/open-agentic-catalog/values.schema.json")
	var decodedSchema struct {
		Properties struct {
			ConfigEnv struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"configEnv"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &decodedSchema); err != nil {
		t.Fatalf("decode Helm values schema: %v", err)
	}
	requireTextFragments(t, values, []string{
		"digest: \"\"",
		"imagePullSecrets: []",
		"terminationGracePeriodSeconds: 40",
		"migration:",
		"serviceAccounts:",
		"activeDeadlineSeconds: 900",
		"podDisruptionBudget:",
		"configEnv:",
		"CATALOG_ENVIRONMENT: production",
		"CATALOG_PUBLICATION_ARD_PROFILE: ard-0.91-aa3e598",
		"CATALOG_PUBLICATION_AI_CATALOG_PROFILE: ai-catalog-1.0-b062278",
		"CATALOG_WEBHOOKS_ENABLED: \"false\"",
		"CATALOG_SEMANTIC_SEARCH: '{\"enabled\":false}'",
		"webhookEncryption: null",
		"federationCredentials: null",
		"policyId\": \"helm-default-deny",
		"CATALOG_FEDERATION_POLICY_FILE: /etc/open-agentic-catalog/federation/policy/policy.json",
		"CATALOG_WORKER_OPERATIONS_ADDRESS: 0.0.0.0:9090",
	})
	requireTextFragments(t, string(schema), []string{`"digest": {"type": "string", "pattern": "^(?:|sha256:[a-f0-9]{64})$"}`, `"imagePullSecrets"`, `"terminationGracePeriodSeconds"`, `"migration"`, `"activeDeadlineSeconds"`, `"podDisruptionBudget"`, `"podAnnotationMap"`, `"checksum/config"`, `"checksum/federation-policy"`, `"serviceAccounts"`, `"resourceAnnotationMap"`, `"helm.sh/hook-delete-policy"`, `"federationCredentials"`, `"CATALOG_FEDERATION_POLICY_FILE": {"const": "/etc/open-agentic-catalog/federation/policy/policy.json"}`, `"CATALOG_CREDENTIAL_RESOLUTION_DIRECTORY": {"const": "/etc/open-agentic-catalog/federation/credentials"}`, `"CATALOG_HTTP_ADDRESS": {"const": "0.0.0.0:8080"}`, `"CATALOG_WORKER_OPERATIONS_ADDRESS": {"const": "0.0.0.0:9090"}`, `"CATALOG_DATABASE_DRIVER": {"const": "postgres"}`, `"CATALOG_AUTH_MODE": {"const": "oidc"}`, `"const": "ard-0.91-aa3e598"`, `"const": "ai-catalog-1.0-b062278"`})
	helpers := string(readTestFile(t, repository, "deployments/helm/open-agentic-catalog/templates/_helpers.tpl"))
	requireTextFragments(t, helpers, []string{
		"if .Values.fullnameOverride",
		`define "open-agentic-catalog.resourceName"`,
		`define "open-agentic-catalog.selectorLabels"`,
		`app.kubernetes.io/name: {{ include "open-agentic-catalog.name" . | quote }}`,
		`app.kubernetes.io/instance: {{ .Release.Name | quote }}`,
		`define "open-agentic-catalog.configData"`,
		`$inactiveDedicatedTenant`,
		`$inactiveOrganizationClaim`,
		`define "open-agentic-catalog.image"`,
		`printf "%s@%s" .Values.image.repository .Values.image.digest`,
		`define "open-agentic-catalog.serviceAccountName"`,
		`define "open-agentic-catalog.validate"`,
		"the API, worker, and migration database Secret name/key references must be distinct",
		"the API, worker, and migration ServiceAccount names must be distinct",
	})
	for _, filename := range []string{"deployment-api.yaml", "deployment-worker.yaml"} {
		content := string(readTestFile(t, repository, "deployments/helm/open-agentic-catalog/templates/"+filename))
		checksum := `checksum/config: {{ include "open-agentic-catalog.configData" . | trim | sha256sum | quote }}`
		if filename == "deployment-api.yaml" {
			checksum = `checksum/config: {{ printf "%s\n%s" (include "open-agentic-catalog.configData" . | trim) .Values.auth.trustBundleConfigMap | sha256sum | quote }}`
		}
		requireTextFragments(t, content, []string{
			"envFrom:",
			"configMapRef:",
			"CATALOG_CURSOR_SIGNING_KEY",
			"if eq (index .Values.configEnv \"CATALOG_WEBHOOKS_ENABLED\") \"true\"",
			"CATALOG_WEBHOOK_ENCRYPTION_KEY",
			"checksum/federation-policy:",
			checksum,
			`omit .Values.podAnnotations "checksum/config" "checksum/federation-policy"`,
			"name: federation-policy",
			"mountPath: /etc/open-agentic-catalog/federation/policy",
			"CATALOG_CREDENTIAL_RESOLUTION_ENABLED",
			"mountPath: /etc/open-agentic-catalog/federation/credentials",
			"mountPath: /etc/open-agentic-catalog/federation/trust",
			"enableServiceLinks: false",
			"automountServiceAccountToken: false",
			"terminationGracePeriodSeconds: {{ .Values.terminationGracePeriodSeconds }}",
			"securityContext: {runAsNonRoot: true, seccompProfile: {type: RuntimeDefault}}",
			"securityContext: {allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: {drop: [\"ALL\"]}}",
			"emptyDir: {sizeLimit: 64Mi}",
			"imagePullSecrets:",
			`image: {{ include "open-agentic-catalog.image" . | quote }}`,
			`open-agentic-catalog.serviceAccountName`,
		})
		if strings.Contains(content, "/config/config.json") {
			t.Errorf("%s still requires a JSON configuration mount", filename)
		}
	}
	federationPolicy := string(readTestFile(t, repository, "deployments/helm/open-agentic-catalog/templates/configmap-federation.yaml"))
	requireTextFragments(t, federationPolicy, []string{"kind: ConfigMap", "federation.policy.existingConfigMap", "federation.policy.document"})
	apiDeployment := string(readTestFile(t, repository, "deployments/helm/open-agentic-catalog/templates/deployment-api.yaml"))
	for _, forbidden := range []string{"CATALOG_OBJECT_", "s3Credentials", "AWS_"} {
		if strings.Contains(apiDeployment, forbidden) {
			t.Errorf("API deployment retains obsolete object setting %q", forbidden)
		}
	}
	workerDeployment := string(readTestFile(t, repository, "deployments/helm/open-agentic-catalog/templates/deployment-worker.yaml"))
	requireTextFragments(t, workerDeployment, []string{
		"ports: [{name: operations, containerPort: 9090, protocol: TCP}]",
		"startupProbe: {httpGet: {path: /startupz, port: operations}",
		"livenessProbe: {httpGet: {path: /livez, port: operations}",
		"readinessProbe: {httpGet: {path: /readyz, port: operations}",
	})
	for _, forbidden := range []string{".Values.secrets.s3Credentials", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN"} {
		if strings.Contains(workerDeployment, forbidden) {
			t.Errorf("worker deployment receives API-only legacy object credential %q", forbidden)
		}
	}
	service := string(readTestFile(t, repository, "deployments/helm/open-agentic-catalog/templates/service.yaml"))
	if strings.Contains(service, "operations") || strings.Contains(service, "9090") {
		t.Fatal("Helm Service must not publish the worker operations listener")
	}
	migration := string(readTestFile(t, repository, "deployments/helm/open-agentic-catalog/templates/job-migrate.yaml"))
	requireTextFragments(t, migration, []string{
		`name: {{ include "open-agentic-catalog.resourceName" (dict "root" . "suffix" "migrate") | quote }}`,
		"helm.sh/hook: pre-install,pre-upgrade",
		`helm.sh/hook-weight: "-5"`,
		"automountServiceAccountToken: false",
		"catalogctl\", \"migrate\", \"apply",
		"envFrom:",
		"CATALOG_DATABASE_DSN",
		"activeDeadlineSeconds: {{ .Values.migration.activeDeadlineSeconds }}",
		"resources:",
		"enableServiceLinks: false",
		"imagePullSecrets:",
		"securityContext: {runAsNonRoot: true, seccompProfile: {type: RuntimeDefault}}",
		"securityContext: {allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: {drop: [\"ALL\"]}}",
		"emptyDir: {sizeLimit: 64Mi}",
		`serviceAccountName: {{ include "open-agentic-catalog.serviceAccountName" (dict "root" . "role" "migration") | quote }}`,
	})
	if strings.Contains(migration, "/config/config.json") {
		t.Fatal("migration job still requires a JSON configuration mount")
	}
	if strings.Contains(migration, ".Release.Revision") {
		t.Fatal("migration hook Job name changes by release revision and cannot replace a failed prior hook")
	}
	serviceAccounts := string(readTestFile(t, repository, "deployments/helm/open-agentic-catalog/templates/serviceaccount.yaml"))
	requireTextFragments(t, serviceAccounts, []string{
		`range $role := list "api" "worker"`,
		`$migration := .Values.serviceAccounts.migration`,
		"helm.sh/hook: pre-install,pre-upgrade",
		`helm.sh/hook-weight: "-20"`,
		"helm.sh/hook-delete-policy: before-hook-creation,hook-succeeded",
		"automountServiceAccountToken: false",
	})
	migrationConfig := string(readTestFile(t, repository, "deployments/helm/open-agentic-catalog/templates/configmap-migration.yaml"))
	requireTextFragments(t, migrationConfig, []string{
		"helm.sh/hook: pre-install,pre-upgrade",
		`helm.sh/hook-weight: "-10"`,
		"helm.sh/hook-delete-policy: before-hook-creation,hook-succeeded",
		"CATALOG_ENVIRONMENT:",
		"CATALOG_DATABASE_DRIVER:",
		"CATALOG_DATABASE_MAX_CONNECTIONS:",
	})
	for _, applicationOnly := range []string{"CATALOG_AUTH_ISSUER", "CATALOG_DISCOVERY_HOSTS", "CATALOG_FEDERATION_POLICY_FILE", "CATALOG_WEBHOOKS_ENABLED"} {
		if strings.Contains(migrationConfig, applicationOnly) {
			t.Errorf("migration ConfigMap receives application-only setting %s", applicationOnly)
		}
	}
	apiPDB := string(readTestFile(t, repository, "deployments/helm/open-agentic-catalog/templates/pdb.yaml"))
	workerPDB := string(readTestFile(t, repository, "deployments/helm/open-agentic-catalog/templates/pdb-worker.yaml"))
	requireTextFragments(t, apiPDB, []string{".Values.api.podDisruptionBudget.enabled", ".Values.api.podDisruptionBudget.maxUnavailable"})
	requireTextFragments(t, workerPDB, []string{".Values.worker.podDisruptionBudget.enabled", ".Values.worker.podDisruptionBudget.maxUnavailable"})
	makefile := string(readTestFile(t, repository, "Makefile"))
	requireTextFragments(t, makefile, []string{
		"helm-lint:",
		"helm-test:",
		"helm-check: helm-lint helm-test",
		"helm-schema-check:",
		"deploy-check: helm-check helm-schema-check monitoring-schema-check container-check",
		"KUBECONFORM_IMAGE := ghcr.io/yannh/kubeconform:$(KUBECONFORM_VERSION)@sha256:",
		"HELM_CHECK_VALUES :=",
		"HELM_KUBE_VERSION := 1.34.0",
		"ambiguous-values.yaml",
		"--is-upgrade --values $(HELM_CHART)/ci/optional-values.yaml",
		"$(KUBECONFORM_IMAGE) -strict -summary",
	})
	profiles, err := fs.Glob(repository.FS(), "deployments/helm/open-agentic-catalog/ci/*.yaml")
	if err != nil || len(profiles) == 0 {
		t.Fatalf("chart profile inventory: %v", err)
	}
	var selected string
	for _, line := range strings.Split(makefile, "\n") {
		if strings.HasPrefix(line, "HELM_CHECK_VALUES :=") {
			selected = strings.TrimPrefix(line, "HELM_CHECK_VALUES :=")
		}
	}
	fields := strings.Fields(selected)
	if len(fields) != len(profiles) {
		t.Fatalf("chart gate must select all %d profiles: %s", len(profiles), selected)
	}
	for _, profile := range profiles {
		if !slices.Contains(fields, "$(HELM_CHART)/ci/"+filepath.Base(profile)) {
			t.Errorf("chart gate omits profile %s", profile)
		}
	}
	helmTests := string(readTestFile(t, repository, "test/helm/chart_test.go"))
	requireTextFragments(t, helmTests, []string{
		"//go:build helm",
		"func TestDefaultRenderContract",
		"func TestSharedAndOptionalProfiles",
		"func TestUnsafeValuesAreRejected",
		"func TestPackagedChartExcludesTestInputsAndRenders",
		"validateRenderedConfiguration",
	})
	helmIgnore := string(readTestFile(t, repository, "deployments/helm/open-agentic-catalog/.helmignore"))
	requireTextFragments(t, helmIgnore, []string{"*.tgz", "ci/"})
	ambiguousValues := string(readTestFile(t, repository, "deployments/helm/open-agentic-catalog/ci/ambiguous-values.yaml"))
	requireTextFragments(t, ambiguousValues, []string{`fullnameOverride: "null"`, `nameOverride: "false"`, `name: "true"`, `key: "false"`})
	for _, name := range []string{"CATALOG_DATABASE_DSN", "CATALOG_CURSOR_SIGNING_KEY", "CATALOG_WEBHOOK_ENCRYPTION_KEY"} {
		if strings.Contains(values, name+":") {
			t.Errorf("Helm ConfigMap values contain secret-bearing variable %s", name)
		}
	}
	if strings.Contains(values, "CATALOG_HTTP_CORS_ORIGINS:") {
		t.Error("default Helm values enable a placeholder CORS origin")
	}
	allowed := make(map[string]struct{})
	for _, name := range config.EnvironmentVariables() {
		allowed[name] = struct{}{}
	}
	nonSecretRuntime := make(map[string]struct{}, len(allowed))
	for name := range allowed {
		nonSecretRuntime[name] = struct{}{}
	}
	for _, name := range []string{"CATALOG_DATABASE_DSN", "CATALOG_DATABASE_DSN_FILE", "CATALOG_CURSOR_SIGNING_KEY", "CATALOG_CURSOR_SIGNING_KEY_FILE", "CATALOG_WEBHOOK_ENCRYPTION_KEY", "CATALOG_WEBHOOK_ENCRYPTION_KEY_FILE"} {
		delete(nonSecretRuntime, name)
	}
	// This allowlisted runtime input is projected directly onto the API from
	// auth.trustBundleConfigMap; it must not enter the shared configEnv map.
	delete(nonSecretRuntime, "CATALOG_AUTH_TRUST_BUNDLE_FILE")
	for name := range nonSecretRuntime {
		if _, exists := decodedSchema.Properties.ConfigEnv.Properties[name]; !exists {
			t.Errorf("Helm values schema omits allowlisted non-secret runtime variable %s", name)
		}
	}
	for name := range decodedSchema.Properties.ConfigEnv.Properties {
		if _, exists := nonSecretRuntime[name]; !exists {
			t.Errorf("Helm values schema exposes unsupported ConfigMap variable %s", name)
		}
	}
	inConfig := false
	for lineNumber, line := range strings.Split(values, "\n") {
		if line == "configEnv:" {
			inConfig = true
			continue
		}
		if !inConfig {
			continue
		}
		if !strings.HasPrefix(line, "  ") {
			break
		}
		name, _, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || !strings.HasPrefix(name, "CATALOG_") {
			continue
		}
		if _, exists := allowed[name]; !exists {
			t.Errorf("values.yaml line %d uses runtime variable %s outside the loader allowlist", lineNumber+1, name)
		}
		if !bytes.Contains(schema, []byte(`"`+name+`"`)) {
			t.Errorf("values schema does not declare %s", name)
		}
	}
}

func TestOfflineVulnerabilityTargetContract(t *testing.T) {
	t.Parallel()

	repository := openTestRepository(t)
	makefile := string(readTestFile(t, repository, "Makefile"))
	requireTextFragments(t, makefile, []string{
		"OFFLINE_GO_PROXY ?= file://$(GO_MODULE_CACHE)/cache/download",
		"GOVULNCHECK_DATABASE ?=",
		"vulncheck-offline:",
		`case "$(GOVULNCHECK_DATABASE)" in file:///*)`,
		`case "$(OFFLINE_GO_PROXY)" in file:///*)`,
		"GOPROXY=\"$(OFFLINE_GO_PROXY)\" GOSUMDB=off GOTELEMETRY=off",
		`-db "$(GOVULNCHECK_DATABASE)" $(GO_SOURCE_PACKAGES)`,
	})
}

func openTestRepository(t *testing.T) *os.Root {
	t.Helper()

	_, filename, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("resolve test source location")
	}
	repositoryPath := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", ".."))
	repository, err := os.OpenRoot(repositoryPath)
	if err != nil {
		t.Fatalf("open repository root: %v", err)
	}
	t.Cleanup(func() {
		if err := repository.Close(); err != nil {
			t.Errorf("close repository root: %v", err)
		}
	})
	return repository
}

func readTestFile(t *testing.T, repository *os.Root, name string) []byte {
	t.Helper()

	content, err := repository.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return content
}

func requireTextFragments(t *testing.T, content string, fragments []string) {
	t.Helper()

	for _, fragment := range fragments {
		if !strings.Contains(content, fragment) {
			t.Errorf("expected content to include %q", fragment)
		}
	}
}

func makeVariableWords(content, name string) []string {
	prefix := name + " :="
	lines := strings.Split(content, "\n")
	words := make([]string, 0)
	collecting := false
	for _, line := range lines {
		if !collecting {
			var found bool
			line, found = strings.CutPrefix(line, prefix)
			if !found {
				continue
			}
			collecting = true
		}
		line = strings.TrimSpace(line)
		continued := strings.HasSuffix(line, "\\")
		line = strings.TrimSpace(strings.TrimSuffix(line, "\\"))
		words = append(words, strings.Fields(line)...)
		if !continued {
			return words
		}
	}
	return words
}
