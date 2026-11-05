# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# 📚 Open Agentic Catalog - development workflows
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# Usage: make or make help lists every public target by section.
# Source map: shared setup first, then these source/help sections.
#
#  1. Help and aggregate workflows
#  2. Build and local development
#  3. Code quality and security
#  4. Unit and local functional tests
#  5. Coverage
#  6. PostgreSQL tests and operator commands
#  7. API, standards and client compatibility
#  8. Optional UI
#  9. Documentation
# 10. Containers and local artifacts
# 11. Local Compose environment
# 12. Packaged functional and recovery scenarios
# 13. Helm, Minikube and monitoring
# 14. Embeddings and search relevance
# 15. Performance and measurement
# 16. Hooks and cleanup
#
# Public targets carry a ## help description and must be listed in .PHONY.
# Use ##@ for source/help sections; keep internal workflows out of public help.
# Keep workflow instructions in documentation/src/content/docs/contributing/make-targets.md
# and tool/build requirements in contributing/build-and-release.md in that manual.

# ─────────────────────────────────────────────────────────────────────────────
# Execution policy and workflow isolation
# ─────────────────────────────────────────────────────────────────────────────
SHELL := /bin/sh

ifneq ($(filter dev-tools-install version-bump version-bump-dry-run release-check,$(MAKECMDGOALS)),)
ifneq ($(words $(MAKECMDGOALS)),1)
$(error Tool installation and version mutation/release checks must run alone)
endif
override CATALOG_ENV_FILE := /dev/null
endif

# Local workflow commands never load deployment settings or combine workflows.
LOCAL_WORKFLOW_TARGETS := workflow-local-tools-install workflow-local-list workflow-local-lint workflow-local-unit workflow-local-ui workflow-local-docs workflow-local-charts workflow-local-image workflow-local-security workflow-local-postgres
ifneq ($(filter $(LOCAL_WORKFLOW_TARGETS),$(MAKECMDGOALS)),)
ifneq ($(words $(MAKECMDGOALS)),1)
$(error Local workflow targets must run alone)
endif
override CATALOG_ENV_FILE := /dev/null
WORKFLOW_LOCAL_PRODUCT ?= backend
override WORKFLOW_LOCAL_PRODUCT := $(value WORKFLOW_LOCAL_PRODUCT)
WORKFLOW_LOCAL_SCAN ?= true
override WORKFLOW_LOCAL_SCAN := $(value WORKFLOW_LOCAL_SCAN)
WORKFLOW_LOCAL_RELEASE_CANDIDATE ?= false
override WORKFLOW_LOCAL_RELEASE_CANDIDATE := $(value WORKFLOW_LOCAL_RELEASE_CANDIDATE)
export WORKFLOW_LOCAL_PRODUCT WORKFLOW_LOCAL_SCAN WORKFLOW_LOCAL_RELEASE_CANDIDATE
endif

# Workflow acquisition and validation are explicit, environment-isolated goals.
ifneq ($(filter workflow-tools-install workflow-check,$(MAKECMDGOALS)),)
ifneq ($(words $(MAKECMDGOALS)),1)
$(error Workflow tool targets must run alone)
endif
override CATALOG_ENV_FILE := /dev/null
endif

# Workflow files are trusted Make syntax, not runtime dotenv input. Measurement
# goals bypass them before inclusion so developer settings cannot select targets.
CATALOG_ENV_FILE ?= .env
# Keep the launcher dry-run safe while passing the resolved recursive Make path.
PITR_MAKE_COMMAND := $(MAKE)
ifneq ($(filter test-postgres-pitr-prepare test-postgres-pitr,$(MAKECMDGOALS)),)
ifneq ($(words $(MAKECMDGOALS)),1)
$(error PostgreSQL PITR targets must run alone)
endif
override CATALOG_ENV_FILE := /dev/null
endif
ifneq ($(filter sonarqube-tools-install sonarqube-up sonarqube-scan sonarqube-status sonarqube-credentials sonarqube-down,$(MAKECMDGOALS)),)
ifneq ($(words $(MAKECMDGOALS)),1)
$(error SonarQube targets must run alone)
endif
override CATALOG_ENV_FILE := /dev/null
endif
ifneq ($(filter zap-tools-install test-security-passive test-security-active schemathesis-tools-install test-api-generated,$(MAKECMDGOALS)),)
ifneq ($(words $(MAKECMDGOALS)),1)
$(error Scanner and generated API targets must run alone)
endif
override CATALOG_ENV_FILE := /dev/null
endif
ifneq ($(filter formal-tools-install test-formal,$(MAKECMDGOALS)),)
ifneq ($(words $(MAKECMDGOALS)),1)
$(error Formal tool targets must run alone)
endif
override CATALOG_ENV_FILE := /dev/null
endif
ifneq ($(filter test-property,$(MAKECMDGOALS)),)
ifneq ($(words $(MAKECMDGOALS)),1)
$(error Property testing must run alone)
endif
override CATALOG_ENV_FILE := /dev/null
endif
ifneq ($(filter test-mutation,$(MAKECMDGOALS)),)
ifneq ($(words $(MAKECMDGOALS)),1)
$(error Mutation testing must run alone)
endif
override CATALOG_ENV_FILE := /dev/null
endif
ifneq ($(filter coverage-full,$(MAKECMDGOALS)),)
ifneq ($(words $(MAKECMDGOALS)),1)
$(error Full coverage must run alone)
endif
override CATALOG_ENV_FILE := /dev/null
endif
ifneq ($(filter test-unit coverage-unit ui-test-unit test-tooling-unit ui-test-property ui-test-mutation,$(MAKECMDGOALS)),)
ifneq ($(words $(MAKECMDGOALS)),1)
$(error Unit test targets must run alone)
endif
override CATALOG_ENV_FILE := /dev/null
endif
ifneq ($(filter clean clean-dry-run clean-output clean-output-dry-run,$(MAKECMDGOALS)),)
ifneq ($(words $(MAKECMDGOALS)),1)
$(error Cleanup targets must run alone)
endif
override CATALOG_ENV_FILE := /dev/null
endif
ifneq ($(filter test-interoperability-pi-ard pi-ard-tools-install test-interoperability-pi-ard-prepare ard-explorer-tools-install test-interoperability-ard-explorer-startup test-interoperability-ard-explorer plugin-tools-install test-interoperability-plugins skill-tools-install test-interoperability-skills test-demo-catalog test-demo-runtimes demo-runtimes-test a2a-tools-install test-interoperability-a2a mcp-tools-install mcp-catalog-tools-install contextforge-tools-install test-interoperability-contextforge test-interoperability-mcp test-interoperability-mcp-catalog interoperability-tools-install test-interoperability test-interoperability-hf test-interoperability-agntcy agntcy-tools-install test-minikube-agntcy gateway-tools-install test-interoperability-gateway ans-tools-install test-interoperability-ans capture-ans-trust-fixtures test-trust-verification,$(MAKECMDGOALS)),)
ifneq ($(words $(MAKECMDGOALS)),1)
$(error Interoperability targets must run alone)
endif
override CATALOG_ENV_FILE := /dev/null
endif
ifneq ($(filter client-go-tools-install client-go-generate client-go-check,$(MAKECMDGOALS)),)
ifneq ($(words $(MAKECMDGOALS)),1)
$(error Go-client compatibility targets must run alone)
endif
override CATALOG_ENV_FILE := /dev/null
endif
ifneq ($(filter sdk-tools-install sdk-generate sdk-check sdk-package sdk-rust-audit-tools-install sdk-rust-audit webhook-report-build webhook-report-check test-webhook-report test-webhook-report-dsn mcp-scanner-receiver-build mcp-scanner-receiver-check test-mcp-scanner-receiver test-mcp-scanner-receiver-dsn,$(MAKECMDGOALS)),)
ifneq ($(words $(MAKECMDGOALS)),1)
$(error SDK targets must run alone)
endif
override CATALOG_ENV_FILE := /dev/null
endif
ifneq ($(filter langgraph-tools-install test-langgraph-unit test-langgraph test-langgraph-model,$(MAKECMDGOALS)),)
ifneq ($(words $(MAKECMDGOALS)),1)
$(error LangGraph installation and verification targets must run alone)
endif
override CATALOG_ENV_FILE := /dev/null
endif
ifneq ($(filter mcp-scanner-tools-install mcp-scanner-scan mcp-scanner-check test-mcp-scanner-compose,$(MAKECMDGOALS)),)
ifneq ($(words $(MAKECMDGOALS)),1)
$(error MCP Scanner targets must run alone)
endif
override CATALOG_ENV_FILE := /dev/null
endif
ifneq ($(filter vscode-tools-install vscode-check vscode-package test-vscode,$(MAKECMDGOALS)),)
ifneq ($(words $(MAKECMDGOALS)),1)
$(error VS Code targets must run alone)
endif
override CATALOG_ENV_FILE := /dev/null
endif
SDK_LANGUAGE ?= all
ISOLATED_API_TARGETS := validation-tools-install api-yaml-check api-validate api-contract-check api-lint api-check api-validation-test api-diff api-breaking-check
ifneq ($(filter $(ISOLATED_API_TARGETS),$(MAKECMDGOALS)),)
ifneq ($(filter-out $(ISOLATED_API_TARGETS),$(MAKECMDGOALS)),)
$(error API validation targets cannot be mixed with other workflows)
endif
ifneq ($(filter validation-tools-install,$(MAKECMDGOALS)),)
ifneq ($(words $(MAKECMDGOALS)),1)
$(error Install validation tools separately from checks)
endif
endif
override CATALOG_ENV_FILE := /dev/null
endif
ifneq ($(filter hooks-install hooks-uninstall pre-commit-check,$(MAKECMDGOALS)),)
ifneq ($(words $(MAKECMDGOALS)),1)
$(error Hook targets must run alone)
endif
override CATALOG_ENV_FILE := /dev/null
endif
ifneq ($(filter embeddings-install test-embeddings,$(MAKECMDGOALS)),)
ifneq ($(words $(MAKECMDGOALS)),1)
$(error Embedding fixture targets must run alone)
endif
override CATALOG_ENV_FILE := /dev/null
endif
ISOLATED_MEASUREMENT_TARGETS := test-load-smoke test-load test-scale test-public-delivery test-soak test-monitoring perf-clean bench benchstat-tools-install bench-compare \
	bench-search bench-search-dsn bench-search-plans bench-search-plans-dsn
ifneq ($(filter $(ISOLATED_MEASUREMENT_TARGETS),$(MAKECMDGOALS)),)
ifneq ($(words $(MAKECMDGOALS)),1)
$(error Isolated measurement targets must run alone)
endif
override CATALOG_ENV_FILE := /dev/null
endif
# The selected file path is literal environment data for every loading target.
# Earlier isolated targets have already overridden it with /dev/null.
ifeq ($(origin CATALOG_ENV_FILE),environment)
CATALOG_ENV_FILE := $(value CATALOG_ENV_FILE)
endif
# Imported launcher values are literal environment data. Freeze them before
# Make can expand dollar signs in paths; trusted workflow files retain normal
# Make syntax and command-line > file > environment precedence.
DEMO_UP_INPUT_VARS := CATALOG_ENV_FILE COMPOSE_PROJECT IMAGE UI_IMAGE GO PYTHON CURL COMPOSE CONTAINER_ENGINE \
	CATALOG_COMPOSE_HTTP_PORT CATALOG_COMPOSE_POSTGRES_PORT CATALOG_COMPOSE_SUBNET \
	CATALOG_UI_PORT CATALOG_UI_LOG_LEVEL CATALOG_UI_LOG_FORMAT CATALOG_UI_DOCUMENTATION_URL \
	LOCAL_UI LOCAL_MONITORING LOCAL_EMBEDDINGS LOCAL_WEBHOOKS LOCAL_DEMO_ARCHIVES LOCAL_DEMO_RUNTIMES \
	EMBEDDING_PROFILE DEMO_RUNTIME_DIR DEMO_A2A_PORT DEMO_MCP_PORT \
	CATALOG_MONITORING_PROMETHEUS_PORT CATALOG_MONITORING_GRAFANA_PORT \
	CATALOG_MONITORING_RETENTION_HOURS CATALOG_MONITORING_RETENTION_MB \
	DEMO_DATA_FILE DEMO_SAMPLE_SET DEMO_STATE_FILE DEMO_BASE_URL DEMO_EXTEND DEMO_TIMEOUT \
	DEMO_ARCHIVE_DIR DEMO_ARCHIVE_ADDRESS DEMO_ARCHIVE_IP DEMO_ARCHIVE_PORT DEMO_ARCHIVE_ORIGIN DEMO_ARCHIVE_TRUST_FILE
