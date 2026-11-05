# Scoped ZAP security checks

These explicit functional gates use the existing disposable PostgreSQL/API smoke fixture, a built UI image, installed Playwright/Chromium and ZAP 2.17.0. They never accept a target URL or scan a retained environment. Use Linux amd64 with Docker Engine and host access to its bridge addresses; Docker Desktop and rootless engines are unqualified.

Ordinary units and runtime images do not install or execute ZAP.

```sh
make zap-tools-install
make test-security-passive
make test-security-active
```

Run each target alone. Installation downloads the exact image in `tool.json`; execution requires it locally and uses `pull_policy: never`. Install UI packages and Chromium with the existing `ui-install` and `ui-browser-install` targets.

Existing Go, container-build and conformance prerequisites also apply. No online add-on updates, marketplace installation, external callback rules, crawl or external target selection is enabled.

## Scope and qualification

Both profiles calibrate the actual scanner against harmless local HTML fixtures: reflected XSS (40012), PostgreSQL error-style SQL injection (40018) and missing framing protection (10020) must be detected; the escaped, header-protected control must clear those rules. A missing detection fails the run. A separate real API calibration removes the valid identity and must fail with 401 before application accounting begins.

These deliberately insecure responses exist only inside the disposable tool container.

Six successful API controls use the actual proxy and existing Go response-contract validator: namespace list/create/get/update, catalog search and browse. They use synthetic administrator credentials, fresh idempotency keys and the current ETag. Search/browse require a nonempty paginated fixture.

The separate Chromium journey signs in as the reader, searches three smoke-seeded records, visits Explore and signs out, retaining the existing network/error assertions. Production clients' proxy, origin and TLS controls are unchanged.

ZAP must record the selected API controls and browser navigation, login, session, search, Explore and logout exchanges. The browser-to-host observations do not observe the host's separate API client. Direct API controls supply the API-listener observations.

These plain-HTTP development-login fixtures do not qualify production TLS, OIDC or an IdP.

`active.yaml` imports the unchanged Control OpenAPI 3.1 file. Its context **excludes** every path except `/api/v1/catalog/search` and `/api/v1/catalog/resources`; inclusion alone does not restrict the importer. Exactly two imports and completed, nonempty rule executions are required.

ZAP also sends temporary GETs to the root and a random numeric root path to establish 404 baselines, retaining a type-0 copy. Only those method/path-shape/history-type/status combinations are accepted and reported separately. Only reflected XSS and SQL injection are enabled at Low strength/Medium threshold.

The namespace mutation controls remain passive: active mutation fuzzing would mostly hit stale ETags or command prerequisites, and Schemathesis already owns invalid command generation. All other active rules and API operations remain unqualified by this gate.

The checked-in Automation Framework plans execute import, scan and passive-drain jobs. The existing functional supervisor supplies the exit policy: tool errors/warnings, missing jobs, empty or incomplete traffic, rule timeouts, authentication loss, unexpected findings and cleanup failure fail. ZAP's unsent type-0 parent nodes are distinguished from real transport failures.

HTTP counts and scanner findings are not statement coverage or evidence of complete security testing.

## Isolation and evidence

All containers use an invocation-owned Docker **internal** network, verified before scanning. There is no Internet route, and scanner traffic never reaches an IdP or public service. The host driver reaches owned container addresses directly because Docker does not publish internal-network ports.

The UI shares the scanner's network namespace so Chromium can proxy its loopback development origin without weakening UI origin checks. Browser traffic is restricted to that origin. Selected active read operations cannot initiate Catalog resource fetching.

The scanner has one active thread, a 1 GiB Java heap, a 2 GiB container limit, two CPUs, 256 PIDs, bounded temporary storage and rotated local logs. Rules have one-minute limits; application/calibration active phases have three/two-minute limits. A 480-second driver deadline, 240-second plan wait, 90-second browser deadline, bounded API responses, a 1,000-alert rejection ceiling and a 2,000-history-entry admission ceiling supplement the existing 20-minute packaged supervisor and separate cleanup deadline.

The history ceiling is a completion check, not a per-request firewall. No privileged container, host network or Docker socket is exposed to ZAP.

Reports are stored under ignored `output/functional/run-<id>/report.json`. They bind source, contract, application/UI/scanner images, exact add-ons, controls, observed route/status tuples, rule completion/counts and cleanup. Only alert rule/risk/host/path/method/name summaries survive.

Cookies, bearer headers, response bodies, attack evidence, plans containing live fixture keys and raw ZAP sessions/logs remain disposable. Use the same target to reproduce; it creates fresh fixture identities and state. Review new findings independently and retain a focused regression before changing product behavior.

## Reviewed dispositions

Every unlisted finding fails, including Low and Informational. These exceptions are limited to the exact rule, risk, host, path and method in the driver; they do not disable scanner rules.

| Rule and scope | Rationale and revisit condition |
| --- | --- |
| 10096 Low, UI `GET /auth/session` | `expiresAt` deliberately tells the signed-in browser when its session expires. It is not a secret or a production-clock disclosure claim. Revisit if the response exposes additional timestamps or private state. |
| 10109 Informational, UI `GET /catalog` and `/explore` | These are the expected React application shell. Revisit when changing browser architecture or scanner scope. |
| 10112 Informational, API `GET /api/v1/catalog/resources` | The paginated response includes an opaque continuation value; identifying token-like response data is not an authentication bypass. Revisit changes to cursor disclosure or retention. |
| 10112 Informational, UI `POST /auth/login` | Successful login intentionally sets the protected session cookie. Revisit session/cookie changes; this HTTP fixture does not establish TLS/OIDC security. |

## Maintenance

`tool.json` pins the image digest, core version and every installed add-on version; mismatch fails. ZAP and the upstream `zap-extensions` repository declare Apache-2.0. Their image additionally contains OS/Java/browser and add-on dependencies with separate upstream licenses, including Java's GPL classpath exception; the source license is not a complete image-license inventory.

Upstream notices remain inside the unmodified execution-only image. It is neither redistributed nor added to shipped images or the Go license allowlist. A pin update requires reviewing the closure/notices and requalifying both targets, calibrations and completion checks.

Native isolated evidence tests run in `test-tooling-unit`; Make/process tests run in the functional harness. Reuse ordinary source, unit, local functional, UI and documentation gates after driver or fixture changes. No scanner replaces those tests, tenant-isolation assertions or independent penetration testing.

Upstream references: [Automation Framework](https://www.zaproxy.org/docs/automate/automation-framework/), [OpenAPI import and soft redirect limit](https://www.zaproxy.org/docs/desktop/addons/openapi-support/automation/), [active scan policy and deadlines](https://www.zaproxy.org/docs/desktop/addons/automation-framework/job-ascan/), [API](https://www.zaproxy.org/docs/api/).
