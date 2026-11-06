# Owned Minikube runtime tests

`make test-minikube-ui` builds the separate UI image, verifies the default installation has no UI Pods, enables two replicas with mounted test keys and API tokens, then checks root health probes, subpath navigation, shared sessions and authenticated API access through each Pod. The two-replica UI selection also updates its synthetic mounted Secret and replaces processes through old-first staging, new-first promotion and emergency old-key removal. Independent cookie decoding verifies the writer key; explicit Pod routes prove mixed-stage acceptance without Service affinity.

A live overlap host accepts the same unexpired old cookie that a replaced host rejects, while new cookies retain authorized session/API access. Pod UIDs and stage placements are retained without key or cookie values. Immediate removal qualifies the emergency procedure; deterministic host tests own the ordinary retirement window and five-minute pending-login lifetime.

Disabling the option removes UI resources and retains the backend. It owns and cleans its cluster, images, port-forwards and temporary inputs. This is packaged host/session evidence, not a real human OIDC browser login; the process-based Playwright suite remains `make test-ui`.

`make test-minikube-tracing` selects the focused tracing scenario: two APIs and two workers, explicit per-Pod requests, replay across APIs, deterministic worker lease takeover, fresh process identities, mounted export credentials/private CA, collector outages and disablement. It reuses the existing fixture and [bounded OTLP decoder](../tracing/README.md), without installing a monitoring or trace-backend chart. API/worker trace resources identify processes, not Pods; this test topology is not a production requirement.

This build-tagged Go executable creates a disposable local cluster and tests the shipped Helm chart. It does not reuse a caller's cluster or turn the local PostgreSQL/identity fixtures into product chart dependencies.

| Files | Responsibility |
| --- | --- |
| [main.go](main.go), [config.go](config.go) | Scenario selection, host/input checks and overall execution bounds. |
| [harness.go](harness.go), [resources.go](resources.go) | Owned cluster, images, fixture resources, commands, diagnostics and cleanup. |
| [scenario.go](scenario.go), [upgrade.go](upgrade.go) | Basic installation, failed-hook recovery, governed workflows and retained-state reconfiguration. |
| [topology.go](topology.go), [topology_service.go](topology_service.go), [topology_service_disruption.go](topology_service_disruption.go), [topology_diagnostics.go](topology_diagnostics.go) | Multi-replica replay/leases, in-cluster Service calibration, disruption, placement and bounded network diagnostics. |
| [monitoring.go](monitoring.go), [monitoring_cross_namespace.go](monitoring_cross_namespace.go), [monitoring_recovery.go](monitoring_recovery.go) | Companion lifecycle, retained history, cross-namespace Pod discovery, live permissions, isolated credentials and fresh samples from running unready Pods. |
| [tracing.go](tracing.go) | Per-replica OTLP export, incoming parents, linked worker replacement, scoped credentials/trust and collector-outage checks. |
| [ui.go](ui.go), [ui_policy.go](ui_policy.go), [replicas.go](replicas.go) | Packaged UI sessions, three-replica command integrity, optimistic concurrency, mixed request batches, process replacement and kubelet resource observations. |
| [autoscaling.go](autoscaling.go), [autoscaling_load.go](autoscaling_load.go), [autoscaling_worker.go](autoscaling_worker.go), [autoscaling_maintenance.go](autoscaling_maintenance.go) | External HPA transitions, bounded Search traffic, metrics loss and command/worker integrity. |
| [instrumentation.go](instrumentation.go), [webhooks.go](webhooks.go) | Direct application-metric assertions and optional webhook scenarios. |

From the repository root, `make test-minikube-contract` runs harness tests without creating a cluster. `make test-minikube` runs the single-node scenario; `make test-minikube-topology` runs the three-node scenario; `make test-minikube-monitoring` installs the separate monitoring chart in an owned single-node cluster with two API and two worker replicas. Monitoring uses a 40-minute default overall bound, pinned upstream images and ephemeral then externally retained PVC-backed storage.

The monitoring scenario then installs a separate collector namespace with an explicit target namespace and release. It checks live Pod read permissions, denied Secret/node access, credential mounts and selection with decoys in both namespaces. Each current API/worker Pod must supply a sample less than 20 seconds old, using a five-second scrape interval. An unsatisfied readiness gate verifies collection from running unready API Pods, followed by readiness restoration. The scenario removes only its cross-namespace discovery binding and restarts Prometheus to clear the existing watch. It requires an explicit list denial, no discovered targets and no new samples, while historical samples remain readable. After binding restoration, all four current Pods must supply samples newer than the restoration. Catalog readiness, retained search results and Pod identities remain unchanged during the failure. Disable/re-enable and uninstall checks verify removal in both namespaces while preserving the external Secret and two bound PVCs.