DEMO_UP_LAUNCH_TARGETS := demo-up demo-check demo-archives-prepare compose-up demo-seed
ifneq ($(filter $(DEMO_UP_LAUNCH_TARGETS),$(MAKECMDGOALS)),)
$(foreach name,$(DEMO_UP_INPUT_VARS),$(if $(filter environment,$(origin $(name))),$(eval $(name) := $$(value $(name)))))
endif
workflow-empty :=
workflow-space := $(workflow-empty) $(workflow-empty)
workflow-tab := $(workflow-empty)	$(workflow-empty)
ifneq ($(filter $(DEMO_UP_LAUNCH_TARGETS),$(MAKECMDGOALS)),)
$(if $(or $(findstring \$(workflow-space),$(CATALOG_ENV_FILE)),$(findstring \$(workflow-tab),$(CATALOG_ENV_FILE))),$(error A workflow path cannot contain a backslash before whitespace))
endif
# Include expands glob patterns before the recipe can validate the selected
# literal file. Refuse ambiguous selection before any workflow is evaluated.
$(if $(or $(findstring *,$(CATALOG_ENV_FILE)),$(findstring ?,$(CATALOG_ENV_FILE)),$(findstring [,$(CATALOG_ENV_FILE))),$(error A workflow path cannot contain wildcard characters: * ? [))
-include $(subst $(workflow-tab),\$(workflow-tab),$(subst $(workflow-space),\$(workflow-space),$(CATALOG_ENV_FILE)))


# ─────────────────────────────────────────────────────────────────────────────
# Runtime configuration and operator inputs
# ─────────────────────────────────────────────────────────────────────────────
CATALOG_CONFIG_FILE ?= config/local.json
CATALOG_CONFIG_ROLE ?= all
CATALOG_POSTGRES_CONFIG_FILE ?= config/postgres.json
CATALOG_CONTAINER_CONFIG_FILE ?= config/local.json
CATALOG_MIGRATION_DATABASE_DSN ?= postgres://catalog:catalog@127.0.0.1:55432/catalog?sslmode=disable
CATALOG_DATABASE_API_ROLE ?= catalog_api
CATALOG_DATABASE_WORKER_ROLE ?= catalog_worker
REPAIR_TENANT_ID ?=
REPAIR_RESERVATION_ID ?=
REPAIR_REASON ?=
REPAIR_PLAN_DIGEST ?=
REPAIR_LIMIT ?= 50
SEARCH_PROJECTION_REPAIR_LIMIT ?= 500
SEARCH_PROJECTION_REPAIR_OPERATOR_LABEL ?=
RESTORE_TENANT_ID ?=
RESTORE_RECOVERY_POINT ?=
RESTORE_CURSOR ?=
RESTORE_VERIFY_LIMIT ?= 100


# ─────────────────────────────────────────────────────────────────────────────
# Scoped environment allowlists
# ─────────────────────────────────────────────────────────────────────────────
CATALOG_RUNTIME_ENV_VARS := CATALOG_DEMO_ARCHIVES CATALOG_LOG_LEVEL CATALOG_LOG_FORMAT CATALOG_TRACING CATALOG_ENVIRONMENT CATALOG_PROFILE CATALOG_SERVICE_NAME \
	CATALOG_HTTP_SERVICE_ACTIVITY_ENABLED CATALOG_HTTP_ADDRESS CATALOG_PUBLIC_BASE_URL CATALOG_HTTP_READ_TIMEOUT CATALOG_HTTP_REQUEST_TIMEOUT \
	CATALOG_HTTP_WRITE_TIMEOUT CATALOG_HTTP_IDLE_TIMEOUT CATALOG_HTTP_SHUTDOWN_TIMEOUT \
	CATALOG_VALIDATION_MAX_FINDINGS CATALOG_VALIDATION_MAX_DIAGNOSTIC_BYTES \
	CATALOG_MCP_ENABLED CATALOG_MCP_MAX_TOOL_BYTES CATALOG_MCP_MAX_CONCURRENCY CATALOG_MCP_ALLOWED_ORIGINS \
	CATALOG_HTTP_MAX_BODY_BYTES CATALOG_HTTP_CORS_ORIGINS CATALOG_DATABASE_DRIVER \
	CATALOG_DATABASE_DSN CATALOG_DATABASE_DSN_FILE CATALOG_DATABASE_MAX_CONNECTIONS \
	CATALOG_DATABASE_MAX_CONN_LIFETIME CATALOG_DATABASE_MAX_CONN_IDLE_TIME CATALOG_DATABASE_HEALTH_CHECK_PERIOD \
	CATALOG_AUTH_MODE CATALOG_AUTH_ISSUER CATALOG_AUTH_TRUST_BUNDLE_FILE \
	CATALOG_AUTH_AUDIENCE CATALOG_AUTH_TENANCY_MODE CATALOG_AUTH_DEDICATED_TENANT_ID \
	CATALOG_AUTH_ORGANIZATION_CLAIM CATALOG_AUTH_ENFORCE_SCOPES CATALOG_JOBS_MAX_ATTEMPTS \
	CATALOG_JOBS_RETRY_CEILING CATALOG_WORKER_POLL_INTERVAL \
	CATALOG_WORKER_DEPENDENCY_RETRY_INITIAL_DELAY CATALOG_WORKER_DEPENDENCY_RETRY_CEILING \
	CATALOG_WORKER_LEASE_DURATION CATALOG_WORKER_CONCURRENCY CATALOG_WORKER_SOURCE_MAINTENANCE_INTERVAL \
	CATALOG_WORKER_HOUSEKEEPING_INTERVAL CATALOG_WORKER_TERMINAL_JOB_RETENTION \
	CATALOG_WORKER_ACKNOWLEDGED_OUTBOX_RETENTION \
	CATALOG_WORKER_OPERATIONS_ADDRESS CATALOG_FEDERATION_REQUEST_TIMEOUT \
	CATALOG_FEDERATION_MAX_RESPONSE_BYTES CATALOG_FEDERATION_MAX_REDIRECTS \
	CATALOG_FEDERATION_MAX_FANOUT CATALOG_FEDERATION_MAX_HOPS \
	CATALOG_FEDERATION_MAX_LIVE_PAGES_PER_SOURCE \
	CATALOG_FEDERATION_POLICY_FILE CATALOG_DISCOVERY_HOSTS \
	CATALOG_PUBLICATION_ARD_PROFILE CATALOG_PUBLICATION_AI_CATALOG_PROFILE \
	CATALOG_WEBHOOKS_ENABLED CATALOG_WEBHOOKS_MAX_ATTEMPTS CATALOG_WEBHOOKS_TIMEOUT \
	CATALOG_WEBHOOKS_POLL_INTERVAL CATALOG_WEBHOOKS_RETRY_CEILING CATALOG_WEBHOOKS_POLICY_FILE CATALOG_SEMANTIC_SEARCH CATALOG_RATINGS_SHARING_ENABLED CATALOG_RATINGS_MINIMUM_SHARED_COUNT CATALOG_LISTING_MEDIA CATALOG_TRUST_VERIFICATION CATALOG_PUBLISHER_DOMAIN_VERIFICATION \
	CATALOG_MANAGED_MEDIA_ENABLED CATALOG_SOFTWARE_ASSESSMENT_ENABLED \
	CATALOG_CREDENTIAL_RESOLUTION_ENABLED CATALOG_CREDENTIAL_RESOLUTION_DIRECTORY \
	CATALOG_WEBHOOK_ENCRYPTION_KEY \
	CATALOG_WEBHOOK_ENCRYPTION_KEY_FILE CATALOG_CURSOR_SIGNING_KEY \
	CATALOG_CURSOR_SIGNING_KEY_FILE
CATALOG_RUNTIME_ENV_TARGETS := run config-check env-check diagnostics compose-up
CATALOG_DATABASE_OPERATOR_TARGETS := migrate-plan migrate-apply migrate-semantic-plan migrate-semantic-apply \
	repair-idempotency-list repair-idempotency-plan repair-idempotency-apply repair-search-projections restore-verify
CATALOG_DATABASE_POOL_ENV_VARS := CATALOG_DATABASE_MAX_CONNECTIONS CATALOG_DATABASE_MAX_CONN_LIFETIME \
	CATALOG_DATABASE_MAX_CONN_IDLE_TIME CATALOG_DATABASE_HEALTH_CHECK_PERIOD
ifneq ($(filter $(CATALOG_RUNTIME_ENV_TARGETS),$(MAKECMDGOALS)),)
$(foreach name,CATALOG_OBJECT_DRIVER CATALOG_OBJECT_DIRECTORY CATALOG_OBJECT_ENDPOINT CATALOG_OBJECT_BUCKET CATALOG_OBJECT_REGION,$(if $(filter undefined,$(origin $(name))),,$(error $(name) is no longer supported; remove obsolete object settings)))
endif
CATALOG_COMPOSE_ENV_VARS := CATALOG_COMPOSE_HTTP_PORT CATALOG_COMPOSE_POSTGRES_PORT \
	CATALOG_COMPOSE_SUBNET \
	CATALOG_COMPOSE_FEDERATION_POLICY_FILE CATALOG_COMPOSE_FEDERATION_CREDENTIALS_DIRECTORY CATALOG_COMPOSE_WEBHOOK_DIRECTORY \
	CATALOG_COMPOSE_FEDERATION_TRUST_DIRECTORY CATALOG_COMPOSE_TRUST_VERIFICATION_DIRECTORY
CATALOG_POSTGRES_TEST_ENV_VARS := CATALOG_TEST_DATABASE_DSN
CATALOG_POSTGRES_TEST_ENV_TARGETS := test-integration-dsn coverage-postgres-dsn-profile coverage-postgres-dsn-check bench-search-dsn bench-search-plans-dsn test-process-postgres-dsn coverage-process-postgres-dsn-profile test-webhook-report-dsn test-mcp-scanner-receiver-dsn
CATALOG_SMOKE_ENV_VARS := CATALOG_SMOKE_BASE_URL CATALOG_SMOKE_ADMIN_TOKEN \
	CATALOG_SMOKE_CONTRIBUTOR_TOKEN CATALOG_SMOKE_REVIEWER_TOKEN \
	CATALOG_SMOKE_PUBLISHER_TOKEN CATALOG_SMOKE_READER_TOKEN \
	CATALOG_SMOKE_OTHER_TENANT_TOKEN

ifneq ($(filter $(DEMO_UP_LAUNCH_TARGETS),$(MAKECMDGOALS)),)
$(foreach name,$(CATALOG_RUNTIME_ENV_VARS) $(CATALOG_COMPOSE_ENV_VARS),$(if $(filter environment,$(origin $(name))),$(eval $(name) := $$(value $(name)))))
endif
# An unexport declaration defines an absent Make variable with an empty value.
# Remember actual caller/file definitions before those declarations so a child
# receives explicit empty inputs, but not manufactured empty runtime settings.
DEMO_UP_DEFINED_RUNTIME_VARS := $(foreach name,$(CATALOG_RUNTIME_ENV_VARS) $(CATALOG_COMPOSE_ENV_VARS),$(if $(filter undefined,$(origin $(name))),,$(name)))


# ─────────────────────────────────────────────────────────────────────────────
# Environment export boundaries - preserve definition and evaluation order
# ─────────────────────────────────────────────────────────────────────────────
define export_to_targets_if_defined
ifneq ($(origin $(2)),undefined)
$(1): export $(2) := $$($(2))
endif
endef
# Export only each launcher's documented inputs. Ordinary checks and unrelated
# recipes must not inherit runtime credentials just because Make loaded a file.
$(foreach name,$(CATALOG_RUNTIME_ENV_VARS),$(eval $(call export_to_targets_if_defined,$(CATALOG_RUNTIME_ENV_TARGETS),$(name))))
$(foreach name,$(CATALOG_DATABASE_POOL_ENV_VARS),$(eval $(call export_to_targets_if_defined,$(CATALOG_DATABASE_OPERATOR_TARGETS),$(name))))
unexport MCPGODEBUG
unexport $(CATALOG_RUNTIME_ENV_VARS)
$(foreach name,$(CATALOG_COMPOSE_ENV_VARS),$(eval $(call export_to_targets_if_defined,compose-up,$(name))))
# Fixture maintenance needs the selected address boundary, never runtime credentials.
$(eval $(call export_to_targets_if_defined,compose-stop compose-down compose-reset compose-ps compose-logs,CATALOG_COMPOSE_SUBNET))
unexport $(CATALOG_COMPOSE_ENV_VARS)
$(foreach name,$(CATALOG_POSTGRES_TEST_ENV_VARS),$(eval $(call export_to_targets_if_defined,$(CATALOG_POSTGRES_TEST_ENV_TARGETS),$(name))))
unexport $(CATALOG_POSTGRES_TEST_ENV_VARS)
$(foreach name,$(CATALOG_SMOKE_ENV_VARS),$(eval $(call export_to_targets_if_defined,smoke,$(name))))
unexport $(CATALOG_SMOKE_ENV_VARS)
unexport CATALOG_ENV_FILE CATALOG_CONFIG_FILE CATALOG_CONFIG_ROLE CATALOG_POSTGRES_CONFIG_FILE
unexport CATALOG_CONTAINER_CONFIG_FILE CATALOG_MIGRATION_DATABASE_DSN
unexport CATALOG_DATABASE_API_ROLE CATALOG_DATABASE_WORKER_ROLE
unexport REPAIR_TENANT_ID REPAIR_RESERVATION_ID REPAIR_REASON REPAIR_PLAN_DIGEST REPAIR_LIMIT
unexport SEARCH_PROJECTION_REPAIR_LIMIT SEARCH_PROJECTION_REPAIR_OPERATOR_LABEL
unexport RESTORE_TENANT_ID RESTORE_RECOVERY_POINT RESTORE_CURSOR RESTORE_VERIFY_LIMIT
unexport MINIKUBE_PRELOAD_ARCHIVE MINIKUBE_TIMEOUT
unexport CATALOG_CLIENT_SKILL_SESSION_DIR
unexport MONITORING_CONTEXT MONITORING_NAMESPACE MONITORING_RELEASE MONITORING_VALUES MONITORING_COMPONENT MONITORING_PORT


# ─────────────────────────────────────────────────────────────────────────────
# Tool commands
# ─────────────────────────────────────────────────────────────────────────────
GO := go
PYTHON := $(if $(wildcard .tools/dev/bin/python),'$(subst ','"'"',$(CURDIR)/.tools/dev/bin/python)',python3)
RUFF ?= $(if $(wildcard .tools/dev/bin/ruff),$(CURDIR)/.tools/dev/bin/ruff,ruff)
UV ?= uv
DEV_PYTHON ?= python3
BUMPVERSION ?= $(CURDIR)/.tools/dev/bin/bump-my-version
PART ?= patch
GOCOV ?= gocov
GOCOV_HTML ?= gocov-html
CURL := curl
HELM := helm
MINIKUBE := minikube
KUBECTL := kubectl
TRIVY := trivy
HADOLINT := hadolint
SHELLCHECK := shellcheck
MARKDOWNLINT := markdownlint
YAMLLINT := yamllint

# ─────────────────────────────────────────────────────────────────────────────
# Local deployment and measurement defaults
# ─────────────────────────────────────────────────────────────────────────────
CONTAINER_ENGINE ?= docker
COMPOSE ?= docker compose
COMPOSE_PROJECT ?= open-agentic-catalog
LOCAL_MONITORING ?= off
LOCAL_UI ?= off
LOCAL_EMBEDDINGS ?= off
LOCAL_DEMO_ARCHIVES ?= off
LOCAL_DEMO_RUNTIMES ?= off
DEMO_RUNTIME_DIR ?= output/demo/runtimes
DEMO_A2A_PORT ?= 19443
DEMO_MCP_PORT ?= 19444
unexport LOCAL_DEMO_RUNTIMES DEMO_RUNTIME_DIR DEMO_A2A_PORT DEMO_MCP_PORT
LOCAL_WEBHOOKS ?= on
UI_IMAGE ?= open-agentic-catalog-ui:dev
CATALOG_UI_PORT ?= 4180
CATALOG_UI_LOG_LEVEL ?= info
CATALOG_UI_LOG_FORMAT ?= text
CATALOG_UI_DOCUMENTATION_URL ?=
UI_DOCUMENTATION_DIRECTORY ?=
ui-dev compose-up: export CATALOG_UI_DOCUMENTATION_URL := $(CATALOG_UI_DOCUMENTATION_URL)
ui-dev: export UI_DOCUMENTATION_DIRECTORY := $(UI_DOCUMENTATION_DIRECTORY)
ui-dev compose-up: export CATALOG_UI_LOG_LEVEL := $(CATALOG_UI_LOG_LEVEL)
ui-dev compose-up: export CATALOG_UI_LOG_FORMAT := $(CATALOG_UI_LOG_FORMAT)
PERF_RUN_ID ?=
PERF_DURATION_SECONDS ?=
PERF_WARMUP_SECONDS ?= 5
PERF_WORKERS ?= 2
PERF_ITEMS ?= 1000
PERF_SQL_PLANS ?= off
PERF_SCALE_WORKLOAD ?= standard
test-scale: export PERF_ITEMS := $(PERF_ITEMS)
test-scale: export PERF_SQL_PLANS := $(PERF_SQL_PLANS)
test-scale: export PERF_SCALE_WORKLOAD := $(PERF_SCALE_WORKLOAD)
unexport PERF_ITEMS PERF_SQL_PLANS PERF_SCALE_WORKLOAD
CATALOG_MONITORING_PROMETHEUS_PORT ?= 19090
CATALOG_MONITORING_GRAFANA_PORT ?= 13000
CATALOG_MONITORING_RETENTION_HOURS ?= 6
CATALOG_MONITORING_RETENTION_MB ?= 512
LOCAL_MONITORING_ENV_VARS := CATALOG_MONITORING_PROMETHEUS_PORT CATALOG_MONITORING_GRAFANA_PORT \
	CATALOG_MONITORING_RETENTION_HOURS CATALOG_MONITORING_RETENTION_MB
LOCAL_MONITORING_TARGETS := compose-check compose-up compose-stop compose-down compose-reset compose-ps compose-logs compose-monitoring-reset
$(foreach name,$(LOCAL_MONITORING_ENV_VARS),$(eval $(call export_to_targets_if_defined,$(LOCAL_MONITORING_TARGETS),$(name))))
unexport $(LOCAL_MONITORING_ENV_VARS)
$(foreach name,PERF_DURATION_SECONDS PERF_WARMUP_SECONDS PERF_WORKERS,$(eval $(call export_to_targets_if_defined,test-load-smoke test-load test-scale test-public-delivery test-soak test-monitoring,$(name))))
unexport PERF_DURATION_SECONDS PERF_WARMUP_SECONDS PERF_WORKERS
CONTAINERFILE ?= Containerfile
COMPAT_DOCKERFILE := deployments/container/Dockerfile
FUNCTIONAL_CONTAINERFILE := test/functional/Containerfile
GO_VERSION := $(shell awk '/^go / { print $$2; exit }' go.mod)
GO_TOOLCHAIN := go$(GO_VERSION)
HOST_GOARCH ?= $(shell GOTOOLCHAIN=local $(GO) env GOHOSTARCH)
CONTAINER_PLATFORM ?= linux/$(HOST_GOARCH)
ARTIFACT_OUTPUT ?= output/artifacts
CHART_VERSION ?= 0.1.0
APP_VERSION ?= $(VERSION)


# ─────────────────────────────────────────────────────────────────────────────
# Packaging inputs
# ─────────────────────────────────────────────────────────────────────────────
# Pass packaging inputs as data, not interpolated shell arguments.
ui-container-inspect ui-sbom ui-container-scan container-inspect container-verify container-scan licenses sbom: export PACKAGING_ENGINE = $(CONTAINER_ENGINE)
ui-container-inspect ui-sbom ui-container-scan container-inspect container-verify container-scan licenses sbom: export PACKAGING_GO = $(GO)
ui-container-inspect ui-sbom ui-container-scan container-inspect container-verify container-scan licenses sbom: export PACKAGING_TRIVY = $(TRIVY)
container-inspect container-verify container-scan licenses sbom: export PACKAGING_IMAGE = $(IMAGE)
ui-container-inspect ui-sbom ui-container-scan: export PACKAGING_IMAGE = $(UI_IMAGE)
ui-container-inspect ui-sbom ui-container-scan container-inspect container-verify container-scan licenses sbom: export PACKAGING_PLATFORM = $(CONTAINER_PLATFORM)
ui-container-inspect ui-sbom ui-container-scan container-inspect container-verify container-scan licenses sbom: export PACKAGING_VERSION = $(VERSION)
ui-container-inspect ui-sbom ui-container-scan container-inspect container-verify container-scan licenses sbom: export PACKAGING_COMMIT = $(COMMIT)
ui-container-inspect ui-sbom ui-container-scan container-inspect container-verify container-scan licenses sbom: export PACKAGING_SOURCE_STATE = $(SOURCE_STATE)
ui-container-inspect ui-sbom ui-container-scan container-inspect container-verify container-scan licenses sbom helm-package: export PACKAGING_OUTPUT = $(ARTIFACT_OUTPUT)
helm-package: export PACKAGING_HELM = $(HELM)
helm-package: export PACKAGING_CHART_VERSION = $(CHART_VERSION)
helm-package: export PACKAGING_APP_VERSION = $(APP_VERSION)
helm-package: export PACKAGING_KUBE_VERSION = $(HELM_KUBE_VERSION)


# ─────────────────────────────────────────────────────────────────────────────
# Go source selection and fixed test environment
# ─────────────────────────────────────────────────────────────────────────────
MODULE_PATH := $(shell awk '/^module / { print $$2; exit }' go.mod)
GO_MODULE_CACHE ?= $(shell GOTOOLCHAIN=local $(GO) env GOMODCACHE)
OFFLINE_GO_PROXY ?= file://$(GO_MODULE_CACHE)/cache/download
GOVULNCHECK_DATABASE ?=
# Keep npm dependencies and local artifacts outside Go lint/test/coverage scans.
GO_SOURCE_DIRS := api cmd examples internal migrations schemas samples test catalog-ui/cmd catalog-ui/internal
GO_SOURCE_PACKAGES := $(addsuffix /...,$(addprefix ./,$(GO_SOURCE_DIRS)))
# Instrument application code, while still executing example and harness units.
GO_APPLICATION_PACKAGES := $(addsuffix /...,$(addprefix ./,$(filter-out examples test,$(GO_SOURCE_DIRS))))
# Fixed selections must not inherit user flags that skip tests, enable an
# external fixture, or redirect discovery into another workspace.
GO_TEST_TARGETS := test test-functional-local test-unit coverage-unit coverage-full test-tooling-unit test-mutation test-property test-race test-shuffle \
	coverage-profile coverage-postgres-dsn-profile test-integration-dsn test-process-postgres-dsn coverage-process-postgres-dsn-profile test-fuzz \
	test-e2e embeddings-check api-contract-check api-validate client-skill-check \
	conformance-fixture ui-test ui-test-unit docs-check architecture-check lint license-check \
	test-integration-compile test-deployment-compile helm-test test-minikube-contract dead-code-report dead-code-check
# An empty GOFLAGS falls back to persisted `go env -w` settings. Use a fixed,
# nonempty value without disabling configured toolchains, proxies or caches.
$(GO_TEST_TARGETS): override export GOFLAGS := -mod=readonly
$(GO_TEST_TARGETS): override export GOWORK := off
# Python helpers also run inside the broad Go wrappers; keep their assertions.
$(GO_TEST_TARGETS): override export PYTHONOPTIMIZE := 0
$(GO_TEST_TARGETS): override export CATALOG_PROCESS_COVERAGE_DIR :=
# Ordinary complete suites use bounded, reproducible Rapid checks. The focused
# launcher validates PROPERTY_* exploration settings and supplies its own values.
unexport $(filter RAPID_%,$(.VARIABLES))
$(GO_TEST_TARGETS): override export RAPID_CHECKS := 100
$(GO_TEST_TARGETS): override export RAPID_SEED := 1
$(GO_TEST_TARGETS): override export RAPID_SHRINKTIME := 1s
$(GO_TEST_TARGETS): override export RAPID_NOFAILFILE := 1
$(GO_TEST_TARGETS): override export RAPID_FAILFILE :=
$(GO_TEST_TARGETS): override export RAPID_STEPS := 30
$(GO_TEST_TARGETS): override export RAPID_LOG := false
$(GO_TEST_TARGETS): override export RAPID_V := false
$(GO_TEST_TARGETS): override export RAPID_DEBUG := false
$(GO_TEST_TARGETS): override export RAPID_DEBUGVIS := false
# Format only owned source roots. Prune npm packages, immutable upstream inputs
# and generated protocol bindings; find -exec preserves whitespace in filenames.
GO_FORMAT_FIND := find $(GO_SOURCE_DIRS) \
	\( -name node_modules -o -path internal/a2apb -o -path schemas/upstream \) -prune -o -type f -name '*.go'

# ─────────────────────────────────────────────────────────────────────────────
# Pinned analyzers and deployment tools
# ─────────────────────────────────────────────────────────────────────────────
STATICCHECK_VERSION := v0.8.2
DEADCODE_VERSION := v0.51.0
GOLANGCI_LINT_VERSION := v2.15.0
GOVULNCHECK_VERSION := v1.8.0
GO_LICENSES_VERSION := v1.6.0
GO_LICENSE_ALLOWLIST := Apache-2.0,MIT,BSD-2-Clause,BSD-3-Clause
KUBECONFORM_VERSION := v0.8.0
KUBECONFORM_IMAGE := ghcr.io/yannh/kubeconform:$(KUBECONFORM_VERSION)@sha256:faffaf43f95aa6425306e1ab8d6fcad72acb9049158f38e574c085ea1ec0f64e
HELM_CHART := deployments/helm/open-agentic-catalog
HELM_KUBE_VERSION := 1.34.0
HELM_CHECK_VALUES := $(HELM_CHART)/ci/shared-values.yaml $(HELM_CHART)/ci/optional-values.yaml $(HELM_CHART)/ci/ambiguous-values.yaml $(HELM_CHART)/ci/ui-values.yaml $(HELM_CHART)/ci/mcp-values.yaml $(HELM_CHART)/ci/semantic-values.yaml $(HELM_CHART)/ci/pgbouncer.yaml $(HELM_CHART)/ci/payload-values.yaml
MINIKUBE_VERSION := v1.39.0
MINIKUBE_KUBERNETES_VERSION := v1.35.9
MINIKUBE_DRIVER := docker
MINIKUBE_CONTAINER_RUNTIME := containerd
MINIKUBE_BASE_IMAGE := gcr.io/k8s-minikube/kicbase:v0.0.51@sha256:4a1c825b61479e6c898851ea66f13c620aaeab6002746e95067fc2c4b38a0b24
MINIKUBE_POSTGRES_IMAGE := pgvector/pgvector:pg18@sha256:2358fcba361ed2233a5ed81b5fe4ca779ccb304120ce531a3bf51c0ed7e2bc11
MINIKUBE_PRELOAD_ARCHIVE ?=
MINIKUBE_TIMEOUT ?=
MONITORING_CONTEXT ?=
MONITORING_NAMESPACE ?=
MONITORING_RELEASE ?= catalog-monitoring
MONITORING_VALUES ?=
MONITORING_COMPONENT ?= grafana
MONITORING_PORT ?= 13000

# ─────────────────────────────────────────────────────────────────────────────
# Coverage gates
# ─────────────────────────────────────────────────────────────────────────────
UNIT_COVERAGE_MIN ?= 90
UNIT_CRITICAL_PACKAGE_COVERAGE_MIN ?= 90
FULL_COVERAGE_MIN ?= 95
COVERAGE_MIN ?= 90
CORE_COVERAGE_MIN ?= 93
CRITICAL_PACKAGE_COVERAGE_MIN ?= 90
POSTGRES_COVERAGE_MIN ?= 95
CRITICAL_COVERAGE_PACKAGES := $(MODULE_PATH)/internal/adapter \
	$(MODULE_PATH)/internal/audit \
	$(MODULE_PATH)/internal/catalog \
	$(MODULE_PATH)/internal/draftfile \
	$(MODULE_PATH)/internal/events \
	$(MODULE_PATH)/internal/federation \
	$(MODULE_PATH)/internal/identity \
	$(MODULE_PATH)/internal/jobs \
	$(MODULE_PATH)/internal/tenantsettings \
	$(MODULE_PATH)/internal/jsondoc \
	$(MODULE_PATH)/internal/platform/authn \
	$(MODULE_PATH)/internal/platform/config \
	$(MODULE_PATH)/internal/platform/fetcher \
	$(MODULE_PATH)/internal/platform/httpserver \
	$(MODULE_PATH)/internal/platform/idempotency \
	$(MODULE_PATH)/internal/platform/webhooksender \
	$(MODULE_PATH)/internal/publication \
	$(MODULE_PATH)/internal/standards \
	$(MODULE_PATH)/internal/webhook \
	$(MODULE_PATH)/schemas

# ─────────────────────────────────────────────────────────────────────────────
# Build metadata and conformance inputs
# ─────────────────────────────────────────────────────────────────────────────
FUZZ_TIME ?= 10s
# Quote untrusted override values before shell use; the helper validates identifiers
# before they can enter linker flags, labels or launcher environments.
define build-newline


endef
$(if $(findstring $(build-newline),$(value VERSION)),$(error VERSION must not contain newlines))
$(if $(findstring $(build-newline),$(value COMMIT)),$(error COMMIT must not contain newlines))
shell-quote = '$(subst ','"'"',$(1))'
VERSION_INPUT := $(if $(filter undefined,$(origin VERSION)),,--version $(call shell-quote,$(value VERSION)))
COMMIT_INPUT := $(if $(filter undefined,$(origin COMMIT)),,--commit $(call shell-quote,$(value COMMIT)))
# Carry explicit build inputs, not a resolved identity, into the Compose child.
# The child keeps versioning.py's validation and derives SOURCE_STATE itself.
ifneq ($(origin VERSION),undefined)
DEMO_UP_VERSION_INPUT := $(value VERSION)
demo-up-compose: private override export VERSION := $(DEMO_UP_VERSION_INPUT)
endif
ifneq ($(origin COMMIT),undefined)
DEMO_UP_COMMIT_INPUT := $(value COMMIT)
demo-up-compose: private override export COMMIT := $(DEMO_UP_COMMIT_INPUT)
endif
# Lazy capture keeps help and isolated-unit selection free of Git subprocesses.
BUILD_IDENTITY = $(eval BUILD_IDENTITY := $(shell $(PYTHON) -B test/packaging/versioning.py tokens $(VERSION_INPUT) $(COMMIT_INPUT) || printf INVALID))$(if $(filter INVALID,$(BUILD_IDENTITY)),$(error Invalid build identity))$(BUILD_IDENTITY)
override VERSION = $(word 1,$(BUILD_IDENTITY))
override COMMIT = $(word 2,$(BUILD_IDENTITY))
override SOURCE_STATE = $(word 3,$(BUILD_IDENTITY))
IMAGE ?= open-agentic-catalog:dev
unexport VERSION COMMIT SOURCE_STATE IMAGE COMPOSE_PROJECT
ARD_MANIFEST_URL ?= http://127.0.0.1:8080/.well-known/ard.json
ARD_PUBLISHER_URL ?= http://127.0.0.1:8080
ARD_REGISTRY_URL ?= http://127.0.0.1:8080/ard/v1
ARD_CONFORMANCE := test/packaging/ard_conformance.py
GO_BUILD_FLAGS := -mod=readonly -trimpath -buildvcs=false -tags=netgo,osusergo
LDFLAGS = -s -w -buildid= -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.sourceState=$(SOURCE_STATE)

# ─────────────────────────────────────────────────────────────────────────────
# Embedding fixture selection - define before Compose captures scoped exports
# ─────────────────────────────────────────────────────────────────────────────
EMBEDDING_PROFILE ?= minilm-l6-v1
ifneq ($(filter embeddings-install test-embeddings,$(MAKECMDGOALS)),)
ifneq ($(filter-out minilm-l6-v1 bge-small-v1,$(EMBEDDING_PROFILE)),)
$(error EMBEDDING_PROFILE must be minilm-l6-v1 or bge-small-v1)
endif
ifneq ($(words $(EMBEDDING_PROFILE)),1)
$(error EMBEDDING_PROFILE must select exactly one profile)
endif
endif

.DEFAULT_GOAL := help


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
##@ 🧭 Help and aggregate workflows
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

.PHONY: help check test test-all verify

help: ## List the supported development commands
	@awk 'BEGIN {FS = ":.*##"; printf "📚 Open Agentic Catalog\nUsage: make <target>\n"} \
		/^##@ / {heading = substr($$0, 5); next} \
		/^[a-zA-Z0-9_-]+:.*##/ { \
			if (heading != "") {printf "\n%s\n", heading; heading = ""} \
			description = $$2; sub(/^[ \t]+/, "", description); \
			printf "  %-40s %s\n", $$1, description \
		}' $(MAKEFILE_LIST)

check: lint docs-check yaml-check container-check compose-check api-check python-lint json-check version-check workflow-check ## Run source, API and repository linters (Ruff, validation and workflow tools must be installed)

test: test-functional-local ## Run the broad local Go and tooling regression suite

test-all: lint test-race coverage-check coverage-core-check profiles conformance-fixture binary-check ## Run service-free Go lint, race, coverage, conformance and binary checks

verify: test-all ## Alias for test-all (Go gate; not all repository checks)


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
##@ 🛠️ Build and local development
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

.PHONY: build binary-check run diagnostics config-check env-check smoke demo-seed
.PHONY: dev-tools-install version version-check version-bump version-bump-dry-run release-check

dev-tools-install: ## Install the locked shared Python dev tools into .tools/dev (uv 0.13.4 required)
	python3 -B test/packaging/versioning.py install --uv "$(UV)" --python "$(DEV_PYTHON)"

version: ## Show the effective application build version, full commit and source state
	@printf '%s\n' '{"version":"$(VERSION)","commit":"$(COMMIT)","sourceState":"$(SOURCE_STATE)"}'

version-check: ## Check VERSION, OpenAPI, package metadata, tool pins and bump configuration without mutation
	$(PYTHON) -B test/packaging/versioning.py check

version-bump-dry-run: ## Preview VERSION and derived metadata edits (PART=major|minor|patch|rc|final)
	$(PYTHON) -B test/packaging/versioning.py preview --bump $(call shell-quote,$(BUMPVERSION)) --part $(call shell-quote,$(PART))

version-bump: ## Update VERSION and derived metadata in a clean checkout; never commit, tag or push
	$(PYTHON) -B test/packaging/versioning.py bump --bump $(call shell-quote,$(BUMPVERSION)) --part $(call shell-quote,$(PART))

release-check: ## Require clean source and the exact matching v<VERSION> tag; never publish
	$(PYTHON) -B test/packaging/versioning.py release $(VERSION_INPUT) $(COMMIT_INPUT)

build: ## Build versioned catalogd and catalogctl binaries under bin/
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build $(GO_BUILD_FLAGS) -ldflags '$(LDFLAGS)' -o bin/catalogd ./cmd/catalogd
	CGO_ENABLED=0 $(GO) build $(GO_BUILD_FLAGS) -ldflags '$(LDFLAGS)' -o bin/catalogctl ./cmd/catalogctl

binary-check: build ## Check pure-Go metadata, embedded roots, removed build IDs and stripped symbols
	@set -eu; for binary in bin/catalogd bin/catalogctl; do \
		metadata="$$( $(GO) version -m "$$binary" )"; \
		printf '%s\n' "$$metadata" | grep -F 'CGO_ENABLED=0' >/dev/null || { printf '%s\n' "$$binary was not built with CGO_ENABLED=0." >&2; exit 1; }; \
		printf '%s\n' "$$metadata" | grep -F -- '-trimpath=true' >/dev/null || { printf '%s\n' "$$binary was not built with -trimpath." >&2; exit 1; }; \
		printf '%s\n' "$$metadata" | grep -F 'golang.org/x/crypto/x509roots/fallback' >/dev/null || { printf '%s\n' "$$binary does not embed the fallback X.509 roots." >&2; exit 1; }; \
		build_id="$$( $(GO) tool buildid "$$binary" )"; \
		test -z "$$build_id" || { printf '%s\n' "$$binary retains a Go linker build ID." >&2; exit 1; }; \
		symbol_status=0; \
		symbols="$$( $(GO) tool nm "$$binary" 2>&1 )" || symbol_status=$$?; \
		if [ "$$symbol_status" -eq 0 ]; then printf '%s\n' "$$binary retains a symbol table." >&2; exit 1; fi; \
		if [ "$$symbol_status" -ne 1 ] || ! printf '%s\n' "$$symbols" | grep -Fx "reading $$binary: no symbols" >/dev/null; then printf '%s\n' "Unable to verify stripped symbols for $$binary:" "$$symbols" >&2; exit 1; fi; \
	done

run: ## Run API and workers together with the in-memory local profile
	$(GO) run ./cmd/catalogd all --config "$(CATALOG_CONFIG_FILE)"

diagnostics: ## Open local role dependencies and print safe-field runtime diagnostics
	$(GO) run ./cmd/catalogctl diagnostics --config "$(CATALOG_CONFIG_FILE)"

config-check: ## Statically validate the selected profile, environment overrides, and required keys
	$(GO) run ./cmd/catalogctl config validate --config "$(CATALOG_CONFIG_FILE)" --role "$(CATALOG_CONFIG_ROLE)"

env-check: config-check ## Compatibility alias for static configuration validation

smoke: ## Exercise a running service through the complete governed local walkthrough
	$(GO) run ./cmd/catalogctl smoke

DEMO_DATA_FILE ?= samples/catalog.yaml
unexport DEMO_DATA_FILE
DEMO_ARCHIVE_DIR ?= output/demo/archives
DEMO_ARCHIVE_ADDRESS ?= 127.0.0.1:18443
DEMO_ARCHIVE_IP ?= 172.30.255.41
DEMO_ARCHIVE_PORT ?= 18443
unexport DEMO_ARCHIVE_DIR DEMO_ARCHIVE_ADDRESS DEMO_ARCHIVE_IP DEMO_ARCHIVE_PORT LOCAL_DEMO_ARCHIVES
.PHONY: demo-check demo-archives-prepare demo-archives
DEMO_BASE_URL ?= http://127.0.0.1:$(if $(CATALOG_COMPOSE_HTTP_PORT),$(CATALOG_COMPOSE_HTTP_PORT),8080)
DEMO_STATE_FILE ?= output/demo/catalog-demo-v1.json
DEMO_ARCHIVE_ORIGIN ?= https://$(DEMO_ARCHIVE_IP):8443
DEMO_EXTEND ?= false
DEMO_SAMPLE_SET ?= curated
DEMO_TIMEOUT ?= 5m
unexport DEMO_BASE_URL DEMO_STATE_FILE DEMO_ARCHIVE_ORIGIN DEMO_EXTEND DEMO_TIMEOUT DEMO_SAMPLE_SET
demo-seed: export DEMO_BASE_URL := $(DEMO_BASE_URL)
demo-seed: export DEMO_STATE_FILE := $(DEMO_STATE_FILE)
demo-seed: export DEMO_ARCHIVE_ORIGIN := $(DEMO_ARCHIVE_ORIGIN)
demo-seed: export DEMO_SAMPLE_SET := $(DEMO_SAMPLE_SET)
demo-seed: export DEMO_EXTEND := $(DEMO_EXTEND)
demo-seed: export DEMO_TIMEOUT := $(DEMO_TIMEOUT)

DEMO_ARCHIVE_TRUST_FILE ?= $(DEMO_ARCHIVE_DIR)/cert.pem
unexport DEMO_ARCHIVE_TRUST_FILE
demo-seed: export DEMO_ARCHIVE_TRUST_FILE := $(DEMO_ARCHIVE_TRUST_FILE)
demo-seed: export DEMO_DATA_FILE := $(DEMO_DATA_FILE)
demo-seed: ## Load/verify YAML samples through APIs; explicitly add keys/adopt N-88 with DEMO_EXTEND=true
	$(GO) run ./cmd/catalogctl seed-demo --data-file "$${DEMO_DATA_FILE}" --archive-trust-file "$${DEMO_ARCHIVE_TRUST_FILE}" --sample-set "$${DEMO_SAMPLE_SET}" --base-url "$${DEMO_BASE_URL}" --state-file "$${DEMO_STATE_FILE}" --archive-origin "$${DEMO_ARCHIVE_ORIGIN}" --extend="$${DEMO_EXTEND}" --timeout "$${DEMO_TIMEOUT}"



demo-check demo-archives-prepare: export DEMO_STATE_FILE := $(DEMO_STATE_FILE)
demo-check demo-archives-prepare: export DEMO_DATA_FILE := $(DEMO_DATA_FILE)
demo-check demo-archives-prepare: export DEMO_SAMPLE_SET := $(DEMO_SAMPLE_SET)
demo-check demo-archives-prepare: export DEMO_ARCHIVE_ORIGIN := $(DEMO_ARCHIVE_ORIGIN)
demo-check: ## Validate YAML/assets and preview the selected sample inventory locally (no API writes)
	$(GO) run ./test/demo --check --state-file "$${DEMO_STATE_FILE}" --data-file "$${DEMO_DATA_FILE}" --sample-set "$${DEMO_SAMPLE_SET}" --archive-origin "$${DEMO_ARCHIVE_ORIGIN}"

demo-archives-prepare demo-archives: export DEMO_ARCHIVE_DIR := $(DEMO_ARCHIVE_DIR)
demo-archives-prepare demo-archives: export DEMO_ARCHIVE_ADDRESS := $(DEMO_ARCHIVE_ADDRESS)
demo-archives-prepare demo-archives: export DEMO_ARCHIVE_IP := $(DEMO_ARCHIVE_IP)

demo-archives-prepare: ## Prepare YAML-selected immutable archives, exact allowlist, private TLS and fixture binary
	$(GO) run ./test/demo --directory "$${DEMO_ARCHIVE_DIR}" --fixture-ip "$${DEMO_ARCHIVE_IP}" --prepare --state-file "$${DEMO_STATE_FILE}" --data-file "$${DEMO_DATA_FILE}" --sample-set "$${DEMO_SAMPLE_SET}" --archive-origin "$${DEMO_ARCHIVE_ORIGIN}"
	CGO_ENABLED=0 $(GO) build $(GO_BUILD_FLAGS) -o "$${DEMO_ARCHIVE_DIR}/demo-archives" ./test/demo

demo-archives: ## Serve only prepared Plugin archives over local HTTPS until stopped
	$(GO) run ./test/demo --directory "$${DEMO_ARCHIVE_DIR}" --address "$${DEMO_ARCHIVE_ADDRESS}" --fixture-ip "$${DEMO_ARCHIVE_IP}"

.PHONY: demo-runtimes-prepare demo-runtimes-test test-demo-runtimes test-demo-catalog
demo-runtimes-prepare: ## Prepare stable private TLS/key for two locked local peers; install tools explicitly first
	$(PYTHON) test/monitoring/launcher.py prepare-runtimes

demo-runtimes-test: ## Explicitly run real local A2A SendMessage/GetTask and MCP echo clients against selected running peers
	$(PYTHON) test/demo/runtime_clients.py

test-demo-catalog: ## Qualify owned YAML/API/UI loading, extension/recovery and Catalog-selected local clients
	$(PYTHON) test/demo/qualify_catalog.py

test-demo-runtimes: ## Qualify two owned peers with real clients, admission/TLS controls, stop/restart and cleanup
	$(PYTHON) test/demo/qualify_runtimes.py


.PHONY: demo-up demo-up-validate demo-up-check demo-up-archives demo-up-compose demo-up-ready demo-up-seed
DEMO_UP_PHASE_TARGETS := demo-up-validate demo-up-check demo-up-archives demo-up-compose demo-up-ready demo-up-seed
DEMO_UP_PHASE_INPUT_VARS := $(filter-out $(CATALOG_RUNTIME_ENV_VARS) $(CATALOG_COMPOSE_ENV_VARS),$(DEMO_UP_INPUT_VARS)) $(filter $(DEMO_UP_INPUT_VARS),$(DEMO_UP_DEFINED_RUNTIME_VARS))
# Private target exports do not flow into a phase's prerequisites. The existing
# child targets retain their own consumer allowlists. Environment/file credentials
# cross only the Compose phase, outside recursive Make command-line arguments.
$(foreach name,$(DEMO_UP_PHASE_INPUT_VARS),$(if $(filter undefined,$(origin $(name))),,$(eval $(DEMO_UP_PHASE_TARGETS): private export $(name) := $$($(name)))))
$(foreach name,$(DEMO_UP_DEFINED_RUNTIME_VARS),$(eval demo-up-compose: private export $(name) := $$($(name))))
unexport DEMO_COMPOSE_API_URL DEMO_COMPOSE_ARCHIVE_ORIGIN
demo-up-validate: private export DEMO_COMPOSE_API_URL := http://127.0.0.1:$(if $(CATALOG_COMPOSE_HTTP_PORT),$(CATALOG_COMPOSE_HTTP_PORT),8080)
demo-up-validate: private export DEMO_COMPOSE_ARCHIVE_ORIGIN := https://$(DEMO_ARCHIVE_IP):8443
demo-up: demo-up-seed ## Validate/prepare samples, start retained Compose with UI/archives, then seed; no implicit extension

demo-up-validate:
	@test "$$CATALOG_ENV_FILE" = /dev/null || \
		{ test "$$CATALOG_ENV_FILE" = .env && test ! -e "$$CATALOG_ENV_FILE" && test ! -L "$$CATALOG_ENV_FILE"; } || \
		{ test -f "$$CATALOG_ENV_FILE" && test -r "$$CATALOG_ENV_FILE"; } || { echo 'demo-up: selected workflow file is missing, unreadable or not a regular file.' >&2; exit 2; }
	@case "$$DEMO_COMPOSE_API_URL" in *:0) echo "demo-up: select a fixed nonzero Compose API port." >&2; exit 2 ;; esac
	@test "$$DEMO_BASE_URL" = "$$DEMO_COMPOSE_API_URL" && test "$$DEMO_ARCHIVE_ORIGIN" = "$$DEMO_COMPOSE_ARCHIVE_ORIGIN" || { echo 'demo-up: select the matching Compose API port and archive IP; use demo-seed separately for another explicit origin.' >&2; exit 2; }

demo-up-check: demo-up-validate
	@echo "Demo: validate selected YAML/native inputs"
	+$(MAKE) demo-check

demo-up-archives: demo-up-check
	@echo "Demo: prepare immutable local archives and trust"
	+$(MAKE) demo-archives-prepare

demo-up-compose: demo-up-archives
	@echo "Demo: start selected Catalog environment and verify readiness"
	+$(MAKE) compose-up LOCAL_UI=on LOCAL_DEMO_ARCHIVES=on

demo-up-ready: demo-up-compose
	@echo "Demo: wait for the selected API readiness (bounded local probe)"
	@$(CURL) --fail --silent --show-error --retry 30 --retry-delay 1 --retry-max-time 60 --max-time 2 "$$DEMO_BASE_URL/readyz" >/dev/null

demo-up-seed: demo-up-ready
	@echo "Demo: load/verify receipt-owned samples through governed APIs"
	+$(MAKE) demo-seed
	@echo "Sample prerequisites and client instructions: samples/README.md"



# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
##@ 🔍 Code quality and security
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

.PHONY: fmt fmt-check module-check license-check test-integration-compile test-deployment-compile
.PHONY: architecture-check shell-check dead-code-report dead-code-check lint yaml-check json-check python-lint security
.PHONY: vulncheck-offline
.PHONY: workflow-tools-install workflow-check

workflow-tools-install: ## Explicitly acquire the pinned actionlint executable into .tools/ci/bin
	python3 -B test/ci/tools.py actionlint-install

workflow-check: ## Validate GitHub Actions files with installed actionlint (no acquisition)
	$(PYTHON) -B test/ci/tools.py actionlint-check

.PHONY: workflow-local-tools-install workflow-local-list workflow-local-lint workflow-local-unit workflow-local-ui workflow-local-docs workflow-local-charts workflow-local-image workflow-local-security workflow-local-postgres

workflow-local-tools-install: ## Install pinned act and its runner image for local workflow tests
	PYTHONOPTIMIZE=0 python3 -B test/ci/local_workflows.py install

workflow-local-list: ## List fixed local workflow commands without downloads or containers
	PYTHONOPTIMIZE=0 python3 -B test/ci/local_workflows.py list

workflow-local-lint: ## Run the workflow's source lint job locally with act
	PYTHONOPTIMIZE=0 python3 -B test/ci/local_workflows.py run lint

workflow-local-unit: ## Run the workflow's isolated Go and Python units locally with act
	PYTHONOPTIMIZE=0 python3 -B test/ci/local_workflows.py run unit

workflow-local-ui: ## Run the workflow's UI job locally with act
	PYTHONOPTIMIZE=0 python3 -B test/ci/local_workflows.py run ui

workflow-local-docs: ## Run the workflow's documentation job locally with act
	PYTHONOPTIMIZE=0 python3 -B test/ci/local_workflows.py run docs

workflow-local-charts: ## Run the workflow's Helm validation and packaging job locally with act
	PYTHONOPTIMIZE=0 python3 -B test/ci/local_workflows.py run charts

workflow-local-image: ## Build and scan one local workflow image; WORKFLOW_LOCAL_PRODUCT=backend|ui
	PYTHONOPTIMIZE=0 python3 -B test/ci/local_workflows.py run image

workflow-local-security: ## Run the source security workflow locally; preserve advisory failures
	PYTHONOPTIMIZE=0 python3 -B test/ci/local_workflows.py run security

workflow-local-postgres: ## Run the workflow's disposable PostgreSQL coverage job locally with act
	PYTHONOPTIMIZE=0 python3 -B test/ci/local_workflows.py run postgres

fmt: ## Format maintained Go sources, excluding generated and vendored inputs
	$(GO_FORMAT_FIND) -exec gofmt -w {} +

fmt-check: ## Fail on formatting differences, Go parse errors, or traversal errors
	@set -eu; \
		unformatted="$$( $(GO_FORMAT_FIND) -exec gofmt -l {} + )"; \
		test -z "$$unformatted" || { printf '%s\n' 'Go files require formatting; run make fmt.' "$$unformatted" >&2; exit 1; }

module-check: ## Verify downloaded modules and fail on go.mod or go.sum tidy drift
	$(GO) mod verify
	$(PYTHON) -B test/packaging/modules.py --go "$(GO)"

license-check: ## Reject unapproved or unknown licenses in the complete Go dependency closure
	$(PYTHON) -B test/packaging/go_licenses.py --go "$(GO)" --toolchain "$(GO_TOOLCHAIN)" \
		--version "$(GO_LICENSES_VERSION)" --module "$(MODULE_PATH)" --allowed "$(GO_LICENSE_ALLOWLIST)" \
		$(addprefix --source-root ,$(GO_SOURCE_PACKAGES))

test-integration-compile: ## Compile every integration-tag variant without running external tests
	$(GO) test -tags=integration $(GO_SOURCE_PACKAGES) -run '^$$' -count=1

test-deployment-compile: ## Compile the Helm and Minikube harness variants without using Helm or a cluster
	$(GO) test -tags=helm ./test/helm -run '^$$' -count=1
	$(GO) test -tags=minikube ./test/minikube -run '^$$' -count=1
	$(GO) test -tags=minikube,functional ./test/minikube -run '^$$' -count=1

architecture-check: ## Enforce package ownership and outbound HTTP construction boundaries
	$(GO) test ./internal/platform/runtime -run '^TestArchitecture' -count=1

shell-check: ## Lint maintained shell workflows
	$(SHELLCHECK) test/functional/housekeeping.sh test/functional/restore.sh test/functional/pitr_archive.sh deployments/container/licenses.sh test/hooks/pre-commit

dead-code-report: ## Report unreachable Go functions across shipped and test selections
dead-code-check: ## Fail on unreviewed unreachable functions and invalid exceptions
dead-code-report dead-code-check:
	$(PYTHON) -B test/packaging/dead_code.py $(if $(filter dead-code-check,$@),check,report) \
		--go "$(GO)" --toolchain "$(GO_TOOLCHAIN)" --version "$(DEADCODE_VERSION)" --module "$(MODULE_PATH)" \
		$(addprefix --application-root ,$(GO_APPLICATION_PACKAGES)) $(addprefix --source-root ,$(GO_SOURCE_PACKAGES))

lint: fmt-check module-check license-check test-integration-compile test-deployment-compile architecture-check shell-check dead-code-check ## Compile and run vet plus pinned static analyzers
	$(GO) test $(GO_SOURCE_PACKAGES) -run '^$$' -count=1
	$(GO) test -tags=functional $(GO_SOURCE_PACKAGES) -run '^$$' -count=1
	$(GO) vet -tags=functional,integration,helm,minikube $(GO_SOURCE_PACKAGES)
	GOTOOLCHAIN=$(GO_TOOLCHAIN) $(GO) run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) -checks=U1000 -tests=false -tags=netgo,osusergo $(GO_APPLICATION_PACKAGES)
	GOTOOLCHAIN=$(GO_TOOLCHAIN) $(GO) run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) -checks=U1000 -tags=minikube $(GO_SOURCE_PACKAGES)
	GOTOOLCHAIN=$(GO_TOOLCHAIN) $(GO) run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) -tags=functional,integration,helm,minikube $(GO_SOURCE_PACKAGES)
	GOTOOLCHAIN=$(GO_TOOLCHAIN) $(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run --build-tags=functional,integration,helm,minikube $(GO_SOURCE_PACKAGES)

json-check: ## Validate current JSON and JSON-LD syntax, UTF-8 and unique object keys
	$(PYTHON) -B test/packaging/data_checks.py json

yaml-check: api-yaml-check ## Lint current standalone YAML; syntax-check immutable upstream YAML
	$(PYTHON) -B test/packaging/data_checks.py yaml --yamllint "$(YAMLLINT)"

python-lint: ## Check maintained Python with installed Ruff (version pinned in ruff.toml)
	@command -v "$(RUFF)" >/dev/null 2>&1 || { printf '%s\n' 'Ruff is required. Install the version in ruff.toml and set RUFF=/path/to/ruff if needed; see documentation/src/content/docs/contributing/testing.md#python-linting.' >&2; exit 1; }
	"$(RUFF)" check --config ruff.toml --no-cache --no-fix test extensions/vscode/tooling examples/langgraph examples/tutorials/local-oidc examples/mcp-scanner

security: export PACKAGING_TRIVY = $(TRIVY)
security: ## Scan called Go symbols, source dependencies, secrets, and deployment configuration
	GOTOOLCHAIN=$(GO_TOOLCHAIN) $(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) $(GO_SOURCE_PACKAGES)
	# This dependency's upstream build recipe is never built or shipped by Catalog.
	# Ignored tool/research checkouts are not source inputs; schemas/upstream stays in scope.
	$(PYTHON) -B test/packaging/trivy.py
	$(TRIVY) fs --severity HIGH,CRITICAL --exit-code 1 --scanners vuln,secret,misconfig --helm-kube-version "$(HELM_KUBE_VERSION)" --skip-dirs bin --skip-dirs coverage --skip-dirs .tools --skip-dirs '**/.tools' --skip-dirs '**/.venv' --skip-dirs '**/node_modules' --skip-dirs documentation/dist --skip-dirs catalog-ui/dist --skip-dirs output --skip-dirs specs --skip-dirs objects --skip-dirs 'objects-*' --skip-files documentation/node_modules/yaml-language-server/Dockerfile .

.PHONY: sonarqube-tools-install sonarqube-up sonarqube-scan sonarqube-status sonarqube-credentials sonarqube-down
sonarqube-up sonarqube-scan sonarqube-status sonarqube-credentials sonarqube-down: export SONAR_PORT := $(SONAR_PORT)
sonarqube-scan: export SONAR_GO_COVERAGE := $(SONAR_GO_COVERAGE)
sonarqube-scan: export SONAR_JS_COVERAGE := $(SONAR_JS_COVERAGE)
sonarqube-tools-install: ## Pull the pinned local SonarQube Community Build and scanner images
	$(PYTHON) -B test/packaging/sonarqube.py install

sonarqube-up: ## Start the local SonarQube dashboard; SONAR_PORT defaults to 19000
	$(PYTHON) -B test/packaging/sonarqube.py up

sonarqube-scan: ## Ingest current source and optional SONAR_GO_COVERAGE / SONAR_JS_COVERAGE reports
	$(PYTHON) -B test/packaging/sonarqube.py scan

sonarqube-status: ## Check the owned SonarQube server and print its dashboard URL
	$(PYTHON) -B test/packaging/sonarqube.py status

sonarqube-credentials: ## Show the saved local SonarQube URL, username and password (private terminal only)
	@$(PYTHON) -B test/packaging/sonarqube.py credentials

sonarqube-down: ## Stop owned SonarQube containers/network, retaining analysis data and credentials
	$(PYTHON) -B test/packaging/sonarqube.py down

vulncheck-offline: ## Run pinned govulncheck with an operator-supplied local database
	@test -n "$(GOVULNCHECK_DATABASE)" || { printf '%s\n' 'GOVULNCHECK_DATABASE=file:///absolute/path/to/vulndb is required.' >&2; exit 2; }
	@case "$(GOVULNCHECK_DATABASE)" in file:///*) ;; *) printf '%s\n' 'GOVULNCHECK_DATABASE must be an absolute file:// URL.' >&2; exit 2 ;; esac
	@case "$(OFFLINE_GO_PROXY)" in file:///*) ;; *) printf '%s\n' 'OFFLINE_GO_PROXY must be an absolute file:// URL.' >&2; exit 2 ;; esac
	GOTOOLCHAIN=$(GO_TOOLCHAIN) GOPROXY="$(OFFLINE_GO_PROXY)" GOSUMDB=off GOTELEMETRY=off $(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) -db "$(GOVULNCHECK_DATABASE)" $(GO_SOURCE_PACKAGES)


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
##@ 🔬 Formal verification
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

.PHONY: formal-tools-install test-formal
formal-tools-install: ## Install checksum-pinned Gobra/Silicon/Z3 tools (Linux amd64, Java 21, gh)
	$(PYTHON) -B test/formal/run.py install

test-formal: ## Prove Job retry arithmetic and qualify failure controls with installed tools
	$(PYTHON) -B test/formal/run.py check

# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
##@ 🧪 Unit and local functional tests
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

.PHONY: test-functional-local test-unit test-tooling-unit test-mutation test-property test-race test-shuffle test-fuzz test-e2e

PROPERTY_CHECKS ?= 1000
PROPERTY_SEED ?= 1
test-property: export PROPERTY_CHECKS := $(PROPERTY_CHECKS)
test-property: export PROPERTY_SEED := $(PROPERTY_SEED)
test-property: ## Run bounded isolated Rapid properties with verified completion (PROPERTY_CHECKS, PROPERTY_SEED)
	$(PYTHON) -I -B test/packaging/property.py --go "$(GO)" --toolchain "$(GO_TOOLCHAIN)"

# Tool identity is pinned in test/packaging/mutation-tool.json. No aggregate
# runs this expensive measurement implicitly; helper regressions stay native.
test-mutation: export MUTATION_PACKAGE := $(MUTATION_PACKAGE)
test-mutation: ## Measure Go unit assertion strength; select one reviewed MUTATION_PACKAGE
	$(PYTHON) -I -B test/packaging/mutation.py --go "$(GO)" --toolchain "$(GO_TOOLCHAIN)"

test-functional-local: ## Run units and local functional tests without a deployed environment
	$(GO) test -tags=functional $(GO_SOURCE_PACKAGES) -shuffle=on -count=1

test-unit: ## Run isolated Go units, including Minikube helpers without a cluster
	@set -eu; \
		packages="$$( $(GO) list -tags=minikube -f '{{.ImportPath}}' $(GO_SOURCE_PACKAGES) )"; \
		test -n "$$packages" || { printf '%s\n' 'No unit-test packages were discovered.' >&2; exit 1; }; \
		$(GO) test -tags=minikube $$packages -shuffle=on -count=1

# Keep each owner's imports in its own interpreter. Helm has only functional
# tests and retains its existing explicit selection.
PYTHON_UNIT_DIRS := ci clientcompat demo embeddings formal functional hooks interoperability mcp-scanner monitoring packaging performance pgbouncer sdk search-relevance vscode
test-tooling-unit: ## Run isolated Python helper tests in their owning directories
	@set -eu; test -n "$(PYTHON_UNIT_DIRS)" || { echo "No Python unit owners selected" >&2; exit 1; }; for owner in $(PYTHON_UNIT_DIRS); do \
		(cd "test/$$owner" && $(PYTHON) -B -c 'import sys, unittest; suite = unittest.defaultTestLoader.discover(".", pattern="test_unit_*.py"); assert suite.countTestCases() > 0, "No unit tests discovered"; sys.exit(not unittest.TextTestRunner(verbosity=2).run(suite).wasSuccessful())'); \
	done

test-race: ## Run the broad local suite with the race detector
	$(GO) test -tags=functional -race $(GO_SOURCE_PACKAGES) -shuffle=on -count=1

test-shuffle: ## Repeat ten times; run CLI separately with its measured 25-minute aggregate allowance
	@set -eu; all=$$($(GO) list -tags=functional $(GO_SOURCE_PACKAGES)); \
		packages=$$(printf '%s\n' "$$all" | awk '$$0 != "$(MODULE_PATH)/cmd/catalogctl" && NF'); \
		test -n "$$packages" || { echo "No non-CLI shuffle packages selected" >&2; exit 1; }; \
		$(GO) test -tags=functional $$packages -shuffle=on -count=10 -timeout=20m; \
		$(GO) test -tags=functional ./cmd/catalogctl -shuffle=on -count=10 -timeout=25m

test-fuzz: ## Fuzz security-sensitive identifiers, parsers, and URLs for FUZZ_TIME each
	$(GO) test ./cmd/catalogctl -run '^$$' -fuzz FuzzImportManifest -fuzztime=$(FUZZ_TIME)
	$(GO) test ./internal/draftfile -run '^$$' -fuzz FuzzDecode -fuzztime=$(FUZZ_TIME)
	$(GO) test ./internal/artwork -run '^$$' -fuzz FuzzDecodeArtworkInput -fuzztime=$(FUZZ_TIME)
	$(GO) test ./internal/artwork -run '^$$' -fuzz FuzzExtractArtworkJSONLiteral -fuzztime=$(FUZZ_TIME)
	$(GO) test ./internal/jsondoc -run '^$$' -fuzz FuzzControlJSON -fuzztime=$(FUZZ_TIME)
	$(GO) test ./internal/platform/authn -run '^$$' -fuzz FuzzValidateJWTEncoding -fuzztime=$(FUZZ_TIME)
	$(GO) test ./cmd/catalogctl -run '^$$' -fuzz FuzzRemoteOrigin -fuzztime=$(FUZZ_TIME)
	$(GO) test ./internal/platform/cliauth -run '^$$' -fuzz FuzzLoginCallback -fuzztime=$(FUZZ_TIME)
	$(GO) test ./cmd/catalogctl -run '^$$' -fuzz FuzzRemoteJSON -fuzztime=$(FUZZ_TIME)
	$(GO) test ./internal/audit -run '^$$' -fuzz FuzzVerifyExport -fuzztime=$(FUZZ_TIME)
	$(GO) test ./internal/catalog -run '^$$' -fuzz FuzzParseAIRIdentifier -fuzztime=$(FUZZ_TIME)
	$(GO) test ./internal/catalog -run '^$$' -fuzz FuzzCatalogSearchCursor -fuzztime=$(FUZZ_TIME)
	$(GO) test ./internal/catalog -run '^$$' -fuzz FuzzTraceContext -fuzztime=$(FUZZ_TIME)
	$(GO) test ./internal/identity -run '^$$' -fuzz FuzzMembershipCursor -fuzztime=$(FUZZ_TIME)
	$(GO) test ./internal/platform/httpserver -run '^$$' -fuzz FuzzMCPFiniteAdmission -fuzztime=$(FUZZ_TIME)
	$(GO) test ./internal/platform/fetcher -run '^$$' -fuzz FuzzValidateURL -fuzztime=$(FUZZ_TIME)
	$(GO) test ./internal/adapter -run '^$$' -fuzz FuzzSkillMarkdownValidation -fuzztime=$(FUZZ_TIME)
	$(GO) test ./internal/adapter -run '^$$' -fuzz FuzzSkillPackageValidation -fuzztime=$(FUZZ_TIME)
	$(GO) test ./internal/adapter -run '^$$' -fuzz FuzzPluginArchiveValidation -fuzztime=$(FUZZ_TIME)
	$(GO) test ./internal/publication -run '^$$' -fuzz FuzzNativeFields -fuzztime=$(FUZZ_TIME)
	$(GO) test ./internal/standards/anstrust -run '^$$' -fuzz FuzzCanonicalEvent -fuzztime=$(FUZZ_TIME)
	$(GO) test ./internal/standards/anstrust -run '^$$' -fuzz FuzzVerifyBundle -fuzztime=$(FUZZ_TIME)
	$(GO) test ./internal/standards/anstrust -run '^$$' -fuzz FuzzVerifyReceipt -fuzztime=$(FUZZ_TIME)
	$(GO) test ./internal/standards -run '^$$' -fuzz FuzzNormalizeARDEntry -fuzztime=$(FUZZ_TIME)
	$(GO) test ./internal/standards -run '^$$' -fuzz FuzzValidateAICatalog -fuzztime=$(FUZZ_TIME)
	$(GO) test ./internal/federation -run '^$$' -fuzz FuzzDiscoveryMetadataParsing -fuzztime=$(FUZZ_TIME)
	$(GO) test ./internal/federation -run '^$$' -fuzz FuzzRegistryListPage -fuzztime=$(FUZZ_TIME)
	$(GO) test ./internal/federation -run '^$$' -fuzz FuzzLiveResponseOptionalTypes -fuzztime=$(FUZZ_TIME)

test-e2e: ## Run only TestEndToEnd in internal/integration (in-memory)
	$(GO) test -tags=functional ./internal/integration/... -run TestEndToEnd -shuffle=on -count=1


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
##@ 📊 Coverage
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

.PHONY: coverage-profile coverage-unit coverage-core-profile test-cover coverage-summary
.PHONY: coverage-core-summary coverage-check coverage-core-check coverage-tools-install coverage-html
.PHONY: coverage-process-postgres-profile coverage-process-postgres-dsn-profile coverage-full
.PHONY: coverage-postgres-profile coverage-postgres-summary coverage-postgres-check
.PHONY: coverage-postgres-dsn-profile coverage-postgres-dsn-check functional-coverage

# Ignore repeated stop signals during scoped EXIT cleanup. GNU Make can forward
# a second signal after group termination. SIGKILL still leaves owned scratch.
# Local functional coverage, including units and captured child processes.
coverage-profile: ## Write default-suite coverage, excluding generated bindings and fixture executables
	@set -eu; \
		mkdir -p coverage/.staging; \
		package_lines="$$( $(GO) list -tags=functional -f '{{if and (ne .ImportPath "$(MODULE_PATH)/internal/a2apb") (ne .ImportPath "$(MODULE_PATH)/test/functional/fixture") (ne .ImportPath "$(MODULE_PATH)/test/functional/contracts")}}{{.ImportPath}}{{end}}' $(GO_SOURCE_PACKAGES) )"; \
		coverage_packages="$$(printf '%s\n' "$$package_lines" | awk 'NF { if (result != "") result = result ","; result = result $$0 } END { print result }')"; \
		test -n "$$coverage_packages" || { printf '%s\n' 'No coverage packages were discovered.' >&2; exit 1; }; \
		work="$$(mktemp -d coverage/.staging/local.XXXXXX)"; \
		profile="$$work/default.out"; \
		mkdir "$$work/processes"; \
		cleanup() { trap '' 1 2 15; rm -rf -- "$$work"; }; \
		trap cleanup 0; \
		trap 'trap "" 1 2 15; exit 129' 1; \
		trap 'trap "" 1 2 15; exit 130' 2; \
		trap 'trap "" 1 2 15; exit 143' 15; \
		CATALOG_PROCESS_COVERAGE_DIR="$(CURDIR)/$$work/processes" $(GO) test -tags=functional $(GO_SOURCE_PACKAGES) -shuffle=on -count=1 -covermode=atomic -coverpkg="$$coverage_packages" -coverprofile="$$profile"; \
		$(PYTHON) -B test/packaging/application_coverage.py collect "$$work/processes" "$$work/process.out" --go "$(GO)"; \
		$(PYTHON) -B test/packaging/application_coverage.py merge "$$profile" "$$work/process.out" "$$work/merged.out"; \
		mv "$$work/merged.out" coverage/default.out

# Isolated units retain their own application inventory and measurement.
coverage-unit: ## Enforce application-only unit aggregate and critical-package floors (both default 90 percent)
	@set -eu; \
		mkdir -p coverage/.staging; \
		work="$$(mktemp -d coverage/.staging/unit.XXXXXX)"; \
		trap 'trap "" 1 2 15; rm -rf "$$work"' 0; \
		trap 'trap "" 1 2 15; exit 129' 1; trap 'trap "" 1 2 15; exit 130' 2; trap 'trap "" 1 2 15; exit 143' 15; \
		$(GO) list -tags=minikube -f '{{.ImportPath}}' $(GO_SOURCE_PACKAGES) > "$$work/tests"; \
		test -s "$$work/tests" || { echo 'No unit-test packages were discovered.' >&2; exit 1; }; \
		$(GO) list -tags=minikube -f '{{if and (ne .ImportPath "$(MODULE_PATH)/internal/a2apb") (ne .ImportPath "$(MODULE_PATH)/internal/integration")}}{{.ImportPath}}{{end}}' $(GO_APPLICATION_PACKAGES) > "$$work/application-list"; \
		sed '/^$$/d' "$$work/application-list" > "$$work/packages"; \
		test -s "$$work/packages" || { echo 'No application coverage packages were discovered.' >&2; exit 1; }; \
		coverage_packages="$$(paste -sd, "$$work/packages")"; \
		$(GO) test -tags=minikube $$(cat "$$work/tests") -shuffle=on -count=1 -covermode=atomic -coverpkg="$$coverage_packages" -coverprofile="$$work/profile"; \
		$(PYTHON) -B test/packaging/unit_coverage.py "$$work/profile" "$$work/packages" --minimum "$(UNIT_COVERAGE_MIN)" --critical-minimum "$(UNIT_CRITICAL_PACKAGE_COVERAGE_MIN)" --critical-packages "$(CRITICAL_COVERAGE_PACKAGES)" --zero-statements "$(MODULE_PATH)/migrations" --zero-statements "$(MODULE_PATH)/samples" > "$$work/report"; \
		mv "$$work/profile" coverage/unit.out; \
		cat "$$work/report"

# Summaries and floors derived from the broad local profile.
coverage-core-profile: coverage-profile ## Filter default-suite coverage to code outside the database package
	@set -eu; \
		mkdir -p coverage/.staging; \
		profile="$$(mktemp coverage/.staging/core.out.XXXXXX)"; \
		cleanup() { trap '' 1 2 15; if [ -n "$$profile" ]; then rm -f -- "$$profile"; fi; }; \
		trap cleanup 0; \
		trap 'trap "" 1 2 15; exit 129' 1; \
		trap 'trap "" 1 2 15; exit 130' 2; \
		trap 'trap "" 1 2 15; exit 143' 15; \
		awk 'NR == 1 || ($$1 !~ /internal\/platform\/database\//)' coverage/default.out > "$$profile"; \
		mv "$$profile" coverage/core.out; \
		profile=""; \
		trap - 0 1 2 15

test-cover: coverage-profile ## Write an atomic coverage profile and print the function summary
	$(GO) tool cover -func=coverage/default.out

coverage-summary: coverage-profile ## Print merged statement coverage by service package
	@awk 'NR > 1 { key = $$1; source = key; sub(/:[0-9].*$$/, "", source); sub(/\/[^/]+$$/, "", source); packages[key] = source; statements[key] = $$2; if ($$3 > 0) covered[key] = 1 } END { for (key in statements) { source = packages[key]; totals[source] += statements[key]; if (covered[key]) hits[source] += statements[key] } for (source in totals) printf "%-80s %6.1f%%\n", source, 100 * hits[source] / totals[source] }' coverage/default.out | LC_ALL=C sort

coverage-core-summary: coverage-core-profile ## Print service-free core coverage by function
	$(GO) tool cover -func=coverage/core.out

coverage-check: coverage-profile ## Enforce exact broad-local statement coverage (default 90 percent)
	$(PYTHON) -B test/packaging/application_coverage.py check coverage/default.out --scope local --minimum "$(COVERAGE_MIN)"

coverage-core-check: coverage-core-profile ## Enforce exact local/core aggregate (93) and critical-package (90) floors
	$(PYTHON) -B test/packaging/application_coverage.py check coverage/core.out --scope core --minimum "$(CORE_COVERAGE_MIN)" --critical-minimum "$(CRITICAL_PACKAGE_COVERAGE_MIN)" --critical-packages "$(CRITICAL_COVERAGE_PACKAGES)"

coverage-tools-install coverage-html: export PACKAGING_GO = $(GO)
coverage-tools-install coverage-html: export GOCOV := $(GOCOV)
coverage-tools-install coverage-html: export GOCOV_HTML := $(GOCOV_HTML)
coverage-tools-install: ## Install and verify the locked coverage reporters (explicit network acquisition)
	GOTOOLCHAIN=$(GO_TOOLCHAIN) GOWORK=off CGO_ENABLED=0 GOOS="$$($(GO) env GOHOSTOS)" GOARCH="$$($(GO) env GOHOSTARCH)" $(GO) -C test/packaging/coverage-tools install -mod=readonly github.com/axw/gocov/gocov github.com/matm/gocov-html/cmd/gocov-html
	$(PYTHON) -B test/packaging/coverage_tools.py

coverage-html: coverage-profile ## Render a rich HTML coverage dashboard in coverage/index.html (requires coverage-tools-install)
	$(PYTHON) -B test/packaging/coverage_tools.py
	@set -eu; \
		mkdir -p coverage/.staging; \
		report="$$(mktemp -d coverage/.staging/html.XXXXXX)"; \
		cleanup() { trap '' 1 2 15; if [ -n "$$report" ]; then rm -rf -- "$$report"; fi; }; \
		trap cleanup 0; \
		trap 'trap "" 1 2 15; exit 129' 1; \
		trap 'trap "" 1 2 15; exit 130' 2; \
		trap 'trap "" 1 2 15; exit 143' 15; \
		"$(GOCOV)" convert coverage/default.out > "$$report/profile.json"; \
		"$(GOCOV_HTML)" -t kit < "$$report/profile.json" > "$$report/index.html"; \
		mv "$$report/index.html" coverage/index.html; \
		cleanup; report=""; \
		trap - 0 1 2 15

# Real PostgreSQL process coverage uses the shared disposable-database launcher.
coverage-process-postgres-profile: ## Collect CLI/API/worker coverage with owned PostgreSQL
	$(call run_postgres_integration,coverage-process-postgres-dsn-profile)

coverage-process-postgres-dsn-profile: ## Collect process coverage against a fresh disposable PostgreSQL fixture
	@test -n "$${CATALOG_TEST_DATABASE_DSN:-}" || { echo 'CATALOG_TEST_DATABASE_DSN is required.' >&2; exit 2; }
	@set -eu; \
		mkdir -p coverage/.staging; \
		work="$$(mktemp -d coverage/.staging/process.XXXXXX)"; \
		trap 'trap "" 1 2 15; rm -rf "$$work"' 0; \
		trap 'trap "" 1 2 15; exit 129' 1; trap 'trap "" 1 2 15; exit 130' 2; trap 'trap "" 1 2 15; exit 143' 15; \
		mkdir "$$work/counters"; \
		CATALOG_PROCESS_COVERAGE_DIR="$(CURDIR)/$$work/counters" $(GO) test -race -tags=integration ./test/functional -run '^TestPostgresProcesses$$' -count=1 -timeout=5m; \
		$(PYTHON) -B test/packaging/application_coverage.py collect "$$work/counters" "$$work/profile" --go "$(GO)"; \
		mv "$$work/profile" coverage/process-postgres.out

# Run alone: regenerate every constituent profile before publishing their union.
coverage-full: ## Run fresh unit/local/PostgreSQL/process coverage; enforce 95 percent combined and 80 percent per file
	@set -eu; \
		mkdir -p coverage/.staging; \
		work="$$(mktemp -d coverage/.staging/full.XXXXXX)"; \
		trap 'trap "" 1 2 15; rm -rf "$$work"' 0; \
		trap 'trap "" 1 2 15; exit 129' 1; trap 'trap "" 1 2 15; exit 130' 2; trap 'trap "" 1 2 15; exit 143' 15; \
		$(PYTHON) -B test/packaging/application_coverage.py snapshot > "$$work/before"; \
		$(MAKE) --no-print-directory -j1 coverage-unit CATALOG_ENV_FILE=/dev/null; \
		$(MAKE) --no-print-directory -j1 coverage-check coverage-core-check coverage-postgres-check coverage-process-postgres-profile CATALOG_ENV_FILE=/dev/null; \
		$(PYTHON) -B test/packaging/application_coverage.py snapshot > "$$work/after"; \
		cmp -s "$$work/before" "$$work/after" || { echo 'Source changed during full coverage.' >&2; exit 1; }; \
		$(PYTHON) -B test/packaging/application_coverage.py full coverage/unit.out coverage/default.out coverage/postgres.out coverage/process-postgres.out "$$work/profile" "$$work/report" --minimum "$(FULL_COVERAGE_MIN)"; \
		printf '\nSource digest: ' >> "$$work/report"; cat "$$work/before" >> "$$work/report"; \
		mv "$$work/profile" coverage/full.out; \
		mv "$$work/report" coverage/full.txt; \
		cat coverage/full.txt

# Repository integration coverage retains its separate database-package floor.
coverage-postgres-profile: ## Write PostgreSQL integration coverage using disposable Compose
	$(call run_postgres_integration,coverage-postgres-dsn-profile)

coverage-postgres-summary: coverage-postgres-profile ## Print PostgreSQL integration coverage by function
	$(GO) tool cover -func=coverage/postgres.out

coverage-postgres-check: ## Enforce PostgreSQL integration coverage using disposable Compose
	$(call run_postgres_integration,coverage-postgres-dsn-check)

coverage-postgres-dsn-profile: ## Write PostgreSQL coverage against an explicit disposable DSN
	@test -n "$${CATALOG_TEST_DATABASE_DSN:-}" || { printf '%s\n' 'CATALOG_TEST_DATABASE_DSN is required.' >&2; exit 2; }
	@set -eu; \
		mkdir -p coverage/.staging; \
		profile="$$(mktemp coverage/.staging/postgres.out.XXXXXX)"; \
		cleanup() { trap '' 1 2 15; if [ -n "$$profile" ]; then rm -f -- "$$profile"; fi; }; \
		trap cleanup 0; \
		trap 'trap "" 1 2 15; exit 129' 1; \
		trap 'trap "" 1 2 15; exit 130' 2; \
		trap 'trap "" 1 2 15; exit 143' 15; \
		$(GO) test -race -tags=integration ./internal/platform/database/... -shuffle=on -count=1 -covermode=atomic -coverpkg=$(MODULE_PATH)/internal/platform/database -coverprofile="$$profile"; \
		mv "$$profile" coverage/postgres.out; \
		profile=""; \
		trap - 0 1 2 15

coverage-postgres-dsn-check: coverage-postgres-dsn-profile ## Enforce exact PostgreSQL package coverage (default 95 percent) against a disposable DSN
	$(PYTHON) -B test/packaging/application_coverage.py check coverage/postgres.out --scope postgres --minimum "$(POSTGRES_COVERAGE_MIN)"

# API-operation obligations are distinct from Go statement coverage.
functional-coverage: ## Summarize explicitly selected compatible FUNCTIONAL_REPORTS without running services
	@$(PYTHON) test/functional/operation_coverage.py

functional-coverage: export FUNCTIONAL_REPORTS := $(FUNCTIONAL_REPORTS)


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
##@ 🗄️ PostgreSQL tests and operator commands
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

.PHONY: test-process-postgres test-process-postgres-dsn test-integration test-integration-dsn migrate-plan
.PHONY: migrate-apply migrate-semantic-plan migrate-semantic-apply repair-idempotency-list
.PHONY: repair-idempotency-plan repair-idempotency-apply repair-search-projections restore-verify

# Shared by explicit database tests, coverage and search benchmarks.
define run_postgres_integration
	@set -eu; \
		project="open-agentic-catalog-integration-$${GITHUB_RUN_ID:+$${GITHUB_RUN_ID}-}$$$$"; \
		cleanup() { $(COMPOSE) --env-file /dev/null -f compose.integration.yaml -p "$$project" down --volumes --remove-orphans >/dev/null 2>&1; }; \
		finish() { status=$$?; trap - 0; cleanup_status=0; cleanup || cleanup_status=$$?; if [ "$$cleanup_status" -ne 0 ]; then printf '%s\n' 'PostgreSQL integration cleanup failed.' >&2; if [ "$$status" -eq 0 ]; then status=$$cleanup_status; fi; fi; exit "$$status"; }; \
		trap finish 0; \
		trap 'exit 129' 1; \
		trap 'exit 130' 2; \
		trap 'exit 143' 15; \
		$(COMPOSE) --env-file /dev/null -f compose.integration.yaml -p "$$project" up --detach --wait --wait-timeout 60 postgres; \
		published="$$( $(COMPOSE) --env-file /dev/null -f compose.integration.yaml -p "$$project" port postgres 5432 )"; \
		case "$$published" in 127.0.0.1:[0-9]*) ;; *) printf '%s\n' "Unexpected PostgreSQL test address: $$published" >&2; exit 1 ;; esac; \
		port="$${published##*:}"; \
		status=0; \
		$(MAKE) --no-print-directory $(1) CATALOG_TEST_DATABASE_DSN="postgres://catalog:catalog@127.0.0.1:$$port/catalog_test?sslmode=disable" || status=$$?; \
		if [ "$$status" -ne 0 ]; then $(COMPOSE) --env-file /dev/null -f compose.integration.yaml -p "$$project" logs --no-color postgres >&2 || true; fi; \
		exit "$$status"
