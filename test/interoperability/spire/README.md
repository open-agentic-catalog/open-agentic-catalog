# SPIRE workload reads

The reporter runs as a registered Unix user and calls its local SPIFFE Workload API. It uses the shipped Go SDK to read a private Resource and a reviewed release from an authenticated ARD host. The reporter also runs the shipped `catalogctl remote capabilities` with a compact JWT-SVID in its private tmpfs and the Catalog CA. The test checks the returned ARD profile and operation. A separate workload has permission to read just the publication. The test uses a real SPIRE Server, Agent and OIDC Discovery Provider, Catalog API and worker, and PostgreSQL.

For Kubernetes, run `make spire-tools-install`, then [`make test-minikube-spire`](../../minikube/README.md#optional-spire-workload-authentication). The Helm scenario uses the same native binaries, test certificates and SDK consumer. It switches a Catalog from its ordinary issuer to SPIRE and back.

## Run locally

For isolated checks, run `make test-unit` and `make test-tooling-unit` separately. The Go verifier tests sign ES256 tokens in memory and supply the issuer's public keys through a test transport. The Python tests check archive extraction and credential renewal in temporary directories. Both suites run without built Catalog binaries, Docker, PostgreSQL or SPIRE.

Use Python 3.11+, the repository Go toolchain and SDK module dependencies, OpenSSL, and a local Linux Docker engine with Compose and bind mounts from this checkout. The fixture selects binaries for the engine's AMD64 or ARM64 platform. The installer uses the GitHub CLI to download the two [upstream SPIRE v1.16.0 release archives](https://github.com/spiffe/spire/releases/tag/v1.16.0). The tools are Apache-2.0 licensed and used only in the fixture. Both archives are verified against their platform-specific SHA-256 digests before extraction.

```sh
make spire-tools-install
docker pull pgvector/pgvector:pg18@sha256:38681dc6eaf55308e2a4c6de392d131652dcfac70e1263e35cd9b5ea3fe39782
make test-interoperability-spire
```

The selected target builds the backend image and SDK consumer and creates a unique Compose project. It binds the Catalog HTTPS ports only to loopback, on available ports. The API and worker use their normal database roles. The schema owner migrates the database and provisions the separate fixture administrator. The reporting processes share the Agent's workload socket and PID namespace for Unix attestation, with different UIDs and without its private state. The provider obtains the trust domain's public keys through that socket too.

The reporter checks that the activated manifest contains the reviewed release and excludes a validated draft already present when the snapshot was generated. The fixture also checks namespace and tenant boundaries, missing credentials and membership, token renewal and expiry, grant and membership withdrawal, signing-key rotation and discovery-provider outage and recovery. Wrong or absent issuer controls come from the real Server with the original trust domain, keys and datastore. Separate Catalog configurations exercise an absent OAuth scope and shared tenant claim.

The reporter revalidates the manifest with its last ETag. An unchanged publication returns 304 while the reporter has access. The same request returns 403 after its discovery grant is revoked and 401 while its membership is suspended. During a signing-key rotation, a workload retries the same unexpired token on both the Control API and the publication host after the issuer recovers.

The runner prints its report directory. `report.json` identifies the fixture records, public signing key IDs and completed checks. A missing prerequisite leaves the run marked failed at the stage that could not run. A passing report requires the workload checks and cleanup to succeed. Normal exit and interruption remove the project, its volumes and private inputs. If the host stops mid-run, the resolved Compose file in the printed run directory records that invocation's project name and mounts. Set `run` to that exact directory, then inspect and remove its resources:

```sh
docker compose -f "$run/compose.json" ps --all
docker compose -f "$run/compose.json" down --volumes --remove-orphans
```

The fixture image is tagged with that project's name and `:catalog`. Once its containers are gone, remove that tag and the private inputs under that run directory.

For a hands-on walkthrough, follow the [SPIRE tutorial](../../../documentation/src/content/docs/operations/spire-workload-identity.md). See the [operator configuration and troubleshooting guide](../../../documentation/src/content/docs/guides/integrating.mdx#read-a-private-resource-and-publication-with-spire).

## Renew an importing Catalog's source credential

To test the writer alone, run the following from the repository root. The tests supply the Agent's replies and a clock, then inspect the credential files before and after renewal, interruption and recovery.

```sh
python3 -B -m unittest discover -s test/interoperability -p test_unit_spire_renewal.py
```

For the two-Catalog test, use the same native SPIRE tools and backend build prerequisites as above. The separate writer and HTTPS fixture also need the pinned Python image locally:

```sh
docker pull python:3.13.18-bookworm@sha256:cc55e4ded9c95f6ce2133b3d096db43f214d280c2a8af65d83e246c8fa7d168c
make test-interoperability-spire-renewal
```

The runner starts two PostgreSQL-backed Catalogs in a unique Compose project. Only the registered writer (Unix UID 11007) can acquire the source-reader identity and write the binding. B's API and worker read its directory through group 65532. The writer stages an immutable token file, syncs it to disk, and commits the generation by renaming the metadata. If the active token file goes missing, the writer republishes its cached JWT under a fresh revision and keeps its original expiry. On restart it retires only its own expired token files after the read grace period.

The fixture reads A's authenticated manifest. For the mixed-generation test, a same-origin parent points to a child containing A's published entries. The fixture pauses between requests and compares the expiry of each JWT accepted by A with the generation used for that request.

Revoking the source reader's grant on A causes the next import to fail. A reader on B with only `federation.discover` can still find the last imported version and its provenance without contacting A. The same reader is denied access to B's source configuration. A separate test returns HTTP 503 and checks B's retry limit.

For the private CA rollover, the fixture pauses the writer and completes an import with the original certificate. A then serves a certificate signed by the replacement CA. B refuses the connection and retains the last complete snapshot. After B's API and worker load the updated CA bundle, a new import succeeds with the same unexpired bearer.

With credential resolution disabled, B refuses the bound Source before an HTTP request reaches A. The fixture also imports a separate public manifest without credentials and finds its Agent in B's index.

The test also reads B's `source.tested` audit events. A missing or expired binding records a transport error with no HTTP response. When A refuses the active bearer, the audit identifies the credential revision and the upstream HTTP error class. Both failures leave the tested Source revision unchanged.

The report under `output/interoperability/spire-renewal/` records the credential revisions, Job and snapshot references, process identities and cleanup outcome. Run `go test -race -tags=functional ./internal/federation ./internal/platform/fetcher` for the eligible-peer 304, private TLS, tenant/origin binding and redirect regressions. Follow the [federation guide](../../../documentation/src/content/docs/operations/federation.mdx#renew-a-source-bearer-with-spire) for deployment settings and recovery steps.

To qualify Kubernetes Secret delivery, run [`make test-minikube-spire-renewal`](../../minikube/README.md#optional-spire-workload-authentication). It uses the same native SPIRE tools and pinned Python image. At startup and after renewal, a successful Source test and a completed synchronization must report the active Secret revision. The test allows time for the API and worker to pick up the projected volume independently. An unknown revision or failed operation fails the run.

The Secret writer removes its superseded token keys once their expiry plus the 30-second grace period has passed. If the Agent still supplies the current token, cleanup preserves the active revision and unrelated Secret keys. The separately selected `make test-minikube-spire` covers inbound workload authentication on a single Helm release.
