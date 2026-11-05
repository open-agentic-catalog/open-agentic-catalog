# Helm chart contract tests

The Go tests use the `helm` build tag and invoke Helm to inspect the product chart under [deployments/helm/open-agentic-catalog/](../../deployments/helm/open-agentic-catalog/README.md). They do not create a cluster or contact PostgreSQL.

- `test_functional_packaging.py` runs the packaging script with real Helm. It checks metadata, long labels, image selection, minimum versions, archive contents and output safety. Source and packaged metadata must omit unapproved repository URLs. Temporary directories keep source charts and clusters unchanged.
- `chart_test.go` checks rendered resource inventory, identity/secret separation, migration ordering, security contexts, probes, scheduling, unsafe values and packaged chart contents. It rejects overrides of the database trust checksum and checks rollout after the CA reference changes.
- `names_test.go` checks source and packaged name collisions, boundary lengths, composed names, disabled resources and external policy references. Valid renders retain distinct Kubernetes identities.
- `configuration_test.go` loads the rendered managed policy and applies explicit container environment values after the role ConfigMap. It checks selected enabled features and Publisher DNS modes, runtime rollouts and migration/UI isolation. External policy data, mounted public-key paths and runtime keys use explicit fixtures; real external contents remain unverified.
- `compatibility_test.go` checks both charts' Kubernetes 1.34 minimum on source and packaged archives, including below-minimum rejection and vendor-suffix parsing. These are local version-string tests, not distribution qualification.
- `auth_trust_test.go` covers API-only issuer trust projection and rollout identity.
- `database_pool_test.go` checks default and explicit timing delivery to API/worker and migration configuration, validates the rendered values through the actual loader, and requires API/worker rollout changes.
- `logging_test.go` checks defaults, independent backend/UI levels and formats, migration delivery, affected-workload rollout checksums and invalid-value rejection.
- `webhook_egress_test.go` covers enabled-only receiver policy/trust mounts and migration isolation.
- `tracing_test.go` covers default-off and enabled OTLP configuration, API/worker Secret and trust mounts, migration isolation, checksums and invalid-value rejection. File inputs require their tracing mount; public unauthenticated export and an existing alternate CA mount remain supported.
- `ui_test.go` covers optional UI configuration, scoped mounts, probes, ingress, reserved lifecycle annotations, network policy, development login and image digests. The `ci/payload-values.yaml` overlay qualifies nondefault backend diagnostic/request budgets and independent UI forwarding limits. Identity regressions accept distinct custom accounts and reject UI/backend ServiceAccount collisions, including truncated generated names, for both chart-managed and external accounts. A disabled UI does not reserve an account name.
- `ui_network_policy_test.go` checks exact UI/API release selectors, namespace scope, empty ingress, TCP API and TCP/UDP DNS ports, explicit extra rules and omission when either UI or its policy is disabled. These rendering contracts do not establish CNI enforcement.
- `artwork_test.go` checks exact artwork policy delivery, defaults, rollout and migration isolation.
- `mcp_test.go` checks exact HTTP MCP Ingress routing, independent UI routes and unsafe ingress configuration rejection.
- `semantic_test.go` checks enabled and disabled embeddings, role-scoped Secret inputs and optional migration selection.
- `pgbouncer_test.go` checks private session pooling, TLS, role isolation, configuration and disabled resources. It rejects backend account collisions for managed and external accounts, including truncated names.
- `trust_verification_test.go` checks optional ANS authority inputs, role-scoped mounts, rollout and invalid settings.
- `monitoring_test.go` packages the independent companion chart with shared assets, then checks disabled renders without inputs, enabled requirements, default application-chart independence, narrow RBAC, image/security inputs, external storage and rollout checksums. Secret and PVC cases accept dotted names through 253 characters, preserve exact references and reject malformed or oversized names.

Use `make helm-check` from the repository root for strict lint plus these tests. `make helm-test` runs the Go suite and Python packaging tests; `make helm-package-test` runs just the three packaging cases. Set `HELM` to select the command; a missing command fails the explicit gate rather than skipping tests.

`make test-deployment-compile` compiles tagged harnesses without invoking Helm. Tests load rendered environment values through the real configuration validator, so valid YAML alone is not sufficient.

Kubernetes schema validation is the separate `make helm-schema-check` target and retrieves 1.34.0 schemas to match the chart minimum. Offline Helm tests pass the version explicitly rather than relying on the client's compiled-in default. Runtime installation, probes and network behavior belong to the existing [Minikube harness](../minikube/README.md); its environment is not a distribution allowlist.