endef

PGBOUNCER_WRITER_LOSS ?= planned

.PHONY: test-pgbouncer
test-pgbouncer: ## Qualify session pooling, TLS, locks and recovery with owned PostgreSQL and PgBouncer
	$(PYTHON) -B test/pgbouncer/runner.py --writer-loss "$(PGBOUNCER_WRITER_LOSS)"

test-process-postgres: ## Test real CLI/API/worker processes with owned PostgreSQL
	$(call run_postgres_integration,test-process-postgres-dsn)

test-process-postgres-dsn: ## Test processes against an explicit fresh disposable PostgreSQL fixture
	@test -n "$${CATALOG_TEST_DATABASE_DSN:-}" || { echo 'CATALOG_TEST_DATABASE_DSN is required.' >&2; exit 2; }
	$(GO) test -race -tags=integration ./test/functional -run '^TestPostgresProcesses$$' -count=1 -timeout=5m

test-integration: ## Run PostgreSQL and RLS tests using disposable Compose
	$(call run_postgres_integration,test-integration-dsn)

test-integration-dsn: ## Run PostgreSQL and RLS tests against an explicit disposable DSN
	@test -n "$${CATALOG_TEST_DATABASE_DSN:-}" || { printf '%s\n' 'CATALOG_TEST_DATABASE_DSN is required.' >&2; exit 2; }
	$(GO) test -race -tags=integration ./internal/platform/database/... -shuffle=on -count=1

