# OpenAPI validator dependencies

This isolated Go module pins the oasdiff comparison command used by the [API validation harness](../README.md). Its kin-openapi dependency is part of that tool's graph, not a separately installed validator. The module does not change the backend's dependencies or vendor another API contract.

Run `make validation-tools-install` from the repository root to install the locked tools. Use `make api-diff` or `make api-breaking-check` with an explicit local `API_BASE` for comparisons, and `make api-validation-test` for launcher and policy tests. Keep tool installation separate from read-only validation.
