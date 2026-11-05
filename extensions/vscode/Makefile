# Keep commands local, installed-only and independent of workflow .env files.
SHELL := /bin/sh
PYTHON ?= python3
CATALOG_VSCODE_EXECUTABLE ?=
override CATALOG_ENV_FILE := /dev/null
export CATALOG_ENV_FILE
ifneq ($(words $(MAKECMDGOALS)),0)
ifneq ($(words $(MAKECMDGOALS)),1)
$(error VS Code targets must run alone)
endif
endif
.DEFAULT_GOAL := help
.PHONY: help tools-install test-tools-install check build watch debug package verify test screenshots clean clean-dry-run
help: ## List extension commands
	@awk 'BEGIN {FS=":.*##"; print "Usage: make <target>"} /^[a-zA-Z0-9_-]+:.*##/ {printf "  %-20s %s\n", $$1, $$2}' Makefile
tools-install: ## Install pinned build/check tools; requires a current SDK archive
	$(PYTHON) -B tooling/runner.py install
test-tools-install: ## Install build tools plus the pinned editor and local peer fixtures
	$(PYTHON) -B ../../test/vscode/runner.py install
check: ## Check types, lint, units and enforced core coverage
	$(PYTHON) -B tooling/runner.py check
build: ## Build development output with TypeScript source maps
	$(PYTHON) -B tooling/runner.py build
watch: ## Watch development source; stop with Ctrl+C
	$(PYTHON) -B tooling/runner.py watch
debug: export CATALOG_VSCODE_EXECUTABLE := $(CATALOG_VSCODE_EXECUTABLE)
debug: ## Build and start an owned local editor; select CATALOG_VSCODE_EXECUTABLE
	$(PYTHON) -B tooling/runner.py debug
package: ## Build and validate a reproducible local VSIX
	$(PYTHON) -B tooling/runner.py package
verify: ## Verify the VSIX digest and its current source/tool inputs
	$(PYTHON) -B tooling/runner.py verify
test: ## Qualify local fixtures and the installed VSIX in owned profiles
	$(PYTHON) -B ../../test/vscode/runner.py test
screenshots: ## Capture benign guide images in an owned editor; review before copying
	$(PYTHON) -B ../../test/vscode/runner.py screenshots
clean: ## Remove extension tools/builds/packages; preserve SDKs, reports and profiles
	$(PYTHON) -B tooling/runner.py clean
clean-dry-run: ## Validate and preview extension-only cleanup
	$(PYTHON) -B tooling/runner.py clean-dry-run