.PHONY: webhook-report-build webhook-report-check test-webhook-report test-webhook-report-dsn
webhook-report-build webhook-report-check test-webhook-report test-webhook-report-dsn: export GOWORK := off
webhook-report-build: ## Build the independent Go SDK publication-report example locally
	@mkdir -p bin/examples
	cd examples/publication-report && $(GO) build -mod=readonly -trimpath -o ../../bin/examples/publication-report .

webhook-report-check: ## Check the Go receiver example, SDK, isolated units and source analyzers
	$(MAKE) --no-print-directory sdk-check SDK_LANGUAGE=go
	cmp examples/webhookreceiver/verify.go examples/publication-report/internal/webhookreceiver/verify.go
	cmp LICENSE examples/publication-report/LICENSE
	cmp sdk/go/NOTICE examples/publication-report/SDK-NOTICE
	cd examples/publication-report && $(GO) mod tidy -diff
	cd examples/publication-report && test -z "$$(gofmt -l *.go internal/webhookreceiver/*.go)"
	@mkdir -p coverage
	cd examples/publication-report && $(GO) test -race -shuffle=on -count=1 -coverpkg=./... -coverprofile=../../coverage/publication-report-unit.out ./...
	cd examples/publication-report && $(GO) vet -tags=functional,integration ./...
	cd examples/publication-report && ../../test/sdk/.tools/staticcheck -tags=functional,integration ./...
	cd examples/publication-report && ../../test/sdk/.tools/golangci-lint run --config ../../.golangci.yaml --build-tags=functional,integration ./...
	@mkdir -p output/verification/publication-report
	cd examples/publication-report && ../../test/sdk/.tools/go-licenses report --include_tests --ignore=$(MODULE_PATH) ./... > ../../output/verification/publication-report/licenses.csv
	$(PYTHON) -B -c "import sys; sys.path.insert(0, 'test/packaging'); from go_licenses import check_report; from pathlib import Path; check_report(Path('output/verification/publication-report/licenses.csv').read_text(), set('$(GO_LICENSE_ALLOWLIST)'.split(',')), False)"
	cd examples/publication-report && $(GO) list -deps -f '{{if .Module}}{{.Module.Path}}{{end}}' ./... > ../../output/verification/publication-report/runtime-modules.txt
	$(PYTHON) -B -c "from pathlib import Path; assert '$(MODULE_PATH)' not in Path('output/verification/publication-report/runtime-modules.txt').read_text().splitlines(), 'Example imports the Catalog application module'"

test-webhook-report: webhook-report-build ## Qualify the actual HTTPS receiver with an owned PostgreSQL fixture
	$(call run_postgres_integration,test-webhook-report-dsn)

test-webhook-report-dsn: ## Run receiver process and SQL checks against an explicit disposable database
	@test -n "$${CATALOG_TEST_DATABASE_DSN:-}" || { printf '%s\n' 'CATALOG_TEST_DATABASE_DSN is required.' >&2; exit 2; }
	cd examples/publication-report && PUBLICATION_REPORT_BINARY="$(CURDIR)/bin/examples/publication-report" $(GO) test -race -tags=functional,integration -shuffle=on -count=1 ./...

.PHONY: mcp-scanner-receiver-build mcp-scanner-receiver-check test-mcp-scanner-receiver test-mcp-scanner-receiver-dsn
mcp-scanner-receiver-build mcp-scanner-receiver-check test-mcp-scanner-receiver test-mcp-scanner-receiver-dsn: export GOWORK := off
mcp-scanner-receiver-build: ## Build the standalone signed-webhook MCP scanner receiver
	@mkdir -p bin/examples
	cd examples/mcp-scanner/receiver && $(GO) build -mod=readonly -trimpath -o ../../../bin/examples/mcp-scan-receiver .

mcp-scanner-receiver-check: ## Check receiver units, local MCP/TLS transport, analyzers, SDK and licenses
	$(MAKE) --no-print-directory sdk-check SDK_LANGUAGE=go
	cmp examples/webhookreceiver/verify.go examples/mcp-scanner/receiver/internal/webhookreceiver/verify.go
	cmp LICENSE examples/mcp-scanner/receiver/LICENSE
	cmp sdk/go/NOTICE examples/mcp-scanner/receiver/SDK-NOTICE
	cd examples/mcp-scanner/receiver && $(GO) mod tidy -diff
	cd examples/mcp-scanner/receiver && test -z "$$(gofmt -l .)"
	@mkdir -p coverage output/verification/mcp-scanner-receiver
	cd examples/mcp-scanner/receiver && $(GO) test -race -shuffle=on -count=1 -coverpkg=./... -coverprofile=../../../coverage/mcp-scanner-receiver-unit.out ./...
	cd examples/mcp-scanner/receiver && $(GO) test -race -tags=functional -shuffle=on -count=1 ./...
	cd examples/mcp-scanner/receiver && $(GO) vet -tags=functional,integration ./...
	cd examples/mcp-scanner/receiver && ../../../test/sdk/.tools/staticcheck -tags=functional,integration ./...
	cd examples/mcp-scanner/receiver && ../../../test/sdk/.tools/golangci-lint run --config ../../../.golangci.yaml --build-tags=functional,integration ./...
	cd examples/mcp-scanner/receiver && ../../../test/sdk/.tools/go-licenses report --include_tests --ignore=$(MODULE_PATH) ./... > ../../../output/verification/mcp-scanner-receiver/licenses.csv
	$(PYTHON) -B -c "import sys; sys.path.insert(0, 'test/packaging'); from go_licenses import check_report; from pathlib import Path; check_report(Path('output/verification/mcp-scanner-receiver/licenses.csv').read_text(), set('$(GO_LICENSE_ALLOWLIST)'.split(',')), False)"
	cd examples/mcp-scanner/receiver && $(GO) list -deps -f '{{if .Module}}{{.Module.Path}}{{end}}' ./... > ../../../output/verification/mcp-scanner-receiver/runtime-modules.txt
	$(PYTHON) -B -c "from pathlib import Path; assert '$(MODULE_PATH)' not in Path('output/verification/mcp-scanner-receiver/runtime-modules.txt').read_text().splitlines(), 'Example imports the Catalog application module'"

test-mcp-scanner-receiver: mcp-scanner-receiver-build ## Test signed receipt, restart, MCP collection and reports with owned PostgreSQL
	$(call run_postgres_integration,test-mcp-scanner-receiver-dsn)

test-mcp-scanner-receiver-dsn: ## Run receiver process and SQL checks against an explicit disposable database
	@test -n "$${CATALOG_TEST_DATABASE_DSN:-}" || { echo 'CATALOG_TEST_DATABASE_DSN is required.' >&2; exit 2; }
	cd examples/mcp-scanner/receiver && MCP_SCANNER_RECEIVER_BINARY="$(CURDIR)/bin/examples/mcp-scan-receiver" MCP_SCANNER_TEST_PYTHON="$(CURDIR)/.tools/dev/bin/python" $(GO) test -race -tags=functional,integration -shuffle=on -count=1 -timeout=6m ./...

# Operator commands use explicitly selected authority, not the test fixture.
$(CATALOG_DATABASE_OPERATOR_TARGETS): export CATALOG_DATABASE_DSN = $(CATALOG_MIGRATION_DATABASE_DSN)

migrate-plan: ## Show unapplied PostgreSQL migrations without changing the database
	@$(GO) run ./cmd/catalogctl migrate plan --config "$(CATALOG_POSTGRES_CONFIG_FILE)"

migrate-apply: ## Apply embedded forward-only PostgreSQL migrations
	@$(GO) run ./cmd/catalogctl migrate apply --config "$(CATALOG_POSTGRES_CONFIG_FILE)" --api-role "$(CATALOG_DATABASE_API_ROLE)" --worker-role "$(CATALOG_DATABASE_WORKER_ROLE)"

migrate-semantic-plan: ## Inspect core and optional semantic schema (no writes or model calls)
	@$(GO) run ./cmd/catalogctl migrate plan --semantic --config "$(CATALOG_POSTGRES_CONFIG_FILE)" --api-role "$(CATALOG_DATABASE_API_ROLE)" --worker-role "$(CATALOG_DATABASE_WORKER_ROLE)"

migrate-semantic-apply: ## Apply core and optional semantic schema; requires administrator-installed pgvector
	@$(GO) run ./cmd/catalogctl migrate apply --semantic --config "$(CATALOG_POSTGRES_CONFIG_FILE)" --api-role "$(CATALOG_DATABASE_API_ROLE)" --worker-role "$(CATALOG_DATABASE_WORKER_ROLE)"

repair-idempotency-list: export REPAIR_TENANT_ID := $(REPAIR_TENANT_ID)
repair-idempotency-list: export REPAIR_LIMIT := $(REPAIR_LIMIT)
repair-idempotency-list: ## List bounded pending command reservations for REPAIR_TENANT_ID
	@test -n "$$REPAIR_TENANT_ID" || { printf '%s\n' 'REPAIR_TENANT_ID is required.' >&2; exit 2; }
	@$(GO) run ./cmd/catalogctl repair idempotency list --config "$(CATALOG_POSTGRES_CONFIG_FILE)" --tenant "$$REPAIR_TENANT_ID" --limit "$$REPAIR_LIMIT"

repair-idempotency-plan: export REPAIR_TENANT_ID := $(REPAIR_TENANT_ID)
repair-idempotency-plan: export REPAIR_RESERVATION_ID := $(REPAIR_RESERVATION_ID)
repair-idempotency-plan: export REPAIR_REASON := $(REPAIR_REASON)
repair-idempotency-plan: ## Plan one inactive command repair with explicit target and reason
	@test -n "$$REPAIR_TENANT_ID" || { printf '%s\n' 'REPAIR_TENANT_ID is required.' >&2; exit 2; }
	@test -n "$$REPAIR_RESERVATION_ID" || { printf '%s\n' 'REPAIR_RESERVATION_ID is required.' >&2; exit 2; }
	@test -n "$$REPAIR_REASON" || { printf '%s\n' 'REPAIR_REASON is required.' >&2; exit 2; }
	@$(GO) run ./cmd/catalogctl repair idempotency plan --config "$(CATALOG_POSTGRES_CONFIG_FILE)" --tenant "$$REPAIR_TENANT_ID" --reservation "$$REPAIR_RESERVATION_ID" --reason "$$REPAIR_REASON"

repair-idempotency-apply: export REPAIR_TENANT_ID := $(REPAIR_TENANT_ID)
repair-idempotency-apply: export REPAIR_RESERVATION_ID := $(REPAIR_RESERVATION_ID)
repair-idempotency-apply: export REPAIR_REASON := $(REPAIR_REASON)
repair-idempotency-apply: export REPAIR_PLAN_DIGEST := $(REPAIR_PLAN_DIGEST)
repair-idempotency-apply: ## Apply one unchanged reviewed command-repair plan
	@test -n "$$REPAIR_TENANT_ID" || { printf '%s\n' 'REPAIR_TENANT_ID is required.' >&2; exit 2; }
	@test -n "$$REPAIR_RESERVATION_ID" || { printf '%s\n' 'REPAIR_RESERVATION_ID is required.' >&2; exit 2; }
	@test -n "$$REPAIR_REASON" || { printf '%s\n' 'REPAIR_REASON is required.' >&2; exit 2; }
	@test -n "$$REPAIR_PLAN_DIGEST" || { printf '%s\n' 'REPAIR_PLAN_DIGEST is required.' >&2; exit 2; }
	@$(GO) run ./cmd/catalogctl repair idempotency apply --config "$(CATALOG_POSTGRES_CONFIG_FILE)" --tenant "$$REPAIR_TENANT_ID" --reservation "$$REPAIR_RESERVATION_ID" --reason "$$REPAIR_REASON" --plan-digest "$$REPAIR_PLAN_DIGEST"

repair-search-projections: export REPAIR_TENANT_ID := $(REPAIR_TENANT_ID)
repair-search-projections: export REPAIR_REASON := $(REPAIR_REASON)
repair-search-projections: export SEARCH_PROJECTION_REPAIR_LIMIT := $(SEARCH_PROJECTION_REPAIR_LIMIT)
repair-search-projections: export SEARCH_PROJECTION_REPAIR_OPERATOR_LABEL := $(SEARCH_PROJECTION_REPAIR_OPERATOR_LABEL)
repair-search-projections: ## Rebuild one bounded tenant batch of derived indexed-search projections
	@test -n "$$REPAIR_TENANT_ID" || { printf '%s\n' 'REPAIR_TENANT_ID is required.' >&2; exit 2; }
	@test -n "$$REPAIR_REASON" || { printf '%s\n' 'REPAIR_REASON is required.' >&2; exit 2; }
	@$(GO) run ./cmd/catalogctl repair search-projections --config "$(CATALOG_POSTGRES_CONFIG_FILE)" --tenant "$$REPAIR_TENANT_ID" --reason "$$REPAIR_REASON" --operator-label "$$SEARCH_PROJECTION_REPAIR_OPERATOR_LABEL" --limit "$$SEARCH_PROJECTION_REPAIR_LIMIT"

restore-verify: export RESTORE_TENANT_ID := $(RESTORE_TENANT_ID)
restore-verify: export RESTORE_RECOVERY_POINT := $(RESTORE_RECOVERY_POINT)
restore-verify: export RESTORE_CURSOR := $(RESTORE_CURSOR)
restore-verify: export RESTORE_VERIFY_LIMIT := $(RESTORE_VERIFY_LIMIT)
restore-verify: ## Verify one bounded page of an isolated restored tenant
	@test -n "$$RESTORE_TENANT_ID" || { printf '%s\n' 'RESTORE_TENANT_ID is required.' >&2; exit 2; }
	@test -n "$$RESTORE_RECOVERY_POINT" || { printf '%s\n' 'RESTORE_RECOVERY_POINT is required.' >&2; exit 2; }
	@$(GO) run ./cmd/catalogctl restore verify --config "$(CATALOG_POSTGRES_CONFIG_FILE)" --tenant "$$RESTORE_TENANT_ID" --recovery-point "$$RESTORE_RECOVERY_POINT" --api-role "$(CATALOG_DATABASE_API_ROLE)" --worker-role "$(CATALOG_DATABASE_WORKER_ROLE)" --cursor "$$RESTORE_CURSOR" --limit "$$RESTORE_VERIFY_LIMIT"


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
##@ 🧩 API, standards and client compatibility
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

.PHONY: interoperability-tools-install test-interoperability test-interoperability-hf client-go-tools-install client-go-generate
.PHONY: test-interoperability-agntcy mcp-tools-install test-interoperability-mcp test-interoperability-mcp-catalog a2a-tools-install test-interoperability-a2a
.PHONY: client-go-check client-skill-check validation-tools-install api-yaml-check api-validate
.PHONY: api-contract-check api-lint api-check api-validation-test api-diff api-breaking-check profiles
.PHONY: conformance-prerequisite conformance-fixture conformance-live