The scenario uses dotted Secret and PVC names to check Kubernetes admission, exact chart references and retained storage throughout removal and reinstallation.

Use the [Minikube prerequisites](../../documentation/src/content/docs/operations/deployment.mdx#maintained-local-minikube-verification) for exact host, tool, cached-image, resource and version inputs. Do not invoke the harness against an existing kubeconfig as a shortcut.

Each runtime invocation builds and checks its own image identity, uses bounded setup and cleanup, and reports the tested environment. NetworkPolicy assertions require the selected enforcing CNI. This is application/chart evidence on the recorded platform, not production capacity, database HA, an independent identity-provider matrix or a Kubernetes distribution allowlist.

Calico scenarios wait for both the node DaemonSet and controller Deployment before verifying their resolved image IDs. A ready node alone does not mean the controller has started; unavailable or mismatched images still fail qualification.

The container runtime can report an image index digest or its platform manifest digest as `ImageID`. The harness accepts only the reviewed index or `linux/amd64` digest after it verifies that every node uses `amd64`. The report separates observed IDs from the reviewed index and platform pins. Tags, different repositories and other platform digests do not qualify.

`make test-unit` includes the isolated Minikube helpers using the existing `minikube` tag. Socket tests additionally require `functional`; `make test-minikube-contract` runs both selections. Neither selection needs a Minikube binary or cluster.

[Cleanup process tests](harness_cleanup_functional_test.go) verify partial startup, cancellation and a failed profile-deletion command. Local command stubs record exact owned targets; the test also verifies temporary-file removal and unrelated-file preservation. These are functional tests because they start subprocesses. They qualify cleanup control flow; runtime scenarios separately verify actual cluster resource removal.

The primary identity fixture Pod serves both OIDC and federation HTTPS. The federation Service selects that Pod; its Service labels do not select network-policy peers. Existing API and worker rules allow the primary Pod on TCP port 8443 and keep the decoy blocked. [Isolated policy tests](topology_policy_test.go) check this Service-to-Pod mapping, DNS, direct and pooled database paths, namespace isolation, protocols and ports. The topology scenario separately verifies actual federation and the decoy's unchanged request counter.

The replica and topology scenarios reuse the packaged federation workflow. Their Helm inputs select seven Job attempts and a 5.5-second retry ceiling, matching that workflow's retained-budget and upstream-hint assertions. Other scenarios retain the chart defaults.

The basic scenario checks the failed migration hook's safe command diagnostic, verifies the missing-role fixture and untouched schema directly in PostgreSQL, and then requires a successful install with the correct role.

## Optional Vault secret delivery

Vault is optional. The default chart and ordinary scenarios use existing Kubernetes Secrets without Vault or VSO.

Run these commands separately from the repository root:

```sh
make vault-tools-install
make test-minikube-vault
make test-minikube-vault-cleanup
```

Use the common Minikube prerequisites and explicit preload archive when needed. This scenario selects eight CPUs, 12 GiB memory, Calico and a 40-minute overall bound. It owns its profile, provider namespace, Catalog namespace, storage and private inputs.

[The pins](fixtures/vault/pins.json) select Vault 2.2.1, Vault chart 0.34.1, VSO 1.6.2 and kube-rbac-proxy v0.23.1. The three tool versions have exact approved age exceptions. Installation verifies chart/source hashes and platform-specific image digests. It retains source notices and an acquisition receipt under ignored `.tools/minikube-vault/`. Normal tests never download missing prerequisites. `make clean` includes this cache.

The test starts one persistent Vault Raft member with private TLS and manual initialization/unseal. VSO runs separately with its required proxy and dedicated Kubernetes authentication. It creates five complete Secrets: API, worker and direct-owner DSNs, the cursor key and one source bearer generation. The Catalog chart receives only its existing Secret references. Application and migration Pods receive no Vault token or Kubernetes token.

Initial deployment requires matching KV versions, current sync conditions, exact destination ownership and complete data. The owner migration precedes API/worker startup. Fresh database connections verify each exact role, TLS and writer routing. Real source inspection, Source test and worker synchronization verify the same credential revision. The report contains identities and outcomes, not passwords, bearer tokens or unseal material.

The owned test does not expose Vault through an Ingress. The recorded image scan reports Go JOSE and other advisories. Review fresh findings before selecting these provider images. Only generated keys and fixture-issued Kubernetes tokens enter this lab. These execution-only images are not shipped Catalog dependencies. Production image selection and security review remain operator responsibilities.

The scenario also tests credential rotation, scoped denial and provider recovery. Database maintenance drains API/worker, changes the three passwords, synchronizes successor DSNs and runs coordinated deployment. Fresh TLS connections require SCRAM passwords. They reject wrong and retired passwords before accepting each successor. A partial replacement keeps both runtimes stopped until the remaining inputs are corrected.

Source rotation uses one complete Secret generation and a new revision. API and worker retain their Pod identities. Mounted metadata, Source test and a complete worker snapshot confirm activation. The owned source separately rejects the retired token.

Negative controls check exact Vault paths, ServiceAccount, namespace, audience and destination ownership. Controller outage and Vault sealing refuse maintenance from unconfirmed inputs while accepted credentials continue working. Vault Pod replacement retains the Raft claim and requires unseal. A source authentication failure leaves unrelated startup inputs and Catalog readiness available. Repair activates the successor without restarting Catalog. The final governed smoke replay verifies retained content and idempotency.

The upstream VSO controller has cluster-wide Secret access, TokenRequest authority and workload patch permissions. Per-role Vault policies do not restrict that Kubernetes authority. This scenario installs it only inside the owned cluster. Review controller RBAC separately before using a shared cluster.

The [operator tutorial](../../documentation/src/content/docs/operations/vault.md) provides the same synchronization YAML and policies used by this native scenario. Cleanup completes VSS finalizers before stopping the controller. It observes destination deletion, reconciles a separate positive control and verifies that removed destinations do not return. It revokes owned authentication tokens and policies, then removes the profile, image aliases and private inputs. An unrelated bootstrap Secret survives logical removal.

`make test-minikube-vault-cleanup` repeats initial delivery in two separate owned clusters. It injects an exact failure or sends the runner a real SIGTERM. Both require independent cleanup and set `vault.cleanupTest` in their reports; neither claims full Catalog qualification. Partial setup still uses the existing whole-profile teardown. Retained Compose services and other clusters remain outside the target scope.

## Optional session PgBouncer

Run `make test-minikube-pgbouncer` from the repository root after the same maintained tool/image/host preflight. This selected one-node Calico scenario deploys two API/two worker replicas, one digest-pinned PgBouncer and real PostgreSQL 18. Invocation-owned private TLS verifies both database hops; the direct owner migration/provisioning Jobs mount only public CA trust and run before the pooler is available.

Actual PostgreSQL client addresses prove both runtime roles use the pooler. Concurrent idempotent commands, governed smoke, private ingress/egress controls, a credential-free UI-labelled probe, pooler Pod replacement without restarting runtimes, configuration/no-op reapplication and explicit return to direct DSNs plus pooler removal are asserted. A UI-labelled network probe qualifies database/pooler denial, not browser behavior.

The scenario installs and upgrades the pooled Catalog through the deployment coordinator. It checks retained governed state and uses the coordinator to return to direct database connections.

The certificate phase updates the external database CA ConfigMap and the two server TLS Secrets. It waits for projected file hashes in unchanged Pods. Restricted diagnostic containers mount only the API/worker public trust volume. New certificate files must not change the served leaves before activation.

The test drains API, worker and pooler Pods before replacing consumers. CA overlap and retirement retain the PostgreSQL Pod; successor leaves require a new PostgreSQL Pod. Fresh verified SQL connections check both server identities, exact roles and backend TLS. Each phase replays the governed smoke workflow and completes a distinct source-sync Job. The report records public certificate/trust hashes, Pod placements, backend identities and Job IDs. Predecessor-only client trust must reject both successor endpoints.

These fixture operations are certificate maintenance, with no schema change or Helm upgrade. The existing coordinator checks still own installation, upgrades and return to direct DSNs. The [database certificate procedure](../../documentation/src/content/docs/operations/database-roles.mdx#activate-projected-database-certificates-in-kubernetes) explains operator ordering and failure recovery. The separate local drill proves valid old-chain peer rejection after consumer trust retirement.

It supplements the basic/topology gates and the [local protocol/recovery gate](../pgbouncer/README.md); it does not qualify multi-pooler HA or ARM64 execution. Reports and independently checked cleanup use the existing owned harness.

## Three-replica integrity and short-load test

`make test-minikube-replicas` exercises three UI/API/worker replicas against one owned PostgreSQL fixture. The [replica testing guide](../../documentation/src/content/docs/contributing/testing.md#replica-integrity-and-short-load) maintains the commands, prerequisites, integrity assertions, workload, report fields, cleanup and limitations. Use the separate topology scenario for node loss and the browser suites for JavaScript and human login.

[replica_authority.go](replica_authority.go) adds a direct-API scoped-reader grant/revoke/restore cycle: the same membership and completed Resource command must replay only under current equivalent authority, with one originating mutation audit and unchanged readback. The temporary grant and draft are removed before load. No UI-selected request is used to identify the upstream API process.

[replicas.go](replicas.go) implements the concurrent command, readback, audit, replacement and resource assertions. [replica_pressure.go](replica_pressure.go) adds twelve source Jobs, three observed worker slots, a shared queue, 96 API/UI reads and cancellation of a positively observed SQL wait; [replicas_test.go](replicas_test.go) and [replica_authority_test.go](replica_authority_test.go) cover helper behavior and false-pass rejection without a cluster. Keep tested builds, timings and unresolved observations in local reports. Historical measurements do not qualify a different build.

If node startup reports "Too many open files" or cannot create a file watcher, follow the [host inotify guide](../../documentation/src/content/docs/operations/deployment.mdx#inotify-limits-for-local-clusters). It distinguishes instance quotas, watch quotas and process file limits. It also covers temporary changes, persistent settings and the local starting value of 1024 instances. The harness does not change host sysctls, remove unrelated workloads or relax readiness. Keep this prerequisite failure separate from Catalog chart/runtime findings.

## Drain and disruption qualification

The topology scenario observes a granted fixture lock and exactly one blocked namespace query from the selected API Pod before voluntary eviction. It first proves ready membership in the Catalog Service EndpointSlice, then requires removal/non-ready termination with the original ready survivor, a denied second eviction, and a retained namespace response or the exact safe, correlated `503 REQUEST_DEADLINE_EXCEEDED` problem. Arbitrary 503/504 and transport errors fail.

One 25-second phase budget covers lock observation through response verification; independent request cancellation, remote lock termination and child reaping run on early failure too. Production work/shutdown budgets and database lease clocks stay unchanged.

Forced deletion separately requires the old Pod object to disappear and a different ready replacement beside the original survivor. Node-loss testing retains one ready replica and one hard-spread Pending replacement for both roles; the stopped node stays stopped until cleanup. Authenticated retained namespace reads, byte-exact idempotent replay, fresh commands and singular targeted audit evidence supplement liveness/readiness after each disruption.

The topology scenario also starts one restricted in-cluster client from the owned fixture image. A denied request to the API ClusterIP must time out. After the policy permits API traffic, 48 authenticated requests use separate connections to that same ClusterIP. Both independently identified API processes must answer; equal distribution is not required. The client has no proxy, DNS, redirects, retries or tunnel fallback.

Each request has a two-second timeout. Calibration has a 120-second ceiling and concurrency is one. The report records the ClusterIP, client Pod UID, image, backend process mapping and request counts. The existing drain and replacement ceilings remain 25 and 120 seconds. The calibration precedes the controlled Service disruption checks below. Earlier federation failures remain unexplained by later passing runs.

The topology fixture selects `podDNSOptions: [{name: use-vc}]` for chart-managed Pods. Native UDP DNS waits caused inspection deadlines before the fixture received a manifest request. An owned stack capture identified Go's DNS packet wait. A local control recovered all twelve inspections after selecting TCP DNS. The chart default stays empty; existing DNS policy still permits both UDP and TCP. Explicit UDP/TCP probe checks remain in the scenario.

Before eviction, the in-cluster client selects the original API through the ClusterIP and creates one Namespace. It identifies the process and sends the command on the same connection. The client confirms a 201 header, closes the connection, and returns no receipt to its caller. This is deliberate receipt loss. The owner records only the retained receipt's hash, status and ETag. A separate API read checks current Namespace state; database timestamp precision can differ from the original receipt.

A completed source synchronization Job supplies database-backed reads while the namespace table is locked. Two Service reads run before eviction. The test cordons the selected node to hold the hard-spread replacement Pending. After the EndpointSlice removes the selected API, four fresh connections must return the exact Job state through the original ready survivor. The Deployment must remain at one of two ready replicas. An other-tenant read must return 404. The same creation key must replay the retained response through that survivor.

The original SQL-blocked selected-Pod read, denied second eviction and safe response checks remain in place. The test releases the lock and restores scheduling, including independent cleanup after failure. One shared 120-second deadline covers recovery. Two Service reads then check the retained Namespace through independently identified current APIs. After the temporary reader grant is revoked, replay must return 403. Direct SQL requires exactly one Namespace, creation audit and outbox event.

The report records process/Pod identities, EndpointSlice readiness, response hashes, configured counts and the selected read's status. The required post-transition reads are positive controls within a controlled degraded interval. They do not establish zero-error traffic during endpoint changes or a general latency objective. Voluntary eviction, forced Pod loss and node loss keep their separate assertions. On federation failure, bounded DNS/TLS probes and the existing closed retry checkpoint schema supply diagnostics without private responses or credentials.

Service port-forwarding selects one Pod; it does not qualify every load-balanced route. The existing packaged command-drain/replay and PostgreSQL stale-owner tests retain their distinct scopes. This campaign adds no production HA controller or chaos framework.

## API autoscaling qualification

Run `make test-minikube-autoscaling` with the pinned Minikube prerequisites. First cache the external test image:

```sh
docker pull registry.k8s.io/metrics-server/metrics-server:v0.9.1@sha256:9b0ba1926e1d3089673f45461c3c1416109dea33b3ba3434048defd380df329b
make test-minikube-autoscaling
```

The scenario owns one cluster, an external `autoscaling/v2` HPA and Metrics Server. Neither dependency becomes part of the application chart. The [fixture provenance](fixtures/metrics-server/README.md) records source checksums and local TLS exceptions. The test rejects missing CPU metrics within five seconds before it installs the provider. Catalog readiness and runtime RBAC do not depend on metrics access.

The initial deployment has two APIs and two workers. The HPA selects `catalog-api`, with a minimum of two and a maximum of three replicas. Each API requests 50m CPU. The utilization target is 50 percent. Scale-up permits one Pod per 15 seconds, without stabilization. Scale-down permits one Pod per 60 seconds, with a 60-second stabilization window.

Eight readers issue local Search requests for three minutes. Each reader has a 100-millisecond tick and a maximum of 1,800 requests. Requests have a two-second timeout. Direct Pod tunnels select the original two APIs. This is not a Service load-balancing test. Results must match the initial complete, tenant-scoped page. Only the generation may change during source updates. Authorization policy stays stable during load.

The test requires actual ready replica counts of 2, then 3, then 2. Recommendations alone do not pass. It stops Metrics Server, requires metrics API failure and an inactive HPA condition, then observes useful work for 45 seconds. It restores the provider and requires fresh metrics before the final scale-down. Missing metrics do not guarantee scaling protection.

At each stage, the test checks exact command replay, ungranted Resource-write refusal on every API, tenant isolation and a completed source synchronization Job. The test grants, revokes and restores reader access before load and after scale-down. Each cycle checks current-authority replay, revoked replay refusal, exact readback and one mutation audit. A policy change can invalidate an in-flight Search read.

Original workers must remain ready without restarts. Scale-up retains the original APIs; metrics loss retains all three; scale-down retains two of those three.

Each backend pool permits eight connections. The maximum steady-state runtime budget is `3 * 8 + 2 * 8 = 40`. Ordinary PostgreSQL capacity must cover those 40 connections, two owner connections, two observer connections and ten spare connections. This excludes deployment surge and migration overlap. Use the [operator worksheet](../../documentation/src/content/docs/operations/database-roles.mdx#replica-and-dependency-budget) for production limits.

Failed workload evidence retains at most 32 classified failures, reader lanes, elapsed times and fixed phase labels. It includes no response bodies, credentials or arbitrary problem details. A failed run prints this evidence before cleanup, because it cannot produce a successful final report.

If this bounded Search workload cannot cause scale-up, qualification remains incomplete. Do not add synthetic CPU work or enlarge the workload to force success. UI sessions are outside this scenario.

After the scaling experiment, the scenario freezes the API HPA at two replicas for deterministic refusal checks. It waits up to 90 seconds for two ready API and worker Pods from the current image. It also checks their service accounts, distinct UIDs and zero restart counts. It adds an unrelated HPA for the existing decoy Deployment. Both HPAs use real CPU metrics. The compiled maintenance coordinator must reject the API HPA without replacing or restarting runtimes. A second worker HPA requests an absent custom metric. Its observed `ScalingActive=False` state must not bypass the same refusal.

The fixture has no HPA recreator. It removes both targeting HPAs before maintenance and leaves the unrelated HPA present. It reuses the existing coordinated-upgrade checks: a real ledger lock proves zero old API/worker Pods during owner migration; failed migration and startup leave both roles stopped; retries restore two ready replicas from target values. Original smoke state must remain unchanged. The unrelated HPA specification, serving Pod identity and health must survive the complete sequence.

Only then does the fixture restore the reviewed API HPA configuration. It requires a new HPA identity, active metrics, two ready APIs and current command/authorization/worker readback. External GitOps suspension remains operator-owned. The [maintenance guide](../../documentation/src/content/docs/operations/maintenance.mdx#hand-off-an-external-autoscaler) explains that handoff and manual inspection after failure.

## UI network-policy qualification

The topology scenario tests the shipped optional UI policy through `ui_policy.go`. It reuses the existing Calico cluster and packaged UI login/API-read setup. The recorded Minikube topology qualification run passes all 47 topology checks, including the UI-policy matrix, federation and node-loss continuity. Independent owned cleanup and retained-environment checks pass.

Earlier UI-policy attempts observe DNS delays and failures. Other Minikube topology qualification attempts retain inspection deadlines and one UI-login transport failure before the final untraced pass. That passing run does not explain earlier failures or establish a DNS repair. Failed development-login requests now report a fixed transport category without URLs, cookies or private error text. Run `make test-minikube-topology` with the documented pinned prerequisites; no separate target or production policy engine is introduced.

The phase permits one narrowly selected fixture client into the UI and rejects an unselected ordinary Pod. It attaches a restricted fixture-only ephemeral diagnostic container to a real UI Pod, using that Pod's actual network namespace while leaving both production images unchanged. It checks API TCP access, fresh UDP/TCP DNS resolution, and denied TCP access to PostgreSQL, the private worker listener and a live unrelated peer.

The same unrelated peer is then explicitly allowed through `extraEgress` and denied again after removing that exception. The diagnostic container disappears with the owned UI Pods/cluster; it is never deployed by the application chart.

Every TCP observation uses the same numeric destination and a live permitted control before and after it. Three consecutive fresh results within a phase deadline establish convergence; connection refusal, process errors, missing/ambiguous output and late completion fail rather than count as blocks. Kubernetes and native Calico policy inventories must contain only the expected policies, since independent allows are additive.

The packaged authenticated UI-to-API read qualifies useful application traffic; port-forwards are not ingress-isolation evidence. The test does not claim browser/OIDC, node/probe isolation, external issuer routing or a production network design. UI-only changes preserve the original API/worker Pod identities.

Backend role policies, disruption and node-loss checks retain their existing scope. Failure diagnostics include the UI host alongside backend and fixture logs.

## Coordinated installation and upgrades

The basic scenario uses the same compiled operator tool as `make helm-deploy`, including its initial failed-hook/retry test. Both basic and topology scenarios run the retained upgrade phase with two API and two worker replicas. Topology requires each role to remain spread across distinct nodes before and after upgrade/recovery.

The phase observes the real migration hook blocked by a fixture-owned ledger lock with zero old runtime Pods, verifies invalid target values preserve Pod identities, checks a missing migration role leaves both runtimes stopped, and recovers from zero to the configured count. A missing API Secret then forces startup failure after successful hook completion; corrected target inputs recover without automatic rollback. Reapplication preserves governed smoke identities/state before restoring the selected scenario's baseline inputs.

The caller reopens its Service port-forward after old Pods are drained. These are same-current-format deployment controls, not a fabricated released predecessor or PostgreSQL major upgrade.

Use `make test-minikube-upgrade` for only the cross-node coordinated-upgrade qualification. It reuses the topology scenario, pinned prerequisites, complete upgrade assertions and independent cleanup, then stops before the separate UI-policy/federation/disruption checks. Its JSON report explicitly sets `upgradeOnly: true`; it is not a full topology pass. `make test-minikube-topology` still executes both upgrade and broader topology checks.

## Logical backup and restore

Run `make test-minikube-backup-restore` with the same pinned tools, cached images and optional preload archive. It creates a separate one-node profile with the application chart, two PostgreSQL instances and distinct persistent volumes. It uses no retained cluster, database or credentials.

The scenario populates governed records through the normal smoke fixture, then stops API and worker Pods. It captures all table counts/digests and the complete tenant inventory. A bounded fixture Job takes a private custom-format dump and restores it into the empty second instance. It provisions the exact runtime roles separately because `pg_dump` excludes cluster roles. The archive stays inside the fixture Pod. The Job removes it after success or a command failure; profile cleanup removes temporary storage after interruption.

The original PostgreSQL instance then stays stopped. The packaged CLI requires an unchanged migration plan, reconciles exact grants and verifies every page for both tenants. Every restored table must match its original count and digest before runtime startup. The coordinated Helm deployment selects new database Secrets. Retained resource/release reads, public bytes/ETag, local Search, exact command replay and cross-tenant denial must pass. It does not rerun smoke to recreate missing data.

The JSON report includes archive and inventory digests, page counts and distinct claim UIDs. Existing cleanup removes the owned profile, storage, network, images and private inputs. A failed check or cleanup fails the command. This scenario qualifies logical recovery on the recorded Kubernetes environment. The Compose backup drill separately checks recovery-bound audit manifests, queued work and signed webhook delivery. Physical backups, WAL/PITR and provider recovery guarantees require separate qualification.

## Fresh local AGNTCY Directory

Run `make agntcy-tools-install`, then `make test-minikube-agntcy` alone. This selected single-node Calico scenario uses eight CPUs, 12 GiB memory and a 40-minute overall deadline. Use the same common prerequisites and explicit preload archive as other scenarios.

It deploys the current Catalog chart, API/worker and dedicated PostgreSQL beside pinned Directory in a separate namespace. Native schema validation stays enabled against the owned OASF server. Private HTTPS, actual image identities, current version fields, byte preservation and healthy network-denial controls are asserted. The same run checks six stored/indexed native records, five paginated projections, filter behavior, artifact overlap, all conformance modes and PostgreSQL source/Job/snapshot bindings. Expected peer failures remain visible; an empty manifest does not establish nonempty federation. It uses no retained lab, memory Catalog or ambient kube-context. The [Directory guide](../interoperability/agntcy/README.md#fresh-owned-deployment) owns installation, bounds, reports, cleanup and the selected qualification scope.

## Native MCP Gateway Registry

Run `make gateway-tools-install`, then `make test-interoperability-gateway`, separately. The [Gateway guide](../interoperability/gateway/README.md) owns exact pins, license limits, native authentication/paging/schema controls, Catalog lifecycle and reports. This scenario reuses the one-node Calico supervisor, current managed Catalog image/chart, PostgreSQL 18, eight CPUs, 12 GiB memory and 40-minute deadline. It qualifies indexed source enrollment, persisted bytes/provenance, native add/disable/restore and same-Job outage recovery. Reverse import characterizes the unchanged native private-address guard. It does not alter the retained Directory lab or Compose stack.

## ANS discovery qualification

Run `make ans-tools-install`, then `make test-interoperability-ans`, separately. The [ANS guide](../interoperability/ans/README.md) owns exact inputs, development identity limits, assertions and reports. This scenario uses the existing single-node Calico supervisor, eight CPUs, 12 GiB memory and a 40-minute deadline. It verifies the current Catalog image/chart, API/worker and PostgreSQL 18 beside native RA/TL/Finder. Native controls precede live-only enrollment, exact paging, authenticated discovery, revocation and separate RA/Finder outage recovery. SQL reads bind source approval and audits; persistent peer claims survive the selected outages and are removed with the owned cluster.

## Explicit ANS registration assessments

Run `make test-trust-verification` separately after `make ans-tools-install` and the shared Minikube prerequisites. It extends the existing ANS scenario with real V1/V2 private assessment Jobs, PostgreSQL/audit assertions, offline CLI checks, corruption controls, cancellation, expiry and dependency recovery. Native checkpoint growth, signed history and actual RA/TL signer changes preserve retained state. The [ANS assessment guide](../interoperability/ans/README.md#real-local-assessment-qualification) owns its phase inventory and reports.

The owned HTTPS edge forwards only selected evidence routes. Every policy rollout drains API and worker before the owner hook. Each rollout records its redacted values hash. Existing metadata-only and capture targets retain their distinct scope. No retained environment, public peer, host tuning or new deployment harness is used.

## Optional HTTP MCP

The basic `make test-minikube` scenario enables MCP with non-default limits and an exact TLS Ingress. It verifies both protocol versions through the existing API Service, canonical resource metadata, governed Search and complete disablement. It creates only an owned TLS Secret and Ingress. This cluster has no ingress controller: its Service forwarding does not qualify ingress TLS. `make test-mcp-http` separately qualifies actual TLS proxy traffic and Authorization Code/S256 PKCE against the pinned local Keycloak fixture.

## Cert-manager and MCP ingress renewal

Run `make cert-manager-tools-install`, then `make test-minikube-cert-manager`, separately. Reuse the common Minikube prerequisites and optional explicit preload archive. The scenario owns one Linux AMD64 node, four CPUs, 6 GiB memory and a 40-minute deadline. It leaves retained Compose services and other clusters unchanged.

The [pin file](fixtures/cert-manager/pins.json) fixes both chart archives, CRDs through the cert-manager chart, four image digests and the cmctl executable. cert-manager v1.21.3, Traefik v3.7.16 and cmctl v2.6.2 have exact owner-approved security age exceptions. Traefik chart 42.0.0 meets the ordinary cutoff. Acquisition verifies checksums and stores artifacts, licenses and the CLI's upstream SPDX inventory under ignored `.tools/minikube-cert-manager/`. Normal test execution does not acquire dependencies or use floating image tags. `make clean` includes this tool cache.

cert-manager runs in its own namespace and Helm release. Traefik has a separate release in the Catalog namespace, namespace-scoped RBAC and no dashboard, CRD provider, Gateway provider or public Service. The Ingress uses the explicit `catalog-local` class annotation. The Catalog release keeps its existing MCP-only routes and TLS Secret reference. Catalog ServiceAccounts gain no certificate or Secret permissions.

The fixture creates one private local CA, one namespaced Issuer and one Certificate for `catalog-mcp.test`. Its leaf lifetime is two hours, with renewal thirty minutes before expiry and `privateKey.rotationPolicy: Always`. The operator fixture waits for initial issuance and validates the exact key pair, SAN, server usage and chain before starting the ingress consumer. Clients trust only the independently retained public CA.

Each TLS probe connects to an owned literal-loopback tunnel while preserving the public Host and SNI. It uses no ambient proxy, redirects, keep-alive reuse or TLS session resumption. Fresh connections must serve the expected leaf fingerprint, serial and public key. The test verifies both MCP protocol versions, governed Search, canonical OAuth metadata, authentication, Origin enforcement and refusal of routes outside the two exact MCP paths.

cmctl triggers manual renewal without expiry sleeps or host-clock changes. The Secret and actual served leaf must change together, including the public key. A missing-Secret Issuer reference then blocks a second renewal. The last valid certificate must remain served during three observations over six seconds. Restoring the original reference must finish that pending renewal without another renewal command. The CA remains unchanged, and API, worker, ingress and certificate-controller Pods retain their UIDs with zero restarts.

Replacement polling has a two-minute deadline. Secret reads share that deadline and set a three-second Kubernetes request timeout. A local subprocess regression proves that caller cancellation stops a blocked read without waiting for the overall scenario timeout.

Wrong-host, unrelated-CA and expired-client-time probes must fail at their specific X.509 guard. The expiry probe selects a later time only in its own TLS client; it does not alter host time. Separate local TLS functional tests also serve an already expired leaf and prove rejection before HTTP. Those helper tests do not replace the real controller scenario.

The report contains only public certificate fingerprints, serials, expiry, image identities and fixed outcomes. It retains no private keys or tokens. The test removes its Certificate and Issuer before normal cleanup. Independent supervisor cleanup removes the entire private cluster, network, local image aliases and temporary inputs after failure too. Upstream images and verified tool archives remain cached until explicit cleanup. The [operator procedure](../../documentation/src/content/docs/operations/deployment.mdx#optional-mcp-ingress-certificates) covers setup, renewal, issuer recovery, expiry signals and shared-resource precautions.

## Federation between two independent Catalogs

Run `make test-minikube-peers` with the same Minikube tools, cached images and explicit preload archive. This scenario uses one owned node with eight CPUs and 12 GiB of memory. It deploys the application chart in two namespaces. Each Catalog has its own PostgreSQL instance, persistent claim, database credentials and OIDC signing keys. The deployment coordinator drains only the selected namespace.

Private TLS frontends forward discovery to the actual Catalog APIs. They preserve response bytes, status and validators. The APIs use an exact, tenant-assigned private-network policy and trust bundle. No public peer, retained instance or application fetch exception is used.

The test publishes Agent, MCP Server and Skill records with independent author, reviewer and publisher identities. It checks exact entry bytes, source attribution, one-item pages, unchanged synchronization, updates, withdrawal and reciprocal federation. Draft, private, tenant and wrong-issuer controls must fail before disclosure. A controlled frontend outage preserves the last complete snapshot and local results. The same Job must recover after the fault is removed.

Both API/worker pairs then restart. Native records and synchronized observations must survive in their separate databases. The final report requires both phases and distinct persistent-claim identities. The existing supervisor owns cluster, process, image and temporary-file cleanup.

This scenario qualifies federation, not database replication or node-loss availability. The separate four-kind `make test-federation-peers` Compose journey adds Plugin archive authoring and API/CLI/browser continuation checks. Its development-only private archive exception is not enabled in Helm.