.PHONY: zap-tools-install test-security-passive test-security-active
zap-tools-install: ## Download the digest-pinned execution-only ZAP image
	$(PYTHON) -B test/functional/security_scan.py install

test-security-passive: ## Inspect owned authenticated API/UI traffic with qualified ZAP rules
	$(PYTHON) -B test/functional/security_scan.py check
	$(PYTHON) -B test/functional/runner.py run-security-passive

test-security-active: ## Scan owned catalog search/browse inputs with selected ZAP injection rules
	$(PYTHON) -B test/functional/security_scan.py check
	$(PYTHON) -B test/functional/runner.py run-security-active

.PHONY: schemathesis-tools-install test-api-generated
schemathesis-tools-install: ## Install pinned isolated Schemathesis tooling (downloads allowed)
	$(PYTHON) -B test/functional/generated_api.py install

test-api-generated: ## Generate bounded authenticated API cases against owned PostgreSQL fixtures
	$(PYTHON) -B test/functional/generated_api.py check
	$(PYTHON) -B test/functional/runner.py run-generated-api

.PHONY: plugin-tools-install test-interoperability-plugins
plugin-tools-install: ## Install the pinned test-only portable Plugin reader and hashed dependencies
	$(PYTHON) -B test/interoperability/plugins_runner.py install

test-interoperability-plugins: ## Verify independent Plugin loading and immutable archive handoff offline
	$(PYTHON) -B test/interoperability/plugins_runner.py check

.PHONY: skill-tools-install test-interoperability-skills
skill-tools-install: ## Install the pinned test-only Skill reference reader and hashed dependencies
	$(PYTHON) -B test/interoperability/skills_runner.py install

test-interoperability-skills: ## Verify independent immutable Skill consumption on owned offline fixtures
	$(PYTHON) -B test/interoperability/skills_runner.py check

a2a-tools-install: ## Install the locked test-only official A2A SDK and unchanged Hello World executor
	$(PYTHON) -B test/interoperability/a2a_runner.py install

test-interoperability-a2a: ## Qualify governed A2A handoff, direct control and denials on isolated private HTTPS
	$(PYTHON) -B test/interoperability/a2a_runner.py check

mcp-tools-install: ## Install locked test-only MCP Inspector and Everything (downloads allowed)
	$(PYTHON) -B test/interoperability/mcp_runner.py install

.PHONY: mcp-catalog-tools-install
mcp-catalog-tools-install: ## Install the patched independent dual-protocol Python MCP client (downloads allowed)
	$(PYTHON) -B test/interoperability/mcp_runner.py catalog-install

.PHONY: contextforge-tools-install test-interoperability-contextforge
contextforge-tools-install: ## Install and inventory the reviewed latest stable ContextForge image (downloads allowed)
	PYTHONOPTIMIZE=0 $(PYTHON) -B test/interoperability/contextforge_tools.py

test-interoperability-contextforge: ## Qualify Catalog MCP proxying and governed ContextForge runtime handoff in isolated Compose
	PYTHONOPTIMIZE=0 $(PYTHON) -B test/interoperability/contextforge_runner.py check

test-interoperability-mcp-catalog: ## Qualify Catalog stdio/HTTP protocols, discovery and PostgreSQL replicas with an independent client
	$(PYTHON) -B test/interoperability/mcp_runner.py catalog

test-interoperability-mcp: ## Qualify governed MCP handoff, authorization, credential isolation and TLS/outage failures
	$(PYTHON) -B test/interoperability/mcp_runner.py check

.PHONY: pi-ard-tools-install test-interoperability-pi-ard-prepare test-interoperability-pi-ard
pi-ard-tools-install: ## Acquire reviewed Pi/pi-ard tools explicitly, without lifecycle scripts or registry audits
	$(PYTHON) -B test/interoperability/pi_ard.py install

test-interoperability-pi-ard-prepare: ## Qualify native Pi startup, confinement, budgets and cleanup only
	$(PYTHON) -B test/interoperability/pi_ard.py prepare

test-interoperability-pi-ard: ## Qualify native Pi discovery, continuation and declined installation against an owned Catalog
	$(PYTHON) -B test/interoperability/pi_ard.py check

.PHONY: ard-explorer-tools-install test-interoperability-ard-explorer-startup test-interoperability-ard-explorer
ard-explorer-tools-install: ## Install the pinned execution-only Explorer source and build tools explicitly
	$(PYTHON) test/interoperability/ard_explorer.py install

test-interoperability-ard-explorer-startup: ## Qualify Explorer startup and confinement locally, without browsing qualification
	$(PYTHON) test/interoperability/ard_explorer.py check

test-interoperability-ard-explorer: ## Qualify native Explorer browsing and client limits on an owned Catalog
	$(PYTHON) test/interoperability/ard_explorer.py browse

interoperability-tools-install: ## Install the pinned test-only Hugging Face CLI image (downloads allowed)
	python3 -B test/interoperability/runner.py install

INTEROPERABILITY_TRUST_VERIFICATION ?= off

test-interoperability-hf test-interoperability-gateway test-minikube-agntcy: export INTEROPERABILITY_TRUST_VERIFICATION := $(INTEROPERABILITY_TRUST_VERIFICATION)

test-interoperability-hf: ## Qualify local HF peers (opt-in INTEROPERABILITY_TRUST_VERIFICATION=on)
	$(PYTHON) -B test/interoperability/runner.py peer

.PHONY: agntcy-tools-install test-minikube-agntcy gateway-tools-install test-interoperability-gateway ans-tools-install test-interoperability-ans
agntcy-tools-install: ## Acquire checksum-pinned Directory tools, chart and images; no cluster deployment
	PYTHONOPTIMIZE=0 $(PYTHON) -B test/interoperability/agntcy_tools.py install

ans-tools-install: ## Acquire and build pinned ANS RA, TL and Finder; no cluster deployment
	PYTHONOPTIMIZE=0 $(PYTHON) -B test/interoperability/ans_tools.py install

gateway-tools-install: ## Acquire exact Gateway source/images and build its native auth layout; no deployment
	PYTHONOPTIMIZE=0 $(PYTHON) -B test/interoperability/gateway_tools.py install

test-interoperability-agntcy: export AGNTCY_LAB_DIR := $(AGNTCY_LAB_DIR)
test-interoperability-agntcy: export AGNTCY_GO := $(GO)
test-interoperability-agntcy: ## Qualify A2A interchange and ARD limits against an explicitly selected local Directory lab
	$(PYTHON) -B test/interoperability/agntcy_runner.py

test-interoperability: ## Qualify HF Search, native filters/paging, navigation and client limits locally
	python3 -B test/interoperability/runner.py check

client-go-tools-install: ## Explicitly acquire locked Go-client generator and test dependencies
	$(PYTHON) test/clientcompat/runner.py install

client-go-generate: ## Generate ignored Go client for inspection using installed tools
	$(PYTHON) test/clientcompat/runner.py generate

client-go-check: ## Regenerate, compile and test Go-client compatibility without services or downloads
	$(PYTHON) test/clientcompat/runner.py check

.PHONY: sdk-tools-install sdk-generate sdk-check sdk-package sdk-rust-audit-tools-install sdk-rust-audit
sdk-tools-install: ## Install pinned SDK tools (SDK_LANGUAGE=all|go|python|typescript|rust; default all)
	$(PYTHON) -B test/sdk/runner.py install --language "$(SDK_LANGUAGE)"

sdk-rust-audit-tools-install: ## Explicitly install the separate Rust dependency analyzer and refresh advisory data
	$(PYTHON) -B test/sdk/runner.py audit-install --language rust

sdk-rust-audit: ## Run separate offline Rust license/advisory checks; never a generation/build prerequisite
	$(PYTHON) -B test/sdk/runner.py audit --language rust

sdk-generate: ## Regenerate SDK source with installed tools (SDK_LANGUAGE=all|go|python|typescript|rust)
	$(PYTHON) -B test/sdk/runner.py generate --language "$(SDK_LANGUAGE)"

sdk-check: ## Check SDK drift, units, HTTP/TLS and package consumers (SDK_LANGUAGE=all|go|python|typescript|rust)
	$(PYTHON) -B test/sdk/runner.py check --language "$(SDK_LANGUAGE)"

sdk-package: ## Build local Go/Python/npm/Rust SDK packages without tests or publication (default all)
	$(PYTHON) -B test/sdk/runner.py package --language "$(SDK_LANGUAGE)"

# Optional example dependencies never enter the backend or SDK package.
LANGGRAPH_QUESTION ?=
LANGGRAPH_MODEL_ORIGIN ?=
LANGGRAPH_MODEL ?= qwen2.5:7b
LANGGRAPH_NAMESPACE_ID ?=
LANGGRAPH_ALLOW_LOOPBACK_HTTP ?= 0
unexport LANGGRAPH_QUESTION LANGGRAPH_MODEL_ORIGIN LANGGRAPH_MODEL LANGGRAPH_NAMESPACE_ID LANGGRAPH_ALLOW_LOOPBACK_HTTP
# Treat launcher inputs as literal data, including command-line assignments.
# value prevents Make expression evaluation; override keeps command-line values
# from bypassing the simply-expanded, target-scoped export.
langgraph-demo: override export LANGGRAPH_QUESTION := $(value LANGGRAPH_QUESTION)
langgraph-demo: override export LANGGRAPH_MODEL_ORIGIN := $(value LANGGRAPH_MODEL_ORIGIN)
langgraph-demo: override export LANGGRAPH_MODEL := $(value LANGGRAPH_MODEL)
langgraph-demo: override export LANGGRAPH_NAMESPACE_ID := $(value LANGGRAPH_NAMESPACE_ID)
langgraph-demo: override export LANGGRAPH_ALLOW_LOOPBACK_HTTP := $(value LANGGRAPH_ALLOW_LOOPBACK_HTTP)
test-langgraph-model: override export LANGGRAPH_MODEL_ORIGIN := $(value LANGGRAPH_MODEL_ORIGIN)
test-langgraph-model: override export LANGGRAPH_MODEL := $(value LANGGRAPH_MODEL)

.PHONY: langgraph-tools-install langgraph-demo test-langgraph-unit test-langgraph test-langgraph-model
langgraph-tools-install: ## Install the locked optional Python/LangGraph example environment (no models)
	$(PYTHON) -B test/langgraph/runner.py install --uv "$(UV)" --python "$(DEV_PYTHON)"

langgraph-demo: ## Ask the SDK-based read-only local-model assistant (LANGGRAPH_QUESTION and explicit SDK credentials)
	examples/langgraph/.venv/bin/python -B examples/langgraph/agent.py

test-langgraph-unit: ## Run isolated LangGraph example units and source coverage; no servers or model calls
	$(PYTHON) -B test/langgraph/runner.py unit

test-langgraph: ## Qualify the actual graph/SDK with an owned Catalog HTTP fixture and scripted model
	$(PYTHON) -B test/langgraph/runner.py check

test-langgraph-model: ## Add real local-model qualification; requires explicit LANGGRAPH_MODEL_ORIGIN
	$(PYTHON) -B test/langgraph/runner.py model

.PHONY: mcp-scanner-tools-install mcp-scanner-scan mcp-scanner-check test-mcp-scanner-compose
mcp-scanner-scan: override export MCP_SCANNER_INPUT := $(value MCP_SCANNER_INPUT)
mcp-scanner-scan: override export MCP_SCANNER_OUTPUT := $(value MCP_SCANNER_OUTPUT)
mcp-scanner-scan: override export MCP_SCANNER_SOURCE := $(value MCP_SCANNER_SOURCE)
mcp-scanner-scan: override export MCP_SCANNER_CAPTURED_AT := $(value MCP_SCANNER_CAPTURED_AT)
mcp-scanner-scan: override export MCP_SCANNER_CONTEXT := $(value MCP_SCANNER_CONTEXT)
mcp-scanner-tools-install: ## Build the locked optional MCP Scanner image; explicit downloads
	$(PYTHON) -B test/mcp-scanner/runner.py install

mcp-scanner-scan: ## Scan a saved tools/list capture offline with explicit MCP_SCANNER_* inputs
	$(PYTHON) -B test/mcp-scanner/runner.py scan

mcp-scanner-check: ## Test capture validation, owned subprocesses and the installed real YARA scanner
	$(PYTHON) -B test/mcp-scanner/runner.py check

test-mcp-scanner-compose: ## Test real release-webhook delivery, native YARA and recovery in one owned Compose project
	$(PYTHON) -B test/mcp-scanner/campaign.py run

.PHONY: vscode-tools-install vscode-check vscode-package test-vscode
vscode-tools-install: ## Install pinned optional VS Code extension tools and test host
	$(PYTHON) -B test/vscode/runner.py install

vscode-check: ## Check extension types, lint and isolated units without launching VS Code
	$(PYTHON) -B extensions/vscode/tooling/runner.py check

vscode-package: ## Build a self-contained local VSIX without tests or publication
	$(PYTHON) -B extensions/vscode/tooling/runner.py package

test-vscode: ## Qualify packaged extension discovery and native consumption with owned local peers
	$(PYTHON) -B test/vscode/runner.py test

# Package validation and real CLI reads; no assistant, model or external service.
client-skill-check: ## Validate the portable discovery Skill and its service-free CLI workflows
	$(GO) test -tags=functional ./cmd/catalogctl -run '^TestClientSkill' -shuffle=on -count=1

validation-tools-install: ## Explicitly install locked Redocly and build pinned oasdiff (network acquisition)
	node test/validation/runner.mjs install

api-yaml-check: ## Lint the authoritative OpenAPI YAML, including duplicate keys
	$(YAMLLINT) -c .yamllint.yml api/openapi/control-v1.yaml

api-validate: ## Run Go OpenAPI specification, example and event-selector parity tests
	$(GO) test ./api -count=1

api-contract-check: ## Run in-process HTTP and event contract tests without a database
	$(GO) test -tags=functional ./internal/platform/httpserver ./internal/events -count=1

api-lint: api-yaml-check ## Lint API-tooling JavaScript and Control OpenAPI with installed ESLint and Redocly
	npm --prefix test/validation run lint
	node test/validation/runner.mjs lint

api-check: api-validate api-lint ## Check API-tooling JavaScript, API YAML, Go contracts and Redocly rules

api-validation-test: ## Qualify the validation launcher and pinned tools with positive/negative fixtures
	node --test test/validation/*.test.mjs

api-diff api-breaking-check: export API_BASE := $(API_BASE)
api-diff: ## Report API changes against an explicit absolute API_BASE file
	node test/validation/runner.mjs diff

api-breaking-check: ## Fail breaking changes against an explicit absolute API_BASE file
	node test/validation/runner.mjs breaking

profiles: ## Verify all vendored standards files against pinned checksums
	$(GO) run ./cmd/catalogctl profiles verify

conformance-prerequisite: ## Require the pinned Python validator and usable ARD schema
	$(PYTHON) $(ARD_CONFORMANCE) check

conformance-fixture: conformance-prerequisite ## Validate ARD fixtures, ingestion, resolution, protocol behavior, and the upstream CLI
	$(GO) test -tags=functional ./internal/standards -run '^TestPinnedARDConformanceFixtures$$' -count=1
	$(GO) test -tags=functional ./internal/federation -run '^(TestOfficialARDExamplesUseARDIngestionProfile|TestDomainInspectionAlwaysTriesNormativeARDWellKnown|TestDomainInspectionTriesRequestedWellKnownBeforeFollowingRootRedirect|TestInspectionResolvesAdvertisedARDLocation|TestInspectionRejectsContradictoryAdvertisedProfile|TestInspectionRejectsConflictingRelationsForOneLocation|TestDiscoveryHelpersAreBoundedAndFailClosed|TestSynchronizationRejectsAChangedSourceProfile|TestOriginChangingResponseRequiresReinspectionAndCannotBecomeConditionalState|TestRegistryListRedirectRequiresReinspection)$$' -count=1
	$(GO) test -tags=functional ./internal/platform/httpserver -run '^(TestARDReferenceCompatibilityCorpus|TestARDAutoSearchTraversesEveryLocalResultAndQueriesLiveOnce|TestHTTPAuthoringPublicationSecurityAndReplay)$$' -count=1
	@set -eu; \
		for fixture in \
			'api/examples/ard-manifest.json|0' \
			'schemas/upstream/ard/ard-0.91-aa3e598/conformance/examples/basic/ard.json|2' \
			'schemas/upstream/ard/ard-0.91-aa3e598/conformance/examples/fda-ndc/fda-ndc-catalog.json|0' \
			'schemas/upstream/ard/ard-0.91-aa3e598/conformance/examples/local-business/local-business-catalog.json|0' \
			'schemas/upstream/ard/ard-0.91-aa3e598/conformance/examples/noaa-weather/noaa-weather-catalog.json|0'; do \
			path="$${fixture%|*}"; \
			expected_warnings="$${fixture##*|}"; \
			output="$$( $(PYTHON) $(ARD_CONFORMANCE) manifest "$$path" )"; \
			printf '%s\n' "$$output"; \
			case "$$output" in *"Validated with 0 critical specification errors and $$expected_warnings warnings."*) ;; \
				*) printf 'Unexpected ARD conformance result for %s; expected %s warnings.\n' "$$path" "$$expected_warnings" >&2; exit 1 ;; \
			esac; \
		done

conformance-live: conformance-prerequisite ## Validate all official modes against a deliberately selected running publication
	$(PYTHON) $(ARD_CONFORMANCE) manifest "$(ARD_MANIFEST_URL)"
	$(PYTHON) $(ARD_CONFORMANCE) publisher "$(ARD_PUBLISHER_URL)"
	$(PYTHON) $(ARD_CONFORMANCE) registry "$(ARD_REGISTRY_URL)"


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
##@ 🎨 Optional UI
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

.PHONY: ui-container-inspect ui-sbom ui-container-scan ui-container-build ui-install ui-deps-check ui-deps-audit ui-build
.PHONY: ui-coverage-e2e ui-lint ui-check ui-client-check ui-test-unit ui-test ui-coverage ui-coverage-100 ui-browser-install
.PHONY: ui-dead-code-report ui-dead-code-check
.PHONY: test-ui test-ui-compose test-ui-oidc test-mcp-http ui-dev ui-visual ui-visual-update

.PHONY: ui-test-property ui-test-mutation ui-performance

UI_PERFORMANCE_TIMEOUT_SCALE ?= 3
ui-performance: export UI_PERFORMANCE_TIMEOUT_SCALE := $(UI_PERFORMANCE_TIMEOUT_SCALE)
ui-performance: ## Measure production UI asset/work budgets and report browser timings; UI_PERFORMANCE_TIMEOUT_SCALE=1..10 (default 3)
	cd catalog-ui && node scripts/browser-test.mjs --performance

ui-test-property: ## Run isolated generated JSON cases; optional UI_PROPERTY_RUNS/SEED/PATH replay
	cd catalog-ui && npm run test:property

ui-test-mutation: ## Measure two UI helpers with bounded Stryker/Vitest runs and reviewed survivors
	cd catalog-ui && npm run test:mutation

ui-deps-check: export UI_DEPS_TRIVY = $(TRIVY)

ui-container-inspect: ## Inspect an existing UI_IMAGE, including host, licenses and React/manual hashes
	$(PYTHON) test/packaging/image.py inspect --profile ui

ui-sbom: ## Generate local UI_IMAGE SPDX inventory (Go host, frontend and manual browser suppliers)
	$(PYTHON) test/packaging/image.py sbom --profile ui

ui-container-scan: ## Scan exact local UI_IMAGE and browser inventories; fail on High/Critical findings
	$(PYTHON) test/packaging/image.py scan --profile ui

ui-container-build: ## Build the optional UI image; UI_IMAGE and CONTAINER_PLATFORM select tag/architecture
	@case "$(CONTAINER_PLATFORM)" in linux/amd64|linux/arm64) ;; *) printf '%s\n' 'UI platform must be linux/amd64 or linux/arm64.' >&2; exit 2 ;; esac
	$(CONTAINER_ENGINE) build --platform "$(CONTAINER_PLATFORM)" --target runtime --build-arg VERSION="$(VERSION)" --build-arg COMMIT="$(COMMIT)" --build-arg SOURCE_STATE="$(SOURCE_STATE)" -f catalog-ui/Containerfile -t "$(UI_IMAGE)" .

# The optional client has its own locked frontend tools; ordinary backend
# builds need no UI npm install. Host Go tests run in verify.
ui-install: ## Install the optional UI's locked build dependencies
	cd catalog-ui && npm ci --ignore-scripts --no-audit --no-fund

ui-deps-check: ## Scan the full UI lockfile with installed Trivy; fail on HIGH/CRITICAL advisories
	node catalog-ui/scripts/dependencies.mjs trivy

ui-deps-audit: ## Audit the full UI lockfile with npm's registry; fail on high/critical advisories
	node catalog-ui/scripts/dependencies.mjs audit

ui-build: ## Build browser assets and the static bin/catalog-ui host
	cd catalog-ui && npm run build
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build $(GO_BUILD_FLAGS) -ldflags '$(LDFLAGS)' -o bin/catalog-ui ./catalog-ui/cmd/catalog-ui

ui-lint: ## Check UI code/hooks, typed promises, test conventions and formatting without edits
	cd catalog-ui && npm run lint

ui-dead-code-report: ## Report unused UI files, exports and dependencies in full and browser scopes
	cd catalog-ui && npm run dead-code:report

ui-dead-code-check: ## Reject unused UI code/dependencies and analyzer failures in both scopes
	cd catalog-ui && npm run dead-code:check

ui-check: ## Run UI lint, dead-code, type checks and generated API drift checks
	cd catalog-ui && npm run check

ui-client-check: ## Verify UI types match the authoritative OpenAPI
	cd catalog-ui && npm run client-check

ui-test-unit: ## Run isolated UI component, Node helper and Go host unit tests
	cd catalog-ui && npm run test:unit
	$(GO) test ./catalog-ui/cmd/... ./catalog-ui/internal/... -shuffle=on -count=1

ui-test: ## Run UI coverage, local functional fixture/tooling tests and Go host tests
	cd catalog-ui && npm test
	$(GO) test -tags=functional ./catalog-ui/cmd/... ./catalog-ui/internal/... -shuffle=on -count=1

ui-coverage: ## Report frontend unit coverage with regression floors in output/coverage/catalog-ui
	cd catalog-ui && npm run test:coverage

# Coverage is an explicit functional selection, separate from component coverage.
UI_E2E_REPORTS ?=
.PHONY: ui-coverage-e2e-report
ui-coverage-e2e-report: ## Combine explicit successful browser runs; UI_E2E_REPORTS='path ...' (report only)
	node catalog-ui/scripts/e2e-report.mjs $(UI_E2E_REPORTS)

UI_E2E_PROFILE ?= local
ui-coverage-e2e: ## Measure Chromium source coverage; UI_E2E_PROFILE=local|compose|oidc|semantic (requires ui-install, ui-browser-install)
	node --test catalog-ui/tests/coverage/collector.functional.test.mjs
	@case "$(UI_E2E_PROFILE)" in \
	  local) cd catalog-ui && node scripts/browser-test.mjs --coverage ;; \
	  compose|oidc) cd catalog-ui && node scripts/packaged-test.mjs "$(UI_E2E_PROFILE)" --coverage ;; \
	  semantic) $(PYTHON) test/embeddings/semantic.py --coverage ;; \
	  *) printf '%s\n' 'UI_E2E_PROFILE must be local, compose, oidc or semantic.' >&2; exit 2 ;; \
	esac

ui-coverage-100: ## Check the full frontend 100% target; fails until all coverage gaps are closed
	cd catalog-ui && npm run test:coverage:100

ui-browser-install: ## Install the pinned Playwright Chromium browser for local UI tests
	cd catalog-ui && npm run browser-install

test-ui: ui-check ui-test ## Run all UI checks, unit tests, isolated build and Playwright journeys
	cd catalog-ui && npm run test:browser

test-ui-compose: ## Test packaged browser workflows, federation and webhooks on owned Compose PostgreSQL
	cd catalog-ui && node scripts/packaged-test.mjs compose

test-ui-oidc: ## Test UI and CLI human PKCE login/refresh with the pinned local issuer and owned Compose images
	cd catalog-ui && node scripts/packaged-test.mjs oidc

.PHONY: test-local-auth
test-local-auth: ## Test the documented local password launcher, accounts, access, restart and TLS renewal
	cd catalog-ui && node scripts/packaged-test.mjs oidc --local-passwords

test-mcp-http: ## Qualify packaged HTTP MCP with a separate public OAuth/PKCE client, TLS and current PostgreSQL permissions
	cd catalog-ui && node scripts/packaged-test.mjs oidc --mcp

ui-dev: ui-build ## Run the UI on loopback:4180 against the local API on port 8080
	node catalog-ui/scripts/dev.mjs

ui-visual: ## Compare opt-in Chromium visual baselines using private build/fixtures
	cd catalog-ui && node scripts/browser-test.mjs --visual

ui-visual-update: ## Explicitly regenerate visual baselines; review every changed PNG
	cd catalog-ui && node scripts/browser-test.mjs --visual --update-snapshots=all


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
##@ 📚 Documentation
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

.PHONY: docs-delivery docs-install docs-build docs-preview docs-site-check docs-check docs-pdf-browser-install docs-pdf test-docs-pdf

# Documentation dependencies stay separate from API validation and service builds.
# Install explicitly, then build/check. Preview serves existing output without rebuilding.
docs-install: ## Install locked documentation dependencies (Node/npm required; network access)
	cd documentation && npm ci --no-audit --no-fund

docs-build: ## Build documentation/dist from installed packages (DOCS_BASE defaults to /)
	cd documentation && ASTRO_TELEMETRY_DISABLED=1 DOCS_BASE="$(or $(DOCS_BASE),/)" npm run build

docs-preview: ## Serve existing docs on loopback:4321; use the build's DOCS_BASE, Ctrl-C to stop
	cd documentation && ASTRO_TELEMETRY_DISABLED=1 DOCS_BASE="$(or $(DOCS_BASE),/)" npm run preview

docs-pdf-browser-install: ## Explicitly acquire the pinned documentation Chromium headless shell
	@test -f documentation/node_modules/playwright/package.json || { echo "Run make docs-install before docs-pdf-browser-install." >&2; exit 1; }
	cd documentation && PLAYWRIGHT_BROWSERS_PATH=$(call shell-quote,$(CURDIR)/.tools/documentation-pdf) node node_modules/playwright/cli.js install chromium --only-shell

docs-delivery: ## Stage the completed public manual and matching PDF under ignored output; never upload
	cd documentation && ASTRO_TELEMETRY_DISABLED=1 DOCS_BASE="$(or $(DOCS_BASE),/)" node scripts/delivery.mjs

docs-pdf: ## Export the completed local manual to output/documentation-pdf/catalog-manual.pdf
	@test -f documentation/node_modules/playwright/package.json || { echo "Run make docs-install before docs-pdf." >&2; exit 1; }
	cd documentation && node scripts/pdf-export.mjs

test-docs-pdf: ## Qualify PDF export with the installed browser and local PDF inspection tools
	@test -f documentation/node_modules/playwright/package.json || { echo "Run make docs-install before test-docs-pdf." >&2; exit 1; }
	cd documentation && node --test scripts/pdf.functional.mjs

docs-site-check: ## Lint JS, check Astro, test root/base docs builds; leave root output ready to preview
	cd documentation && ASTRO_TELEMETRY_DISABLED=1 npm run check

docs-check: ## Lint Markdown and audit documentation plus tracked workflow defaults
	$(MARKDOWNLINT) --dot --ignore schemas/upstream --ignore samples/upstream --ignore specs --ignore documentation/node_modules --ignore documentation/.astro --ignore documentation/dist --ignore documentation/src/content/docs/generated --ignore catalog-ui/node_modules --ignore catalog-ui/dist --ignore test/validation/node_modules --ignore test/interoperability/mcp/node_modules --ignore test/validation/.tools --ignore test/apigenerated/.tools --ignore test/sdk/.tools --ignore examples/langgraph/.venv --ignore sdk/typescript/node_modules --ignore sdk/rust/target --ignore test/sdk/rust/consumer/target --ignore examples/rust-catalog-report/target --ignore extensions/vscode/node_modules --ignore extensions/vscode/.tools --ignore extensions/vscode/dist --ignore test/vscode/.tools --ignore .tools --ignore output '**/*.md' '**/*.mdx'
	$(GO) test ./internal/platform/runtime -run '^TestDocumentationContracts$$' -count=1


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
##@ 📦 Containers and local artifacts
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

.PHONY: container-sync-check container-check container-build container-verify container-inspect licenses
.PHONY: sbom docker-build container-run container-scan

container-sync-check: ## Verify compatibility files match the canonical container inputs
	@cmp -s "$(CONTAINERFILE)" "$(COMPAT_DOCKERFILE)" || { printf '%s\n' 'Containerfile and compatibility Dockerfile differ.' >&2; exit 1; }
	@cmp -s .containerignore .dockerignore || { printf '%s\n' '.containerignore and .dockerignore differ.' >&2; exit 1; }

container-check: container-sync-check ## Verify compatibility files and lint every Containerfile
	$(HADOLINT) "$(CONTAINERFILE)" "$(FUNCTIONAL_CONTAINERFILE)" catalog-ui/Containerfile test/ci/Containerfile.act test/interoperability/Containerfile test/embeddings/Containerfile test/interoperability/mcp/Containerfile test/interoperability/a2a/Containerfile test/interoperability/skills/Containerfile test/interoperability/plugins/Containerfile test/interoperability/agntcy/router/Containerfile test/interoperability/gateway/Containerfile.auth test/interoperability/ans/Containerfile test/mcp-scanner/Containerfile

container-build: container-sync-check ## Build the minimal OCI runtime image with Docker or Podman
	@case "$(CONTAINER_PLATFORM)" in linux/amd64|linux/arm64) ;; *) printf 'CONTAINER_PLATFORM must be linux/amd64 or linux/arm64, got %s\n' "$(CONTAINER_PLATFORM)" >&2; exit 2 ;; esac
	$(CONTAINER_ENGINE) build --platform "$(CONTAINER_PLATFORM)" --target runtime --build-arg VERSION="$(VERSION)" --build-arg COMMIT="$(COMMIT)" --build-arg SOURCE_STATE="$(SOURCE_STATE)" -f "$(CONTAINERFILE)" -t "$(IMAGE)" .

container-verify: container-build ## Build, inspect and execute the selected runtime image
	$(PYTHON) test/packaging/image.py verify

container-inspect: ## Inspect an existing local image without building or executing it
	$(PYTHON) test/packaging/image.py inspect

licenses: ## Extract /licenses from an existing local image into ARTIFACT_OUTPUT
	$(PYTHON) test/packaging/image.py licenses

sbom: ## Generate SPDX JSON from an existing local image using Trivy (no registry fallback)
	$(PYTHON) test/packaging/image.py sbom

docker-build: container-build ## Compatibility alias for container-build

container-run: ## Run IMAGE locally with the hardened, in-memory development profile
	@test -f "$(CATALOG_CONTAINER_CONFIG_FILE)" || { printf 'CATALOG_CONTAINER_CONFIG_FILE must identify a regular file, got %s\n' "$(CATALOG_CONTAINER_CONFIG_FILE)" >&2; exit 2; }
	$(CONTAINER_ENGINE) run --rm --pull=never --init --read-only --cap-drop ALL --security-opt no-new-privileges:true --pids-limit 256 --stop-timeout 15 -p 127.0.0.1:8080:8080 --tmpfs /tmp:rw,noexec,nosuid,nodev,size=64m --mount "type=bind,src=$(abspath $(CATALOG_CONTAINER_CONFIG_FILE)),dst=/config/config.json,readonly" -e CATALOG_HTTP_ADDRESS=0.0.0.0:8080 "$(IMAGE)" all --config /config/config.json

container-scan: ## Inspect and scan the exact local IMAGE/platform; retain private evidence and fail on High/Critical findings
	$(PYTHON) test/packaging/image.py scan


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
##@ 🐳 Local Compose environment
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

.PHONY: compose-check compose-up compose-stop compose-down compose-reset compose-ps compose-monitoring-reset
.PHONY: compose-logs

compose-check compose-up: export VERSION = $(word 1,$(BUILD_IDENTITY))
compose-check compose-up: export COMMIT = $(word 2,$(BUILD_IDENTITY))
compose-check compose-up: export SOURCE_STATE = $(word 3,$(BUILD_IDENTITY))
compose-check compose-up: export IMAGE := $(IMAGE)

compose-check: ## Validate every maintained Compose model without starting containers
	@DEMO_A2A_IMAGE=open-agentic-catalog:a2a-check DEMO_MCP_IMAGE=open-agentic-catalog:mcp-check DEMO_RUNTIME_UID=1001 DEMO_RUNTIME_GID=1001 $(COMPOSE) --env-file /dev/null -f compose.yaml -f compose.demo-runtimes.yaml -p open-agentic-catalog-runtime-check config --quiet
	@DEMO_ARCHIVE_UID=1001 DEMO_ARCHIVE_GID=1001 CATALOG_UI_INPUT_DIRECTORY="$(CURDIR)/config" $(COMPOSE) --env-file /dev/null -f compose.yaml -f compose.demo.yaml -f compose.ui.yaml -p open-agentic-catalog-demo-check config --quiet
	@CATALOG_FUNCTIONAL_SUFFIX=compose-check CATALOG_FUNCTIONAL_COMMIT=compose-check CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY="$(CURDIR)/test/functional" $(COMPOSE) --env-file /dev/null -f compose.yaml -f test/security/compose.yaml -p open-agentic-catalog-security-check config --quiet
	@CATALOG_EMBEDDING_IMAGE=open-agentic-catalog:compose-check-embeddings CATALOG_EMBEDDING_MODEL=fixture CATALOG_EMBEDDING_REVISION=fixture CATALOG_EMBEDDING_MODEL_DIRECTORY="$(CURDIR)/test/embeddings" IMAGE=open-agentic-catalog:compose-check CATALOG_COMPOSE_HTTP_PORT=18080 CATALOG_COMPOSE_POSTGRES_PORT=0 $(COMPOSE) --env-file /dev/null -f compose.yaml -f compose.embeddings.yaml -f test/embeddings/semantic.compose.yaml -f test/embeddings/semantic_restore.compose.yaml --profile semantic-restore -p open-agentic-catalog-semantic-check config --quiet
	@CATALOG_EMBEDDING_IMAGE=open-agentic-catalog:compose-check-embeddings CATALOG_EMBEDDING_MODEL=fixture CATALOG_EMBEDDING_REVISION=fixture CATALOG_EMBEDDING_MODEL_DIRECTORY="$(CURDIR)/test/embeddings" IMAGE=open-agentic-catalog:compose-check CATALOG_COMPOSE_HTTP_PORT=18080 CATALOG_COMPOSE_POSTGRES_PORT=0 $(COMPOSE) --env-file /dev/null -f compose.yaml -f compose.embeddings.yaml -f test/embeddings/semantic.compose.yaml -f test/embeddings/semantic_restore.compose.yaml -f test/embeddings/semantic_restored_runtime.compose.yaml --profile semantic-restore -p open-agentic-catalog-semantic-check config --quiet
	@CATALOG_FUNCTIONAL_ARCHIVE_UID=1001 CATALOG_FUNCTIONAL_ARCHIVE_GID=1001 CATALOG_FUNCTIONAL_ARCHIVE_IP=172.31.255.252 IMAGE=open-agentic-catalog:compose-check CATALOG_FUNCTIONAL_PEER_HTTP_PORT=18081 CATALOG_COMPOSE_HTTP_PORT=18080 CATALOG_COMPOSE_POSTGRES_PORT=0 CATALOG_COMPOSE_FIXTURE_PORT=18443 CATALOG_COMPOSE_SUBNET=172.31.255.240/28 CATALOG_FUNCTIONAL_FIXTURE_IMAGE=open-agentic-catalog:compose-check-fixture CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY="$(CURDIR)/test/functional" $(COMPOSE) --env-file /dev/null -f compose.yaml -f test/functional/compose.yaml -f test/functional/peers.compose.yaml -p open-agentic-catalog-functional-check config --quiet
	@CATALOG_FUNCTIONAL_ARCHIVE_UID=1001 CATALOG_FUNCTIONAL_ARCHIVE_GID=1001 CATALOG_FUNCTIONAL_ARCHIVE_IP=172.31.255.252 IMAGE=open-agentic-catalog:compose-check CATALOG_COMPOSE_HTTP_PORT=18080 CATALOG_COMPOSE_POSTGRES_PORT=0 CATALOG_COMPOSE_FIXTURE_PORT=18443 CATALOG_COMPOSE_SUBNET=172.31.255.240/28 CATALOG_FUNCTIONAL_FIXTURE_IMAGE=open-agentic-catalog:compose-check-fixture CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY="$(CURDIR)/test/functional" $(COMPOSE) --env-file /dev/null -f compose.yaml -f test/functional/compose.yaml -f test/functional/webhooks.compose.yaml -f test/functional/backup_restore.compose.yaml --profile backup-restore -p open-agentic-catalog-functional-check config --quiet
	@IMAGE=open-agentic-catalog:compose-check CATALOG_COMPOSE_HTTP_PORT=18080 CATALOG_COMPOSE_POSTGRES_PORT=0 CATALOG_COMPOSE_FIXTURE_PORT=18443 CATALOG_COMPOSE_SUBNET=172.31.255.240/28 CATALOG_FUNCTIONAL_FIXTURE_IMAGE=open-agentic-catalog:compose-check-fixture CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY="$(CURDIR)/test/functional" $(COMPOSE) --env-file /dev/null -f compose.yaml -f test/functional/compose.yaml -f test/functional/failover.compose.yaml --profile failover -p open-agentic-catalog-functional-check config --quiet
	$(COMPOSE) --env-file /dev/null -f test/tracing/viewer.compose.yaml -p open-agentic-catalog-tracing-check config --quiet
	@CATALOG_FUNCTIONAL_TRACING='{"enabled":false}' IMAGE=open-agentic-catalog:compose-check CATALOG_COMPOSE_HTTP_PORT=18080 CATALOG_COMPOSE_POSTGRES_PORT=0 CATALOG_COMPOSE_FIXTURE_PORT=18443 CATALOG_COMPOSE_SUBNET=172.31.255.240/28 CATALOG_FUNCTIONAL_FIXTURE_IMAGE=open-agentic-catalog:compose-check-fixture CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY="$(CURDIR)/test/functional" $(COMPOSE) --env-file /dev/null -f compose.yaml -f test/functional/compose.yaml -f test/functional/tracing.compose.yaml -p open-agentic-catalog-functional-check config --quiet
	$(PYTHON) test/monitoring/launcher.py check open-agentic-catalog-monitoring-check on
	$(PYTHON) test/performance/runner.py check
	$(COMPOSE) --env-file /dev/null -f compose.yaml -p open-agentic-catalog-compose-check config --quiet
	CATALOG_COMPOSE_WEBHOOK_DIRECTORY=./config $(COMPOSE) --env-file /dev/null -f compose.yaml -f compose.webhooks.yaml -p open-agentic-catalog-webhooks-check config --quiet
	$(COMPOSE) --env-file /dev/null -f compose.integration.yaml -p open-agentic-catalog-integration-check config --quiet
	@IMAGE=open-agentic-catalog:compose-check CATALOG_COMPOSE_HTTP_PORT=18080 CATALOG_COMPOSE_POSTGRES_PORT=0 CATALOG_COMPOSE_FIXTURE_PORT=18443 CATALOG_COMPOSE_SUBNET=172.31.255.240/28 CATALOG_FUNCTIONAL_FIXTURE_IMAGE=open-agentic-catalog:compose-check-fixture CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY="$(CURDIR)/test/functional" $(COMPOSE) --env-file /dev/null -f compose.yaml -f test/functional/compose.yaml -p open-agentic-catalog-functional-check config --quiet
	@IMAGE=open-agentic-catalog:compose-check CATALOG_COMPOSE_HTTP_PORT=18080 CATALOG_COMPOSE_POSTGRES_PORT=0 CATALOG_COMPOSE_FIXTURE_PORT=18443 CATALOG_COMPOSE_SUBNET=172.31.255.240/28 CATALOG_FUNCTIONAL_FIXTURE_IMAGE=open-agentic-catalog:compose-check-fixture CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY="$(CURDIR)/test/functional" $(COMPOSE) --env-file /dev/null -f compose.yaml -f test/functional/compose.yaml -f test/functional/oidc-trust.compose.yaml -p open-agentic-catalog-functional-check config --quiet
	@IMAGE=open-agentic-catalog:compose-check CATALOG_COMPOSE_HTTP_PORT=18080 CATALOG_COMPOSE_POSTGRES_PORT=0 CATALOG_COMPOSE_FIXTURE_PORT=18443 CATALOG_COMPOSE_SUBNET=172.31.255.240/28 CATALOG_FUNCTIONAL_FIXTURE_IMAGE=open-agentic-catalog:compose-check-fixture CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY="$(CURDIR)/test/functional" $(COMPOSE) --env-file /dev/null -f compose.yaml -f test/functional/compose.yaml -f test/functional/compose.credentials.yaml -p open-agentic-catalog-functional-check config --quiet
	@IMAGE=open-agentic-catalog:compose-check CATALOG_COMPOSE_HTTP_PORT=18080 CATALOG_COMPOSE_POSTGRES_PORT=0 CATALOG_COMPOSE_FIXTURE_PORT=18443 CATALOG_COMPOSE_SUBNET=172.31.255.240/28 CATALOG_FUNCTIONAL_FIXTURE_IMAGE=open-agentic-catalog:compose-check-fixture CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY="$(CURDIR)/test/functional" CATALOG_FUNCTIONAL_OIDC_PORT=18444 CATALOG_FUNCTIONAL_OIDC_HOSTS='[]' $(COMPOSE) --env-file /dev/null -f compose.yaml -f test/functional/compose.yaml -f test/functional/compose.oidc.yaml -p open-agentic-catalog-functional-check config --quiet
	@IMAGE=open-agentic-catalog:compose-check CATALOG_COMPOSE_HTTP_PORT=18080 CATALOG_COMPOSE_POSTGRES_PORT=0 CATALOG_COMPOSE_FIXTURE_PORT=18443 CATALOG_COMPOSE_SUBNET=172.31.255.240/28 CATALOG_FUNCTIONAL_FIXTURE_IMAGE=open-agentic-catalog:compose-check-fixture CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY="$(CURDIR)/test/functional" $(COMPOSE) --env-file /dev/null -f compose.yaml -f test/functional/compose.yaml -f test/functional/webhooks.compose.yaml -p open-agentic-catalog-functional-check config --quiet

compose-up compose-stop compose-down compose-reset compose-ps compose-logs compose-monitoring-reset demo-runtimes-prepare demo-runtimes-test: export LOCAL_DEMO_RUNTIMES := $(LOCAL_DEMO_RUNTIMES)
compose-up compose-stop compose-down compose-reset compose-ps compose-logs compose-monitoring-reset demo-runtimes-prepare demo-runtimes-test: export DEMO_RUNTIME_DIR := $(DEMO_RUNTIME_DIR)
compose-up compose-stop compose-down compose-reset compose-ps compose-logs compose-monitoring-reset demo-runtimes-prepare demo-runtimes-test: export DEMO_A2A_PORT := $(DEMO_A2A_PORT)
compose-up compose-stop compose-down compose-reset compose-ps compose-logs compose-monitoring-reset demo-runtimes-prepare demo-runtimes-test: export DEMO_MCP_PORT := $(DEMO_MCP_PORT)
compose-up compose-stop compose-down compose-reset compose-ps compose-logs compose-monitoring-reset: export CATALOG_LOCAL_PROJECT = $(COMPOSE_PROJECT)
compose-up compose-stop compose-down compose-reset compose-ps compose-logs compose-monitoring-reset: export LOCAL_DEMO_ARCHIVES := $(LOCAL_DEMO_ARCHIVES)
compose-up compose-stop compose-down compose-reset compose-ps compose-logs compose-monitoring-reset: export DEMO_ARCHIVE_DIR := $(DEMO_ARCHIVE_DIR)
compose-up compose-stop compose-down compose-reset compose-ps compose-logs compose-monitoring-reset: export DEMO_ARCHIVE_IP := $(DEMO_ARCHIVE_IP)
compose-up compose-stop compose-down compose-reset compose-ps compose-logs compose-monitoring-reset: export DEMO_ARCHIVE_PORT := $(DEMO_ARCHIVE_PORT)
compose-up compose-stop compose-down compose-reset compose-ps compose-logs compose-monitoring-reset: export LOCAL_MONITORING := $(LOCAL_MONITORING)
compose-check compose-up compose-stop compose-down compose-reset compose-ps compose-logs compose-monitoring-reset: export LOCAL_UI := $(LOCAL_UI)
compose-check compose-up compose-stop compose-down compose-reset compose-ps compose-logs compose-monitoring-reset: export LOCAL_EMBEDDINGS := $(LOCAL_EMBEDDINGS)
compose-check compose-up compose-stop compose-down compose-reset compose-ps compose-logs compose-monitoring-reset: export EMBEDDING_PROFILE := $(EMBEDDING_PROFILE)
compose-check compose-up compose-stop compose-down compose-reset compose-ps compose-logs compose-monitoring-reset: export LOCAL_WEBHOOKS := $(LOCAL_WEBHOOKS)
compose-check compose-up compose-stop compose-down compose-reset compose-ps compose-logs compose-monitoring-reset: export UI_IMAGE := $(UI_IMAGE)
compose-check compose-up compose-stop compose-down compose-reset compose-ps compose-logs compose-monitoring-reset: export CATALOG_UI_PORT := $(CATALOG_UI_PORT)
compose-check compose-up compose-stop compose-down compose-reset compose-ps compose-logs compose-monitoring-reset: export CATALOG_LOCAL_COMPOSE = $(COMPOSE)
compose-check compose-up compose-stop compose-down compose-reset compose-ps compose-logs compose-monitoring-reset: export CATALOG_LOCAL_ENGINE = $(CONTAINER_ENGINE)

compose-up: ## Start Catalog; LOCAL_UI=on adds UI, LOCAL_MONITORING=on adds dashboards, LOCAL_EMBEDDINGS=on adds a verified local model, LOCAL_DEMO_ARCHIVES=on adds prepared demo archives; LOCAL_DEMO_RUNTIMES=on adds locked A2A/MCP peers
	$(PYTHON) test/monitoring/launcher.py up

compose-stop: ## Stop the local Compose services without removing them
	$(PYTHON) test/monitoring/launcher.py stop

compose-down: ## Remove local Compose containers and network but retain data volumes
	$(PYTHON) test/monitoring/launcher.py down

compose-reset: ## Remove local Compose containers, network, and development data volumes
	$(PYTHON) test/monitoring/launcher.py reset

compose-ps: ## Show local Compose service health
	$(PYTHON) test/monitoring/launcher.py ps

compose-monitoring-reset: ## Remove only this project's monitoring containers and history; preserve PostgreSQL
	$(PYTHON) test/monitoring/launcher.py reset-monitoring

compose-logs: ## Follow all local Compose service logs
	$(PYTHON) test/monitoring/launcher.py logs


.PHONY: local-auth-up local-auth-down
LOCAL_AUTH_DIRECTORY ?= $(HOME)/.local/share/open-agentic-catalog/local-auth
local-auth-up: ## Start the isolated local Keycloak password setup using existing backend/UI images
	$(PYTHON) examples/tutorials/local-oidc/local_auth.py up --directory "$(LOCAL_AUTH_DIRECTORY)" --backend-image "$(IMAGE)" --ui-image "$(UI_IMAGE)"

local-auth-down: ## Remove only the password setup containers/network; retain accounts and data
	$(PYTHON) examples/tutorials/local-oidc/local_auth.py down --directory "$(LOCAL_AUTH_DIRECTORY)"

# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
##@ 🔬 Packaged functional and recovery scenarios
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

.PHONY: test-functional test-functional-oidc test-functional-default-off test-functional-webhooks
.PHONY: trace-viewer-up trace-viewer-down test-tracing test-federation-peers test-backup-restore
.PHONY: test-postgres-failover test-postgres-pitr-prepare test-postgres-pitr test-functional-workflow

test-functional: test-functional-default-off test-functional-webhooks test-functional-oidc ## Exercise default-off, webhook and independent OIDC profiles in owned Compose stacks

test-functional-oidc: ## Qualify one exact independent HTTPS identity provider and Catalog tenancy mappings
	@CATALOG_FUNCTIONAL_COMPOSE_COMMAND="$(COMPOSE)" CATALOG_FUNCTIONAL_ENGINE_COMMAND="$(CONTAINER_ENGINE)" CATALOG_FUNCTIONAL_MAKE_COMMAND="$(MAKE)" $(PYTHON) test/functional/runner.py run-oidc

test-functional-default-off: ## Run default-off container end-to-end tests in an isolated Compose stack
	@CATALOG_FUNCTIONAL_COMPOSE_COMMAND="$(COMPOSE)" CATALOG_FUNCTIONAL_ENGINE_COMMAND="$(CONTAINER_ENGINE)" CATALOG_FUNCTIONAL_MAKE_COMMAND="$(MAKE)" $(PYTHON) test/functional/runner.py run

test-functional-webhooks: ## Exercise owned HTTPS receivers, webhook safety, recovery and key maintenance in Compose
	@CATALOG_FUNCTIONAL_COMPOSE_COMMAND="$(COMPOSE)" CATALOG_FUNCTIONAL_ENGINE_COMMAND="$(CONTAINER_ENGINE)" CATALOG_FUNCTIONAL_MAKE_COMMAND="$(MAKE)" $(PYTHON) test/functional/runner.py run-webhooks

TRACE_VIEWER_UI_PORT ?= 16686
TRACE_VIEWER_OTLP_PORT ?= 14318
trace-viewer-up: export TRACE_VIEWER_UI_PORT := $(TRACE_VIEWER_UI_PORT)
trace-viewer-up: export TRACE_VIEWER_OTLP_PORT := $(TRACE_VIEWER_OTLP_PORT)
trace-viewer-up: ## Start the disposable loopback-only Jaeger trace viewer (no Catalog stack)
	$(COMPOSE) --env-file /dev/null -f test/tracing/viewer.compose.yaml -p open-agentic-catalog-trace-viewer up -d

trace-viewer-down: ## Remove the local Jaeger viewer and discard its in-memory traces
	$(COMPOSE) --env-file /dev/null -f test/tracing/viewer.compose.yaml -p open-agentic-catalog-trace-viewer down

TRACING_VIEWER ?= off
test-tracing: export CATALOG_TRACING_VIEWER := $(TRACING_VIEWER)
test-tracing: ## Verify optional HTTPS OTLP export, causal Jobs and outage independence in owned Compose
	@CATALOG_FUNCTIONAL_COMPOSE_COMMAND="$(COMPOSE)" CATALOG_FUNCTIONAL_ENGINE_COMMAND="$(CONTAINER_ENGINE)" CATALOG_FUNCTIONAL_MAKE_COMMAND="$(MAKE)" $(PYTHON) test/functional/runner.py run-tracing

test-federation-peers: ## Test four-kind federation, lifecycle, pagination and UI/CLI draft transfer
	@PYTHONDONTWRITEBYTECODE=1 PYTHONOPTIMIZE=0 CATALOG_FUNCTIONAL_COMPOSE_COMMAND="$(COMPOSE)" CATALOG_FUNCTIONAL_ENGINE_COMMAND="$(CONTAINER_ENGINE)" CATALOG_FUNCTIONAL_MAKE_COMMAND="$(MAKE)" $(PYTHON) test/functional/runner.py run-peers

test-backup-restore: ## Restore a full logical backup into fresh PostgreSQL and verify Catalog recovery
	@PYTHONDONTWRITEBYTECODE=1 PYTHONOPTIMIZE=0 CATALOG_FUNCTIONAL_COMPOSE_COMMAND="$(COMPOSE)" CATALOG_FUNCTIONAL_ENGINE_COMMAND="$(CONTAINER_ENGINE)" CATALOG_FUNCTIONAL_MAKE_COMMAND="$(MAKE)" $(PYTHON) test/functional/runner.py run-backup-restore

test-postgres-failover: ## Test planned and abrupt PostgreSQL writer changes in isolated Compose
	@PYTHONDONTWRITEBYTECODE=1 PYTHONOPTIMIZE=0 CATALOG_FUNCTIONAL_COMPOSE_COMMAND="$(COMPOSE)" CATALOG_FUNCTIONAL_ENGINE_COMMAND="$(CONTAINER_ENGINE)" CATALOG_FUNCTIONAL_MAKE_COMMAND="$(MAKE)" $(PYTHON) test/functional/runner.py run-failover

test-postgres-pitr-prepare: ## Verify an isolated PostgreSQL base backup and WAL archive without starting recovery
	@PYTHONDONTWRITEBYTECODE=1 PYTHONOPTIMIZE=0 CATALOG_FUNCTIONAL_COMPOSE_COMMAND="$(COMPOSE)" CATALOG_FUNCTIONAL_ENGINE_COMMAND="$(CONTAINER_ENGINE)" $(PYTHON) test/functional/runner.py run-pitr-prepare

test-postgres-pitr: ## Restore a Catalog physical backup to a named point and reject missing required WAL
	@PYTHONDONTWRITEBYTECODE=1 PYTHONOPTIMIZE=0 CATALOG_FUNCTIONAL_COMPOSE_COMMAND="$(COMPOSE)" CATALOG_FUNCTIONAL_ENGINE_COMMAND="$(CONTAINER_ENGINE)" CATALOG_FUNCTIONAL_MAKE_COMMAND="$(PITR_MAKE_COMMAND)" $(PYTHON) test/functional/runner.py run-pitr-recovery

# Internal fixed workflow: the supervisor alone owns selection and cleanup.
test-functional-workflow: conformance-prerequisite
	@set -eu; \
		test -n "$${CATALOG_FUNCTIONAL_STATE_DIRECTORY:-}" || { printf '%s\n' 'Use make test-functional.' >&2; exit 2; }; \
		suffix="$${CATALOG_FUNCTIONAL_SUFFIX:?}"; \
		project="open-agentic-catalog-functional-$$suffix"; \
		image="$${CATALOG_FUNCTIONAL_CANDIDATE_IMAGE:-open-agentic-catalog:functional-$$suffix}"; \
		fixture_image="open-agentic-catalog:functional-$$suffix-fixture"; \
		fixture_directory="$${CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY:?}"; \
		run_id="functional-$$suffix"; \
		subnet="$${CATALOG_FUNCTIONAL_SUBNET:?}"; \
		http_port="$${CATALOG_FUNCTIONAL_HTTP_PORT:?}"; \
		fixture_port="$${CATALOG_COMPOSE_FIXTURE_PORT:?}"; \
		failover_overlay=""; \
		case "$${CATALOG_FUNCTIONAL_PROFILE:-default-off}" in failover-planned|failover-abrupt) failover_overlay='-f test/functional/failover.compose.yaml';; esac; \
		backup_overlay=""; \
		peer_overlay=""; \
		security_overlay=""; \
		generated_overlay=""; \
		if [ "$${CATALOG_FUNCTIONAL_PROFILE:-default-off}" = generated-api ]; then generated_overlay='-f test/functional/generated-api.compose.yaml'; fi; \
		case "$${CATALOG_FUNCTIONAL_PROFILE:-}" in security-*) security_overlay='-f test/security/compose.yaml'; $(PYTHON) -B test/functional/security_scan.py setup;; esac; \
		if [ "$${CATALOG_FUNCTIONAL_PROFILE:-default-off}" = peers ]; then peer_overlay='-f test/functional/peers.compose.yaml'; fi; \
		case "$${CATALOG_FUNCTIONAL_PROFILE:-default-off}" in backup-restore|pitr-recovery) backup_overlay='-f test/functional/webhooks.compose.yaml -f test/functional/backup_restore.compose.yaml';; esac; \
		if [ "$${CATALOG_FUNCTIONAL_PROFILE:-}" = pitr-recovery ]; then backup_overlay="$$backup_overlay -f test/functional/pitr_recovery.compose.yaml"; fi; \
		$(GO) build -o "$$fixture_directory/response-contract" ./test/functional/contracts; \
		export CATALOG_FUNCTIONAL_CONTRACT_DRIVER="$$fixture_directory/response-contract"; \
		export CATALOG_FUNCTIONAL_OPERATION_COVERAGE=on; \
		"$$CATALOG_FUNCTIONAL_CONTRACT_DRIVER" --operations > "$$CATALOG_FUNCTIONAL_STATE_DIRECTORY/operations.json"; \
		catalog_compose() { \
			IMAGE="$$image" VERSION="$${CATALOG_FUNCTIONAL_VERSION:-functional}" SOURCE_STATE="$${CATALOG_FUNCTIONAL_SOURCE_STATE:-unknown}" COMMIT="$${CATALOG_FUNCTIONAL_COMMIT:?}" CATALOG_COMPOSE_HTTP_PORT="$$http_port" CATALOG_COMPOSE_POSTGRES_PORT=0 CATALOG_COMPOSE_FIXTURE_PORT="$$fixture_port" CATALOG_COMPOSE_SUBNET="$$subnet" CATALOG_COMPOSE_FEDERATION_POLICY_FILE="$$fixture_directory/policy.json" CATALOG_COMPOSE_FEDERATION_TRUST_DIRECTORY="$$fixture_directory/trust" CATALOG_FUNCTIONAL_FIXTURE_IMAGE="$$fixture_image" CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY="$$fixture_directory" CATALOG_HTTP_REQUEST_TIMEOUT=3s CATALOG_WORKER_SOURCE_MAINTENANCE_INTERVAL=1m CATALOG_WORKER_HOUSEKEEPING_INTERVAL=1m CATALOG_WORKER_TERMINAL_JOB_RETENTION=24h CATALOG_WORKER_ACKNOWLEDGED_OUTBOX_RETENTION=1h $(COMPOSE) --env-file /dev/null -f compose.yaml -f test/functional/compose.yaml $$failover_overlay $$backup_overlay $$peer_overlay $$security_overlay $$generated_overlay -p "$$project" "$$@"; \
		}; \
		request_status() { \
			$(CURL) --silent --connect-timeout 1 --max-time 3 --output /dev/null --write-out '%{http_code}' "$$1" 2>/dev/null || true; \
		}; \
		wait_http_status() { \
			expected_status="$$1"; \
			endpoint="$$2"; \
			label="$$3"; \
			attempt=0; \
			actual_status=000; \
			while [ "$$attempt" -lt 90 ]; do \
				actual_status="$$(request_status "$$endpoint")"; \
				if [ "$$actual_status" = "$$expected_status" ]; then return 0; fi; \
				attempt=$$((attempt + 1)); \
				sleep 1; \
			done; \
			printf '%s timed out waiting for HTTP %s; last status was %s.\n' "$$label" "$$expected_status" "$$actual_status" >&2; \
			return 1; \
		}; \
		wait_ready() { \
			wait_http_status 200 "$$base_url/readyz" 'Functional API readiness'; \
		}; \
		wait_worker_status() { \
			expected_status="$$1"; \
			path="$$2"; \
			label="$$3"; \
			attempt=0; \
			while [ "$$attempt" -lt 90 ]; do \
				if catalog_compose exec -T worker /usr/local/bin/catalogctl probe --url "http://127.0.0.1:9090/$$path" --expect-status "$$expected_status" >/dev/null 2>&1; then return 0; fi; \
				attempt=$$((attempt + 1)); \
				sleep 1; \
			done; \
			printf '%s timed out waiting for worker HTTP %s.\n' "$$label" "$$expected_status" >&2; \
			return 1; \
		}; \
		wait_worker_ready() { \
			wait_worker_status 200 readyz 'Functional worker readiness'; \
		}; \
		start_namespace_lock() { \
			catalog_compose exec -T postgres psql --username catalog --dbname catalog --set ON_ERROR_STOP=1 --quiet --command "BEGIN; LOCK TABLE namespaces IN ACCESS EXCLUSIVE MODE; SELECT pg_sleep(6); COMMIT" >/dev/null & \
			namespace_lock_pid=$$!; \
			attempt=0; \
			namespace_lock_count=0; \
			while [ "$$attempt" -lt 100 ]; do \
				namespace_lock_count="$$(catalog_compose exec -T postgres psql --username catalog --dbname catalog --tuples-only --no-align --command "SELECT count(*) FROM pg_locks WHERE relation='namespaces'::regclass AND mode='AccessExclusiveLock' AND granted")"; \
				if [ "$$namespace_lock_count" = 1 ]; then return 0; fi; \
				attempt=$$((attempt + 1)); \
				sleep 0.05; \
			done; \
			printf '%s\n' 'Functional request-deadline lock was not acquired.' >&2; \
			return 1; \
		}; \
		wait_for_api_database_lock() { \
			attempt=0; \
			api_lock_waiters=0; \
			while [ "$$attempt" -lt 40 ]; do \
				api_lock_waiters="$$(catalog_compose exec -T postgres psql --username catalog --dbname catalog --tuples-only --no-align --command "SELECT count(*) FROM pg_stat_activity WHERE usename='catalog_api' AND wait_event_type='Lock'")"; \
				if [ "$$api_lock_waiters" -ge 1 ]; then return 0; fi; \
				attempt=$$((attempt + 1)); \
				sleep 0.05; \
			done; \
			printf '%s\n' 'Functional API did not reach the controlled database wait.' >&2; \
			return 1; \
		}; \
		run_smoke() { \
			catalog_compose exec -T api /usr/local/bin/catalogctl smoke --base-url http://127.0.0.1:8080 --run-id "$$run_id" --timeout 3m --admin-token local-admin --contributor-token local-contributor --reviewer-token local-reviewer --publisher-token local-publisher --reader-token local-reader --other-tenant-token other-tenant-admin; \
		}; \
		smoke_fingerprint() { \
			$(PYTHON) -c 'import json, sys; print(json.dumps(json.load(sys.stdin), sort_keys=True, separators=(",", ":")))'; \
		}; \
		set --; \
		case "$${CATALOG_FUNCTIONAL_PROFILE:-default-off}" in webhooks|backup-restore|pitr-recovery) set -- --webhooks;; esac; \
		$(GO) run ./test/functional/fixture generate --directory "$$fixture_directory" --subnet "$$subnet" "$$@"; \
		if [ -n "$$peer_overlay$$backup_overlay" ]; then CGO_ENABLED=0 $(GO) build -o "$$fixture_directory/demo-archives" ./test/demo; fi; \
		if [ -n "$$peer_overlay" ]; then $(PYTHON) test/functional/peers.py prepare; fi; \
		if [ -n "$$backup_overlay" ]; then $(PYTHON) test/functional/backup_restore.py prepare; fi; \
		if [ "$${CATALOG_FUNCTIONAL_PROFILE:-}" = webhooks ]; then \
			$(PYTHON) test/functional/publication_report.py prepare; \
			GOWORK=off CGO_ENABLED=0 $(GO) -C examples/publication-report build -mod=readonly -trimpath -o "$$fixture_directory/publication-report/receiver" .; \
		fi; \
		printf 'Starting isolated functional stack %s on 127.0.0.1:%s.\n' "$$project" "$$http_port"; \
		if [ -n "$${CATALOG_FUNCTIONAL_CANDIDATE_IMAGE:-}" ]; then \
			catalog_compose pull --policy missing postgres; \
			catalog_compose build federation-fixture; \
			catalog_compose up --no-build --pull never --force-recreate --detach; \
		else catalog_compose up --build --force-recreate --detach; fi; \
		fixture_connect=""; \
		if [ -n "$$security_overlay" ]; then \
			container_ip() { $(CONTAINER_ENGINE) inspect --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$$(catalog_compose ps -q "$$1")"; }; \
			api_address="$$(container_ip api):8080"; \
			fixture_address="127.0.0.1:$$fixture_port"; \
			fixture_connect="--connect-to 127.0.0.1:$$fixture_port:$$(container_ip federation-fixture):8443"; \
		else \
			api_address="$$(catalog_compose port api 8080)"; \
			postgres_address="$$(catalog_compose port postgres 5432)"; \
			fixture_address="$$(catalog_compose port federation-fixture 8443)"; \
			test "$$api_address" = "127.0.0.1:$$http_port" || { printf 'Unexpected functional API address: %s\n' "$$api_address" >&2; exit 1; }; \
			test "$$fixture_address" = "127.0.0.1:$$fixture_port" || { printf 'Unexpected functional fixture address: %s\n' "$$fixture_address" >&2; exit 1; }; \
			case "$$postgres_address" in 127.0.0.1:[0-9]*) ;; *) printf 'Unexpected functional PostgreSQL address: %s\n' "$$postgres_address" >&2; exit 1 ;; esac; \
		fi; \
		base_url="http://$$api_address"; \
		export CATALOG_FUNCTIONAL_BASE_URL="$$base_url"; \
		fixture_url="https://$$fixture_address"; \
		wait_ready; \
		wait_worker_ready; \
		wait_worker_status 200 startupz 'Functional worker startup'; \
		wait_worker_status 200 livez 'Functional worker liveness'; \
		attempt=0; \
		until $(CURL) --fail --silent --show-error --connect-timeout 2 --max-time 5 --cacert "$$fixture_directory/trust/functional-ca.pem" $$fixture_connect "$$fixture_url/healthz" >/dev/null 2>&1; do \
			attempt=$$((attempt + 1)); \
			if [ "$$attempt" -ge 30 ]; then printf '%s\n' 'Functional federation fixture did not become ready.' >&2; exit 1; fi; \
			sleep 1; \
		done; \
		catalog_compose ps --status running --services postgres | grep -Fx postgres >/dev/null; \
		catalog_compose ps --status running --services api | grep -Fx api >/dev/null; \
		catalog_compose ps --status running --services worker | grep -Fx worker >/dev/null; \
		catalog_compose ps --status running --services federation-fixture | grep -Fx federation-fixture >/dev/null; \
		version_report="$$( $(CURL) --fail --silent --show-error --connect-timeout 2 --max-time 10 "$$base_url/version" )"; \
		printf '%s' "$$version_report" | $(PYTHON) test/functional/runner.py identity; \
		$(CURL) --fail --silent --show-error --connect-timeout 2 --max-time 10 "$$base_url/openapi/control-v1.yaml" | cmp - api/openapi/control-v1.yaml; \
		$(PYTHON) test/functional/runner.py mark startup-contract; \
		if [ -n "$$peer_overlay" ]; then \
			CATALOG_FUNCTIONAL_BASE_URL="$$base_url" CATALOG_FUNCTIONAL_FIXTURE_PORT="$$fixture_port" $(PYTHON) test/functional/peers.py; \
			$(PYTHON) test/functional/runner.py mark federation-peers; \
			exit 0; \
		fi; \
		if [ -n "$$backup_overlay" ]; then \
			smoke_report="$$(run_smoke)"; \
			printf '%s\n' "$$smoke_report" | $(PYTHON) test/functional/runner.py smoke; \
			$(PYTHON) test/functional/runner.py mark packaged-smoke; \
			backup_driver=test/functional/backup_restore.py; \
			if [ "$${CATALOG_FUNCTIONAL_PROFILE:-}" = pitr-recovery ]; then backup_driver=test/functional/pitr_recovery.py; fi; \
			CATALOG_FUNCTIONAL_BASE_URL="$$base_url" CATALOG_FUNCTIONAL_FIXTURE_URL="$$fixture_url" CATALOG_FUNCTIONAL_FIXTURE_PORT="$$fixture_port" $(PYTHON) "$$backup_driver"; \
			$(PYTHON) test/functional/runner.py mark backup-recovery; \
			exit 0; \
		fi; \
		if [ -n "$$failover_overlay" ]; then \
			smoke_report="$$(run_smoke)"; \
			printf '%s\n' "$$smoke_report" | $(PYTHON) test/functional/runner.py smoke; \
			$(PYTHON) test/functional/runner.py mark packaged-smoke; \
			CATALOG_FUNCTIONAL_BASE_URL="$$base_url" $(PYTHON) test/functional/failover.py; \
			$(PYTHON) test/functional/runner.py mark writer-recovery; \
			exit 0; \
		fi; \
		case "$${CATALOG_FUNCTIONAL_PROFILE:-}" in security-*) \
			smoke_report="$$(run_smoke)"; \
			printf '%s\n' "$$smoke_report" | $(PYTHON) test/functional/runner.py smoke; \
			$(PYTHON) test/functional/runner.py mark packaged-smoke; \
			catalog_compose exec -d zap python3 /zap/wrk/calibration.py; \
			test "$$( $(CONTAINER_ENGINE) network inspect --format '{{.Internal}}' "$${project}_default" )" = true; \
			proxy_host="$$(container_ip zap)"; \
			CATALOG_SECURITY_PROXY_HOST="$$proxy_host" $(PYTHON) -B test/functional/security_scan.py run; \
			$(PYTHON) test/functional/runner.py mark security-scan; \
			exit 0;; \
		esac; \
		if [ "$${CATALOG_FUNCTIONAL_PROFILE:-default-off}" = generated-api ]; then \
			smoke_report="$$(run_smoke)"; \
			printf '%s\n' "$$smoke_report" | $(PYTHON) test/functional/runner.py smoke; \
			$(PYTHON) test/functional/runner.py mark packaged-smoke; \
			CATALOG_FUNCTIONAL_BASE_URL="$$base_url" $(PYTHON) test/functional/generated_api.py run; \
			$(PYTHON) test/functional/runner.py mark generated-api; \
			exit 0; \
		fi; \
		if [ "$${CATALOG_FUNCTIONAL_PROFILE:-default-off}" = oidc ]; then \
			smoke_report="$$(run_smoke)"; \
			printf '%s\n' "$$smoke_report" | $(PYTHON) test/functional/runner.py smoke; \
			$(PYTHON) test/functional/runner.py mark packaged-smoke; \
			CATALOG_FUNCTIONAL_BASE_URL="$$base_url" $(PYTHON) test/functional/oidc.py; \
			$(PYTHON) test/functional/runner.py mark independent-oidc; \
			exit 0; \
		fi; \
		if [ "$${CATALOG_FUNCTIONAL_PROFILE:-default-off}" = webhooks ]; then \
			smoke_report="$$(run_smoke)"; \
			printf '%s\n' "$$smoke_report" | $(PYTHON) test/functional/runner.py smoke; \
			$(PYTHON) test/functional/runner.py mark packaged-smoke; \
			CATALOG_FUNCTIONAL_PROJECT="$$project" CATALOG_FUNCTIONAL_IMAGE="$$image" CATALOG_FUNCTIONAL_FIXTURE_PORT="$$fixture_port" CATALOG_FUNCTIONAL_BASE_URL="$$base_url" CATALOG_FUNCTIONAL_FIXTURE_URL="$$fixture_url" $(PYTHON) test/functional/webhooks.py; \
			$(PYTHON) test/functional/runner.py mark enabled-webhooks; \
			exit 0; \
		fi; \
		if [ "$${CATALOG_FUNCTIONAL_PROFILE:-default-off}" = tracing ]; then \
			smoke_report="$$(run_smoke)"; \
			printf '%s\n' "$$smoke_report" | $(PYTHON) test/functional/runner.py smoke; \
			$(PYTHON) test/functional/runner.py mark packaged-smoke; \
			$(GO) build -o "$$fixture_directory/trace-decode" ./test/tracing/decode; \
			CATALOG_FUNCTIONAL_FIXTURE_PORT="$$fixture_port" CATALOG_FUNCTIONAL_BASE_URL="$$base_url" CATALOG_FUNCTIONAL_FIXTURE_URL="$$fixture_url" $(PYTHON) test/functional/tracing.py; \
			$(PYTHON) test/functional/runner.py mark enabled-tracing; \
			exit 0; \
		fi; \
		$(PYTHON) test/functional/federation.py --base-url "$$base_url" --fixture-url "$$fixture_url" --ca-file "$$fixture_directory/trust/functional-ca.pem" --run-id "$$run_id" >"$$fixture_directory/federation-report.json"; \
		cat "$$fixture_directory/federation-report.json"; \
		$(PYTHON) test/functional/runner.py mark automatic-federation; \
		smoke_report="$$(run_smoke)"; \
		printf '%s\n' "$$smoke_report" | $(PYTHON) test/functional/runner.py smoke; \
		$(PYTHON) test/functional/runner.py mark packaged-smoke; \
		document_id="$$(printf '%s' "$$smoke_report" | $(PYTHON) -c 'import json,sys; print(next(item["documentId"] for item in json.load(sys.stdin)["resources"] if item.get("documentId")))')"; \
		$(PYTHON) test/functional/response_contract.py "$$base_url" "$$document_id"; \
		$(PYTHON) test/functional/journeys.py --base-url "$$base_url" --run-id "$$run_id"; \
		$(PYTHON) test/functional/runner.py mark representative-response-contracts; \
		audit_export_file="$$fixture_directory/audit-export.json"; \
		$(PYTHON) test/functional/audit.py --base-url "$$base_url" --output "$$audit_export_file" --run-id "$$run_id"; \
		$(GO) run ./cmd/catalogctl audit verify-export --file "$$audit_export_file" >/dev/null; \
		recovery_point="$$run_id-recovery-point"; \
		audit_manifest_file="$$fixture_directory/audit-manifest.json"; \
		$(GO) run ./cmd/catalogctl audit bind-manifest --file "$$audit_export_file" --recovery-point "$$recovery_point" > "$$audit_manifest_file"; \
		chmod 0444 "$$audit_manifest_file"; \
		$(PYTHON) test/functional/runner.py mark audit-export; \
		CATALOG_FUNCTIONAL_BASE_URL="$$base_url" $(PYTHON) test/functional/ard_checker.py; \
		$(PYTHON) test/functional/runner.py mark ard-conformance; \
		start_namespace_lock; \
		$(PYTHON) test/functional/deadline.py --base-url "$$base_url" --operation read --expect deadline & \
		deadline_request_pid=$$!; \
		wait_for_api_database_lock; \
		wait "$$deadline_request_pid"; \
		wait "$$namespace_lock_pid"; \
		$(PYTHON) test/functional/deadline.py --base-url "$$base_url" --operation read --expect success; \
		start_namespace_lock; \
		$(PYTHON) test/functional/deadline.py --base-url "$$base_url" --operation command --expect deadline --run-id "$$run_id" & \
		deadline_command_pid=$$!; \
		wait_for_api_database_lock; \
		deadline_api_id="$$(catalog_compose ps --all -q api)"; \
		catalog_compose stop --timeout 20 api & \
		deadline_stop_pid=$$!; \
		attempt=0; \
		drain_status=200; \
		while [ "$$attempt" -lt 40 ]; do \
			drain_status="$$(request_status "$$base_url/readyz")"; \
			if [ "$$drain_status" != 200 ]; then break; fi; \
			attempt=$$((attempt + 1)); \
			sleep 0.05; \
		done; \
		test "$$drain_status" != 200 || { printf '%s\n' 'Functional API remained ready during drain.' >&2; exit 1; }; \
		wait "$$deadline_command_pid"; \
		wait "$$deadline_stop_pid"; \
		wait "$$namespace_lock_pid"; \
		deadline_api_exit="$$( $(CONTAINER_ENGINE) container inspect --format '{{.State.ExitCode}}' "$$deadline_api_id" )"; \
		test "$$deadline_api_exit" = 0 || { printf 'Functional API exited with status %s during bounded in-flight drain.\n' "$$deadline_api_exit" >&2; exit 1; }; \
		catalog_compose start api; \
		wait_ready; \
		$(PYTHON) test/functional/deadline.py --base-url "$$base_url" --operation command --expect deadline-replay --run-id "$$run_id"; \
		$(PYTHON) test/functional/deadline.py --base-url "$$base_url" --operation command --expect success --run-id "$$run_id" --key-variant success; \
		$(PYTHON) test/functional/deadline.py --base-url "$$base_url" --operation command --expect success-replay --run-id "$$run_id" --key-variant success; \
		$(PYTHON) test/functional/runner.py mark command-drain-replay; \
		initial_smoke_fingerprint="$$(printf '%s' "$$smoke_report" | smoke_fingerprint)"; \
		document_id="$$(printf '%s' "$$smoke_report" | $(PYTHON) -c 'import json, sys; resources = json.load(sys.stdin)["resources"]; documents = [resource.get("documentId") for resource in resources if resource.get("documentId")]; sys.stdout.write(documents[0]) if len(documents) == 1 else sys.exit("smoke report must contain exactly one document")')"; \
		catalog_compose exec -T postgres psql --username catalog --dbname catalog --set ON_ERROR_STOP=1 --quiet --command "ALTER ROLE catalog_api SET default_transaction_read_only=on; ALTER ROLE catalog_worker SET default_transaction_read_only=on" >/dev/null; \
		catalog_compose exec -T postgres psql --username catalog --dbname catalog --set ON_ERROR_STOP=1 --quiet --command "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename IN ('catalog_api','catalog_worker') AND pid <> pg_backend_pid()" >/dev/null; \
		wait_http_status 503 "$$base_url/readyz" 'Functional API read-only readiness'; \
		wait_worker_status 503 readyz 'Functional worker read-only readiness'; \
		wait_worker_status 200 livez 'Functional worker liveness during read-only PostgreSQL sessions'; \
		liveness_status="$$(request_status "$$base_url/livez")"; \
		test "$$liveness_status" = 200 || { printf 'API liveness returned %s during read-only PostgreSQL sessions.\n' "$$liveness_status" >&2; exit 1; }; \
		catalog_compose ps --status running --services api | grep -Fx api >/dev/null; \
		catalog_compose ps --status running --services worker | grep -Fx worker >/dev/null; \
		catalog_compose exec -T postgres psql --username catalog --dbname catalog --set ON_ERROR_STOP=1 --quiet --command "ALTER ROLE catalog_api RESET default_transaction_read_only; ALTER ROLE catalog_worker RESET default_transaction_read_only" >/dev/null; \
		wait_ready; \
		wait_worker_ready; \
		$(CONTAINER_ENGINE) inspect --format '[{{json .Id}},{{.State.Pid}},{{json .State.StartedAt}},{{.RestartCount}}]' "$$(catalog_compose ps -q api)" "$$(catalog_compose ps -q worker)" | $(PYTHON) -c 'import json,sys; json.dump([json.loads(line) for line in sys.stdin], sys.stdout)' >"$$fixture_directory/process-before.json"; \
		catalog_compose stop --timeout 10 postgres; \
		wait_http_status 503 "$$base_url/readyz" 'Functional API dependency readiness'; \
		wait_worker_status 503 readyz 'Functional worker dependency readiness'; \
		wait_worker_status 200 livez 'Functional worker liveness during PostgreSQL outage'; \
		$(PYTHON) test/functional/operations.py --url "http://$$(catalog_compose port worker 9090)" --commit "$${CATALOG_FUNCTIONAL_COMMIT:?}" --ready no --phase degraded; \
		liveness_status="$$(request_status "$$base_url/livez")"; \
		test "$$liveness_status" = 200 || { printf 'API liveness returned %s during the PostgreSQL outage.\n' "$$liveness_status" >&2; exit 1; }; \
		catalog_compose ps --status running --services api | grep -Fx api >/dev/null; \
		catalog_compose ps --status running --services worker | grep -Fx worker >/dev/null; \
		catalog_compose ps --status exited --services postgres | grep -Fx postgres >/dev/null; \
		catalog_compose start postgres; \
		wait_ready; \
		wait_worker_ready; \
		catalog_compose ps --status running --services postgres | grep -Fx postgres >/dev/null; \
		catalog_compose ps --status running --services api | grep -Fx api >/dev/null; \
		catalog_compose ps --status running --services worker | grep -Fx worker >/dev/null; \
		$(PYTHON) test/functional/operations.py --url "http://$$(catalog_compose port worker 9090)" --commit "$${CATALOG_FUNCTIONAL_COMMIT:?}" --ready yes --phase recovered --base-url "$$base_url" --source-report "$$fixture_directory/federation-report.json"; \
		$(CONTAINER_ENGINE) inspect --format '[{{json .Id}},{{.State.Pid}},{{json .State.StartedAt}},{{.RestartCount}}]' "$$(catalog_compose ps -q api)" "$$(catalog_compose ps -q worker)" | $(PYTHON) -c 'import json,sys; json.dump([json.loads(line) for line in sys.stdin], sys.stdout)' >"$$fixture_directory/process-after.json"; \
		$(PYTHON) test/functional/operations.py --commit "$${CATALOG_FUNCTIONAL_COMMIT:?}" --before "$$fixture_directory/process-before.json" --after "$$fixture_directory/process-after.json"; \
		$(PYTHON) test/functional/runner.py mark database-recovery; \
		CATALOG_FUNCTIONAL_PROJECT="$$project" CATALOG_FUNCTIONAL_IMAGE="$$image" CATALOG_FUNCTIONAL_FIXTURE_IMAGE="$$fixture_image" CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY="$$fixture_directory" CATALOG_FUNCTIONAL_SUBNET="$$subnet" CATALOG_FUNCTIONAL_HTTP_PORT="$$http_port" CATALOG_FUNCTIONAL_FIXTURE_PORT="$$fixture_port" CATALOG_FUNCTIONAL_COMPOSE_COMMAND="$(COMPOSE)" sh test/functional/housekeeping.sh; \
		wait_worker_ready; \
		$(PYTHON) test/functional/runner.py mark automatic-housekeeping; \
		initial_postgres_id="$$(catalog_compose ps --all -q postgres)"; \
		initial_api_id="$$(catalog_compose ps --all -q api)"; \
		initial_worker_id="$$(catalog_compose ps --all -q worker)"; \
		initial_migrate_id="$$(catalog_compose ps --all -q migrate)"; \
		initial_provision_id="$$(catalog_compose ps --all -q provision)"; \
		catalog_compose up --no-build --force-recreate --detach; \
		wait_ready; \
		wait_worker_ready; \
		test "$$(catalog_compose ps --all -q postgres)" != "$$initial_postgres_id" || { printf '%s\n' 'Functional PostgreSQL container was not recreated.' >&2; exit 1; }; \
		test "$$(catalog_compose ps --all -q api)" != "$$initial_api_id" || { printf '%s\n' 'Functional API container was not recreated.' >&2; exit 1; }; \
		test "$$(catalog_compose ps --all -q worker)" != "$$initial_worker_id" || { printf '%s\n' 'Functional worker container was not recreated.' >&2; exit 1; }; \
		test "$$(catalog_compose ps --all -q migrate)" != "$$initial_migrate_id" || { printf '%s\n' 'Functional migration job was not recreated.' >&2; exit 1; }; \
		test "$$(catalog_compose ps --all -q provision)" != "$$initial_provision_id" || { printf '%s\n' 'Functional identity-provisioning job was not recreated.' >&2; exit 1; }; \
		test "$$(catalog_compose port api 8080)" = "127.0.0.1:$$http_port" || { printf '%s\n' 'Functional API address changed after re-creation.' >&2; exit 1; }; \
		catalog_compose ps --status running --services postgres | grep -Fx postgres >/dev/null; \
		catalog_compose ps --status running --services api | grep -Fx api >/dev/null; \
		catalog_compose ps --status running --services worker | grep -Fx worker >/dev/null; \
		catalog_compose ps --status exited --services migrate | grep -Fx migrate >/dev/null; \
		catalog_compose ps --status exited --services provision | grep -Fx provision >/dev/null; \
		replayed_smoke_report="$$(run_smoke)"; \
		replayed_smoke_fingerprint="$$(printf '%s' "$$replayed_smoke_report" | smoke_fingerprint)"; \
		test "$$replayed_smoke_fingerprint" = "$$initial_smoke_fingerprint" || { printf '%s\n' 'Durable workflow replay changed after stack re-creation.' >&2; exit 1; }; \
		$(PYTHON) test/functional/runner.py mark recreation-replay; \
		current_api_id="$$(catalog_compose ps --all -q api)"; \
		current_worker_id="$$(catalog_compose ps --all -q worker)"; \
		catalog_compose stop --timeout 20 api worker; \
		catalog_compose ps --status exited --services api | grep -Fx api >/dev/null; \
		catalog_compose ps --status exited --services worker | grep -Fx worker >/dev/null; \
		api_exit_code="$$( $(CONTAINER_ENGINE) container inspect --format '{{.State.ExitCode}}' "$$current_api_id" )"; \
		worker_exit_code="$$( $(CONTAINER_ENGINE) container inspect --format '{{.State.ExitCode}}' "$$current_worker_id" )"; \
		test "$$api_exit_code" = 0 || { printf 'Functional API exited with status %s after SIGTERM.\n' "$$api_exit_code" >&2; exit 1; }; \
		test "$$worker_exit_code" = 0 || { printf 'Functional worker exited with status %s after SIGTERM.\n' "$$worker_exit_code" >&2; exit 1; }; \
		CATALOG_FUNCTIONAL_PROJECT="$$project" CATALOG_FUNCTIONAL_IMAGE="$$image" CATALOG_FUNCTIONAL_FIXTURE_IMAGE="$$fixture_image" CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY="$$fixture_directory" CATALOG_FUNCTIONAL_SUBNET="$$subnet" CATALOG_FUNCTIONAL_HTTP_PORT="$$http_port" CATALOG_FUNCTIONAL_FIXTURE_PORT="$$fixture_port" CATALOG_FUNCTIONAL_BASE_URL="$$base_url" CATALOG_FUNCTIONAL_FIXTURE_URL="$$fixture_url" CATALOG_FUNCTIONAL_RECOVERY_POINT="$$recovery_point" CATALOG_FUNCTIONAL_AUDIT_MANIFEST="$$audit_manifest_file" CATALOG_FUNCTIONAL_COMPOSE_COMMAND="$(COMPOSE)" CATALOG_FUNCTIONAL_PYTHON_COMMAND="$$( $(PYTHON) -I -c 'import sys; print(sys.executable)' )" sh test/functional/restore.sh; \
		$(PYTHON) test/functional/runner.py mark restore-repair; \
		catalog_compose up --no-deps --no-build --no-recreate --detach worker api; \
		wait_ready; \
		wait_worker_ready; \
		test "$$(catalog_compose ps --all -q api)" = "$$current_api_id" || { printf '%s\n' 'Functional API restart unexpectedly replaced its container.' >&2; exit 1; }; \
		test "$$(catalog_compose ps --all -q worker)" = "$$current_worker_id" || { printf '%s\n' 'Functional worker restart unexpectedly replaced its container.' >&2; exit 1; }; \
		catalog_compose ps --status running --services postgres | grep -Fx postgres >/dev/null; \
		catalog_compose ps --status running --services api | grep -Fx api >/dev/null; \
		catalog_compose ps --status running --services worker | grep -Fx worker >/dev/null; \
		manifest="$$( $(CURL) --fail --silent --show-error --connect-timeout 2 --max-time 10 "$$base_url/.well-known/ard.json" )"; \
		for segment in order-agent order-mcp order-skill; do \
			expected="urn:air:$$run_id.catalog.example.com:readiness:$$segment-$$run_id"; \
			printf '%s' "$$manifest" | grep -F "$$expected" >/dev/null || { printf 'Recreated stack omitted persisted identifier: %s\n' "$$expected" >&2; exit 1; }; \
		done; \
		document="$$( $(CURL) --fail --silent --show-error --connect-timeout 2 --max-time 10 "$$base_url/public/v1/artifacts/$$document_id" )"; \
		printf '%s' "$$document" | grep -F 'Inspect the order evidence' >/dev/null || { printf '%s\n' 'Recreated stack did not retain the published Skill document.' >&2; exit 1; }; \
		document_status="$$( $(CURL) --fail --silent --show-error --connect-timeout 2 --max-time 10 --output /dev/null --write-out '%{http_code}' --header "If-None-Match: \"$$document_id\"" "$$base_url/public/v1/artifacts/$$document_id" )"; \
		test "$$document_status" = 304 || { printf 'Conditional document read returned %s after re-creation.\n' "$$document_status" >&2; exit 1; }; \
		$(PYTHON) test/functional/runner.py mark role-restart; \
		CATALOG_FUNCTIONAL_BASE_URL="$$base_url" $(PYTHON) test/functional/operators.py; \
		for role in api worker; do \
			catalog_compose exec -T "$$role" /usr/local/bin/catalogctl diagnostics --config /config/config.json --role "$$role" | $(PYTHON) test/functional/operations.py --diagnostics "$$role" --commit "$${CATALOG_FUNCTIONAL_COMMIT:?}"; \
		done; \
		$(PYTHON) test/functional/runner.py mark operator-workflows; \
		$(PYTHON) test/functional/core.py --base-url "$$base_url" --run-id "$$run_id" --with-source; \
		$(PYTHON) test/functional/runner.py mark extended-core; \
		CATALOG_FUNCTIONAL_BASE_URL="$$base_url" $(PYTHON) test/functional/credentials.py; \
		$(PYTHON) test/functional/core.py --base-url "$$base_url" --run-id "$$run_id" --verify; \
		$(PYTHON) test/functional/runner.py mark credential-transport; \
		current_worker_id="$$(catalog_compose ps --all -q worker)"; \
		catalog_compose -f test/functional/oidc-trust.compose.yaml up --no-deps --no-build --detach federation-fixture; \
		$(CURL) --fail --silent --show-error --retry 20 --retry-all-errors --retry-max-time 30 --retry-delay 1 --connect-timeout 2 --max-time 5 --cacert "$$fixture_directory/trust/functional-ca.pem" "https://127.0.0.1:$$fixture_port/.well-known/openid-configuration" >/dev/null; \
		if catalog_compose -f test/functional/oidc-trust.compose.yaml run --rm --no-deps -e CATALOG_AUTH_TRUST_BUNDLE_FILE=/not-mounted/ca.pem api > "$$fixture_directory/oidc-untrusted.log" 2>&1; then printf '%s\n' 'OIDC startup accepted a missing trust bundle.' >&2; exit 1; fi; \
		grep -F 'OIDC trust bundle must be a readable regular file' "$$fixture_directory/oidc-untrusted.log" >/dev/null || { printf '%s\n' 'Missing OIDC trust did not fail at the file boundary.' >&2; exit 1; }; \
		catalog_compose -f test/functional/oidc-trust.compose.yaml up --no-deps --no-build --detach api; \
		wait_ready; \
		wait_worker_ready; \
		test "$$(catalog_compose ps --all -q worker)" = "$$current_worker_id" || { printf '%s\n' 'OIDC trust phase unexpectedly replaced the worker.' >&2; exit 1; }; \
		$(PYTHON) test/functional/runner.py mark oidc-private-trust; \
		printf '%s\n' 'Functional Compose workflow, bounded request and atomic-command drain, exact replay and retry, audit export and restore qualification, selective repair, read/write readiness, dependency recovery, re-creation persistence, clean role restart, and scoped OIDC private-HTTPS discovery checks passed.'


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
##@ 🚢 Helm, Minikube and monitoring
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

.PHONY: helm-package helm-lint helm-package-test helm-test helm-check helm-render helm-schema-check
.PHONY: deploy-check monitoring-package monitoring-schema-check monitoring-install monitoring-status
.PHONY: monitoring-access monitoring-uninstall test-minikube-contract test-minikube test-minikube-topology
.PHONY: test-minikube-monitoring test-minikube-tracing test-minikube-semantic test-minikube-ui
.PHONY: test-minikube-replicas test-minikube-upgrade test-minikube-peers test-minikube-backup-restore

HELM_CONTEXT ?=
HELM_NAMESPACE ?=
HELM_RELEASE ?=
HELM_VALUES ?=
HELM_DEPLOY_TIMEOUT ?= 10m

.PHONY: helm-deploy helm-deploy-check
helm-deploy: ## Drain, migrate and start the explicitly selected Helm release with bounded waits
helm-deploy-check: ## Validate selected Helm inputs and ownership without changing workloads
helm-deploy helm-deploy-check:
	$(GO) run ./test/deployment --context "$(HELM_CONTEXT)" --namespace "$(HELM_NAMESPACE)" --release "$(HELM_RELEASE)" \
		--chart "$(HELM_CHART)" --values "$(HELM_VALUES)" --helm "$(HELM)" --kubectl "$(KUBECTL)" \
		--timeout "$(HELM_DEPLOY_TIMEOUT)" $(if $(filter helm-deploy-check,$@),--check,)

helm-package: ## Package both versioned Helm charts into ARTIFACT_OUTPUT
	$(PYTHON) test/packaging/charts.py

helm-lint: ## Strictly lint the default and every representative Helm profile
	$(HELM) lint --strict --kube-version "$(HELM_KUBE_VERSION)" $(HELM_CHART)
	@set -eu; for values in $(HELM_CHECK_VALUES); do $(HELM) lint --strict --kube-version "$(HELM_KUBE_VERSION)" --values "$$values" $(HELM_CHART); done

helm-package-test: ## Test versioned chart packaging with Helm; no cluster required
	HELM="$(HELM)" $(PYTHON) -B test/helm/test_functional_packaging.py

helm-test: helm-package-test ## Verify rendered chart semantics, configuration, rejection cases, and package contents
	HELM="$(HELM)" HELM_CHART="$(abspath $(HELM_CHART))" HELM_KUBE_VERSION="$(HELM_KUBE_VERSION)" GOTOOLCHAIN=$(GO_TOOLCHAIN) $(GO) test -tags=helm ./test/helm ./test/deployment -shuffle=on -count=1

helm-check: helm-lint helm-test ## Run the complete cluster-free Helm chart contract gate

helm-render: ## Render the Helm chart to standard output
	$(HELM) template open-agentic-catalog $(HELM_CHART) --namespace open-agentic-catalog --kube-version "$(HELM_KUBE_VERSION)"

helm-schema-check: ## Validate representative Helm renders; requires schema-source access
	@set -eu; \
		render_directory="$$(mktemp -d /tmp/open-agentic-catalog-helm-schema.XXXXXX)"; \
		finish() { status=$$?; trap - 0; cleanup_status=0; rm -r -- "$$render_directory" || cleanup_status=$$?; if [ "$$status" -eq 0 ] && [ "$$cleanup_status" -ne 0 ]; then status=$$cleanup_status; fi; exit "$$status"; }; \
		trap finish 0; \
		trap 'exit 129' 1; \
		trap 'exit 130' 2; \
		trap 'exit 143' 15; \
		$(HELM) template default $(HELM_CHART) --namespace open-agentic-catalog --kube-version "$(HELM_KUBE_VERSION)" >"$$render_directory/default.yaml"; \
		index=0; for values in $(HELM_CHECK_VALUES); do index=$$((index + 1)); $(HELM) template "profile-$$index" $(HELM_CHART) --namespace open-agentic-catalog --kube-version "$(HELM_KUBE_VERSION)" --values "$$values" >"$$render_directory/profile-$$index.yaml"; done; \
		$(HELM) template upgrade $(HELM_CHART) --namespace open-agentic-catalog --kube-version "$(HELM_KUBE_VERSION)" --is-upgrade --values $(HELM_CHART)/ci/optional-values.yaml >"$$render_directory/upgrade.yaml"; \
		cat "$$render_directory"/*.yaml | $(CONTAINER_ENGINE) run --rm -i $(KUBECONFORM_IMAGE) -strict -summary -kubernetes-version "$(HELM_KUBE_VERSION)"

deploy-check: helm-check helm-schema-check monitoring-schema-check container-check ## Run the complete static container and Kubernetes manifest gate

monitoring-package: ## Package the optional monitoring chart with the shared pinned assets
	$(PYTHON) test/monitoring/helm.py package

monitoring-schema-check: monitoring-package ## Validate the packaged optional monitoring Kubernetes manifests
	@set -eu; rendered="$$(mktemp)"; trap 'rm -f "$$rendered"' EXIT; \
		archive="$$($(PYTHON) test/monitoring/helm.py package)"; \
		$(HELM) template monitor "$$archive" --namespace monitor --kube-version "$(HELM_KUBE_VERSION)" \
		--set enabled=true --set target.release=catalog --set target.deploymentLabel=check --set grafana.adminSecret.name=admin > "$$rendered"; \
		$(CONTAINER_ENGINE) run --rm -i $(KUBECONFORM_IMAGE) -strict -summary -kubernetes-version "$(HELM_KUBE_VERSION)" < "$$rendered"

monitoring-install: ## Install/upgrade the optional chart; requires explicit context, namespace and values
monitoring-status: ## Inspect only the explicitly selected monitoring release
monitoring-access: ## Open a loopback-only port-forward to the selected monitoring release
monitoring-uninstall: ## Remove only the selected monitoring release, retaining external claims and credentials

monitoring-install monitoring-status monitoring-access monitoring-uninstall:
	$(PYTHON) test/monitoring/helm.py $(patsubst monitoring-%,%,$@) --helm "$(HELM)" --kubectl "$(KUBECTL)" \
		--context "$(MONITORING_CONTEXT)" --namespace "$(MONITORING_NAMESPACE)" --release "$(MONITORING_RELEASE)" \
		--values "$(MONITORING_VALUES)" --component "$(MONITORING_COMPONENT)" --port "$(MONITORING_PORT)"

test-minikube-contract: ## Test the local Minikube harness without creating a cluster
	GOTOOLCHAIN=$(GO_TOOLCHAIN) $(GO) test -tags=minikube,functional ./test/minikube -shuffle=on -count=1

define run_minikube_scenario
	CATALOG_MINIKUBE_BIN="$(MINIKUBE)" CATALOG_KUBECTL_BIN="$(KUBECTL)" CATALOG_HELM_BIN="$(HELM)" CATALOG_CONTAINER_ENGINE_BIN="docker" \
	CATALOG_MINIKUBE_VERSION="$(MINIKUBE_VERSION)" CATALOG_MINIKUBE_KUBERNETES_VERSION="$(MINIKUBE_KUBERNETES_VERSION)" \
	CATALOG_MINIKUBE_DRIVER="$(MINIKUBE_DRIVER)" CATALOG_MINIKUBE_CONTAINER_RUNTIME="$(MINIKUBE_CONTAINER_RUNTIME)" \
	CATALOG_MINIKUBE_BASE_IMAGE="$(MINIKUBE_BASE_IMAGE)" CATALOG_MINIKUBE_POSTGRES_IMAGE="$(MINIKUBE_POSTGRES_IMAGE)" \
	CATALOG_MINIKUBE_PRELOAD_ARCHIVE="$(MINIKUBE_PRELOAD_ARCHIVE)" CATALOG_MINIKUBE_TIMEOUT="$(MINIKUBE_TIMEOUT)" \
	GOTOOLCHAIN=$(GO_TOOLCHAIN) $(GO) run -tags=minikube ./test/minikube --scenario $(1) $(2)
endef

test-minikube: helm-check test-minikube-contract ## Install and exercise the chart in an owned single-node Minikube cluster
	$(call run_minikube_scenario,basic)

test-minikube-backup-restore: helm-check test-minikube-contract ## Restore a full logical backup into fresh PostgreSQL and recover the owned Helm deployment
	$(call run_minikube_scenario,backup-restore)

test-minikube-agntcy: export CATALOG_AGNTCY_PYTHON := $(subst ',,$(PYTHON))
test-minikube-agntcy: export PYTHONOPTIMIZE := 0
test-minikube-agntcy: helm-check test-minikube-contract ## Qualify local Directory (opt-in INTEROPERABILITY_TRUST_VERIFICATION=on)
	$(call run_minikube_scenario,agntcy)

test-interoperability-ans: export CATALOG_ANS_PYTHON := $(subst ',,$(PYTHON))
test-interoperability-ans: export PYTHONOPTIMIZE := 0
test-interoperability-ans: helm-check test-minikube-contract ## Qualify native ANS and Catalog live discovery, paging, revocation and recovery in an owned cluster
	$(call run_minikube_scenario,ans)

.PHONY: capture-ans-trust-fixtures test-trust-verification
test-trust-verification: export CATALOG_ANS_PYTHON := $(subst ',,$(PYTHON))
test-trust-verification: export PYTHONOPTIMIZE := 0
test-trust-verification: helm-check test-minikube-contract ## Verify explicit private ANS assessment Jobs against real owned services
	$(call run_minikube_scenario,ans,--ans-trust)

capture-ans-trust-fixtures: export CATALOG_ANS_PYTHON := $(subst ',,$(PYTHON))
capture-ans-trust-fixtures: export PYTHONOPTIMIZE := 0
capture-ans-trust-fixtures: helm-check test-minikube-contract ## Capture authentic V1/V2 public ANS evidence in an owned cluster; no Catalog assessment
	$(call run_minikube_scenario,ans,--ans-capture)

test-interoperability-gateway: export CATALOG_GATEWAY_PYTHON := $(subst ',,$(PYTHON))
test-interoperability-gateway: export PYTHONOPTIMIZE := 0
test-interoperability-gateway: helm-check test-minikube-contract ## Qualify local Gateway lifecycle (opt-in INTEROPERABILITY_TRUST_VERIFICATION=on)
	$(call run_minikube_scenario,gateway)

test-minikube-topology: helm-check test-minikube-contract ## Exercise replicas, disruption, and Calico isolation in an owned three-node cluster
	$(call run_minikube_scenario,topology)

.PHONY: test-minikube-pgbouncer
test-minikube-pgbouncer: helm-check test-minikube-contract ## Qualify optional session PgBouncer, private TLS and network isolation in an owned cluster
	$(call run_minikube_scenario,pgbouncer)

test-minikube-peers: helm-check test-minikube-contract ## Qualify federation between two owned Helm releases with separate PostgreSQL instances
	$(call run_minikube_scenario,peers)

test-minikube-upgrade: helm-check test-minikube-contract ## Qualify coordinated upgrades and retained-state recovery across an owned three-node cluster
	$(call run_minikube_scenario,topology,--upgrade-only)

test-minikube-monitoring: helm-check test-minikube-contract ## Qualify optional monitoring, dashboards and retained PVCs in an owned cluster
	$(call run_minikube_scenario,monitoring)

test-minikube-tracing: helm-check test-minikube-contract ## Verify OTLP causality across two API and worker replicas in an owned cluster
	$(call run_minikube_scenario,tracing)

test-minikube-semantic: helm-check test-minikube-contract ## Qualify optional hybrid search with owned Helm and model replicas
	$(call run_minikube_scenario,semantic)

test-minikube-ui: helm-check test-minikube-contract ## Test optional UI image, subpath and cross-replica sessions in an owned cluster
	$(call run_minikube_scenario,ui)

.PHONY: test-minikube-autoscaling
test-minikube-autoscaling: helm-check test-minikube-contract ## Test external HPA transitions, metrics loss and maintenance handoff in an owned cluster
	$(call run_minikube_scenario,autoscaling)

.PHONY: vault-tools-install test-minikube-vault test-minikube-vault-cleanup
vault-tools-install: ## Cache reviewed optional Vault/VSO test inputs; do not deploy
	$(PYTHON) -B test/minikube/vault_tools.py

test-minikube-vault: helm-check test-minikube-contract ## Test optional static Vault delivery in an owned Minikube cluster
	$(call run_minikube_scenario,vault)

test-minikube-vault-cleanup: helm-check test-minikube-contract ## Verify owned Vault cleanup after an injected failure and real SIGTERM
	$(call run_minikube_scenario,vault,--vault-cleanup-test failure)
	$(call run_minikube_scenario,vault,--vault-cleanup-test interrupt)

.PHONY: cert-manager-tools-install test-minikube-cert-manager
cert-manager-tools-install: ## Cache exact test-only certificate controllers, charts and renewal CLI; do not deploy
	$(PYTHON) -B test/minikube/certmanager_tools.py

test-minikube-cert-manager: helm-check test-minikube-contract ## Test external MCP ingress issuance, served renewal and issuer recovery in an owned cluster
	$(call run_minikube_scenario,cert-manager)

test-minikube-replicas: helm-check test-minikube-contract ## Test three UI/API/worker replicas, concurrent integrity, recovery and bounded load
	$(call run_minikube_scenario,replicas)


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
##@ 🧠 Embeddings and search relevance
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

.PHONY: embeddings-install embeddings-check test-embeddings test-semantic test-semantic-restore test-semantic-restore-prepare search-relevance-install
.PHONY: search-relevance-check search-relevance-evaluate search-relevance-compare-install
.PHONY: search-relevance-compare

embeddings-install: ## Acquire one pinned local embedding profile and fixture images explicitly
	$(PYTHON) test/embeddings/runner.py install --profile "$(EMBEDDING_PROFILE)"

embeddings-check: ## Test embedding client and metadata/ranking without Docker, a model or database
	$(GO) test -tags=functional ./internal/platform/embeddings ./internal/platform/config ./internal/platform/fetcher ./internal/catalog ./test/embeddings/...

test-embeddings: ## Qualify the Go embedding client against one owned offline TEI model
	$(PYTHON) test/embeddings/runner.py test --profile "$(EMBEDDING_PROFILE)"

test-semantic: ## Qualify runtime hybrid search with an owned Compose stack and offline MiniLM
	$(PYTHON) test/embeddings/semantic.py

test-semantic-restore: ## Restore a small semantic Catalog and verify search, permissions and index rebuild
	$(PYTHON) test/embeddings/semantic.py --restore

test-semantic-restore-prepare: ## Back up, restore and verify a small semantic Catalog before runtime startup
	$(PYTHON) test/embeddings/semantic.py --restore-prepare

search-relevance-install: ## Download pinned local embedding model and evaluation images explicitly
	$(PYTHON) test/search-relevance/runner.py install

search-relevance-check: ## Test relevance fixtures, metrics and launcher without a model or database
	$(PYTHON) test/search-relevance/runner.py check

search-relevance-evaluate: ## Evaluate lexical/semantic/hybrid quality with owned offline CPU model and exact pgvector
	$(PYTHON) test/search-relevance/runner.py evaluate

search-relevance-compare-install: ## Acquire pinned BGE/MiniLM inputs for the separate relevance comparison
	$(PYTHON) test/search-relevance/comparison.py install

search-relevance-compare: ## Tune BGE/MiniLM then qualify only the frozen winner on fresh held-out queries
	$(PYTHON) test/search-relevance/comparison.py evaluate


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
##@ ⏱️ Performance and measurement
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

.PHONY: bench-search bench-search-dsn bench-search-plans bench-search-plans-dsn test-load-smoke test-load
.PHONY: test-scale test-public-delivery test-soak bench test-monitoring perf-clean benchstat-tools-install bench-compare

bench-search: ## Compare bounded batch and point hydration using disposable PostgreSQL
	$(call run_postgres_integration,bench-search-dsn)

bench-search-dsn: ## Run hydration benchmarks against an explicit disposable DSN
	@test -n "$${CATALOG_TEST_DATABASE_DSN:-}" || { printf '%s\n' 'CATALOG_TEST_DATABASE_DSN is required.' >&2; exit 2; }
	$(GO) test -tags=integration ./internal/platform/database -run '^$$' -bench '^BenchmarkPostgresSearchHydration$$' -benchmem -benchtime=500ms -count=3

bench-search-plans: ## Inspect bounded exact-role Search plans using disposable PostgreSQL
	$(call run_postgres_integration,bench-search-plans-dsn)

bench-search-plans-dsn: ## Inspect Search plans against an explicit disposable DSN
	@test -n "$${CATALOG_TEST_DATABASE_DSN:-}" || { printf '%s\n' 'CATALOG_TEST_DATABASE_DSN is required.' >&2; exit 2; }
	$(GO) test -tags=integration ./internal/platform/database -run '^$$' -bench '^BenchmarkPostgresSearchPlans$$' -benchtime=1x -count=1 -v

test-load-smoke: ## Run an owned short search diagnostic; PERF_DURATION_SECONDS/PERF_WORKERS adjust load
	$(PYTHON) test/performance/runner.py smoke

test-load: ## Run owned local/indexed/public read phases and a bounded mixed Catalog workflow
	$(PYTHON) test/performance/runner.py load

test-public-delivery: ## Measure owned 1,001-entry public 200/304 delivery with fixed bounds
	$(PYTHON) test/performance/runner.py public-delivery

test-scale: ## Test owned 1k/10k/100k search fixtures; PERF_ITEMS defaults to 1000
	$(PYTHON) test/performance/runner.py scale

test-soak: ## Run the owned mixed workload for one finite hour, two readers and at most 60 writes
	$(PYTHON) test/performance/runner.py soak

bench: ## Record ten focused Go runtime collection/exposition benchmark samples
	$(PYTHON) test/performance/runner.py bench

benchstat-tools-install: ## Explicitly install the pinned report-only benchmark comparison tool
	$(PYTHON) -B test/performance/benchmarks.py install

bench-compare: export BENCH_BASELINE := $(BENCH_BASELINE)
bench-compare: export BENCH_CANDIDATE := $(BENCH_CANDIDATE)
bench-compare: ## Compare two compatible bench reports; requires BENCH_BASELINE and BENCH_CANDIDATE
	$(PYTHON) -B test/performance/benchmarks.py compare

test-monitoring: ## Qualify owned Compose metrics, Prometheus collection and Grafana provisioning
	$(PYTHON) test/performance/runner.py monitoring

perf-clean: export PERF_RUN_ID := $(PERF_RUN_ID)
perf-clean: ## Reap only a validated abandoned PERF_RUN_ID; retain safe measurement reports
	$(PYTHON) test/performance/runner.py clean


# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
##@ 🧹 Hooks and cleanup
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

.PHONY: hooks-install hooks-uninstall pre-commit-check clean clean-dry-run clean-output clean-output-dry-run

hooks-install: ## Opt in to staged checks; require installed tools and preserve existing hooks
	python3 -B test/hooks/runner.py install

hooks-uninstall: ## Remove only the unchanged Catalog pre-commit hook
	python3 -B test/hooks/runner.py uninstall

pre-commit-check: ## Check staged secrets, JSON/YAML, Go formatting and whitespace without changing files
	python3 -B test/hooks/runner.py check

clean: ## Remove known builds, example binaries, coverage, caches, dependencies and tools; preserve retained reports, configuration and data
	$(PYTHON) -B test/packaging/clean.py

clean-dry-run: ## Validate and list the normal cleanup inventory without changing files
	$(PYTHON) -B test/packaging/clean.py --dry-run

clean-output: ## Remove selected output reports and test work; preserve runtime inputs, models and nested Git repositories
	$(PYTHON) -B test/packaging/clean.py --scope output

clean-output-dry-run: ## Preview selected output removal and explain preserved paths without changing files
	$(PYTHON) -B test/packaging/clean.py --scope output --dry-run
