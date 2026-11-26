# Qualify Agentic Resource Registry discovery

This optional test runs an independent Agentic Resource Registry beside a disposable Catalog. The registry authors and approves its own synthetic records and serves its normal anonymous ARD endpoints. Catalog inspects the publication over private HTTPS, indexes its entries and sends authenticated readers' live searches to the approved source.

## Run locally

Start at the repository root with the [build prerequisites](../../../documentation/src/content/docs/contributing/build-and-release.md#prerequisites-and-identities) and Docker Compose connected to a local Linux engine. Install the shared Python tools once, check the ARD validator and prepare the images:

```sh
make dev-tools-install
make conformance-prerequisite
make container-build
make agentic-registry-tools-install
```

The install target downloads the upstream source archive with the GitHub CLI, verifies its SHA-256 and builds the peer's Dockerfile. Set `ARR_SOURCE_ARCHIVE` to reuse a downloaded copy of the same archive. The selected server is [AgenticResourceRegistry/AgenticResourceRegistry at d587d73](https://github.com/AgenticResourceRegistry/AgenticResourceRegistry/tree/d587d73df6bbdde4be2adcf4f834dca89c2902d9), licensed under Apache-2.0.

Catalog uses the pgvector image from its Compose file. The peer has its own PostgreSQL, Redis and ClickHouse images, each with its upstream license. The upstream build installs its frozen Python packages and Debian packages. The preparation receipt records the platform and image IDs. Separate inventories list installed Python distributions and OS packages. Third-party code and built images stay on the developer's machine.

Run the test offline with the prepared images:

```sh
make test-interoperability-agentic-registry
```

The test uses `open-agentic-catalog:dev` by default. Set `ARR_CATALOG_IMAGE` to select another locally built Catalog image. At startup the test resolves the selected Catalog image and runs the registry and fixture by their prepared image IDs. Moving a local tag during the test leaves that run on the same images. Each invocation creates its own internal networks, databases, fixture credentials and TLS authority. The HTTPS listener forwards only anonymous discovery traffic to the native registry. The registry's author and curator use a separate private connection. The test publishes no ports and executes no discovered commands.

Open `report.json` in the run directory printed by the target. A successful run records `"status": "passed"` after the PostgreSQL readback and `"cleanup": "complete"` after checking for remaining containers, volumes and networks. If a prepared image is missing or its ID has changed, rerun `make agentic-registry-tools-install` before repeating the test.

## What the results mean

The peer implements ARD v0.91 at `b76f235`. Catalog validates its output against `ard-0.91-aa3e598`. The public Skill tests whether its native version and metadata survive import. The public MCP Server supplies the positive reader Search case. [Peer's ARD version decision](https://github.com/AgenticResourceRegistry/AgenticResourceRegistry/blob/d587d73df6bbdde4be2adcf4f834dca89c2902d9/docs/adr/0001-agentic-resource-discovery.md#decision-1-pin-the-specification)

| Read | What to look for |
| --- | --- |
| Native manifest and List | A Registry advertisement plus approved public resources in the manifest. List walks resource rows one at a time until the peer stops returning a token. The private approved Skill, public pending Skill and archived Skill have authenticated control records and stay out of anonymous discovery. |
| Public Skill | The peer publishes `application/ai-skill+md`. The pinned ARD checker accepts the entry with a media-type warning. Catalog accepts `application/agent-skills+md` and the ARD profile-qualified Markdown type. An administrator can read the original entries for versions `1.0.0` and `1.1.0`. A native name search finds the approved version. Catalog's indexed Search omits it, and live Search returns an empty, partial page with an `invalid_response` diagnostic for the Source. |
| Public MCP Server | For the native `application/mcp-server-card+json` entry, compare the version, identifier, upstream score and Registry URI across the native and Catalog live responses. Catalog ranks the result separately. A reader without permission to search live receives HTTP 403 and the peer receives no Search request. |
| Public Prompt | The vendor media type and publisher metadata can be inspected through authorized raw evidence reads. An unsupported Prompt does not acquire a Catalog resource kind. |
| Changes | The Skill advances on one native Listing. While the new review is open, readers still get the approved version. The recommendation change updates metadata on the same version. Archiving the Skill and MCP Server marks their previous observations as `withdrawn`. An administrator can still read their original entry bytes. The MCP Server disappears from native, indexed and live Search while the public Prompt remains in the manifest. |
| Public discovery disabled | The peer keeps its Registry advertisement and returns no anonymous Search results. Even with a fresh inspection and the current ETag, updating the existing Hybrid Source to use the Registry-only interface returns HTTP 412. A failed refresh leaves its last complete snapshot readable. The test restores the original interface and synchronizes the same Source again. |

The recovery test stops the registry API, verifies an unsuccessful refresh preserves the earlier import, restarts the peer and completes a fresh read. Restarting the Catalog API and worker then checks previously stored source evidence.

The harness runs the pinned ARD manifest and complete-entry checker on native bytes, checks native Search and List against the pinned Registry OpenAPI, and checks the separate HTTP 501 response for Explore. A native List walk and a Catalog document synchronization exercise different paths. The peer selects up to 5,000 approved public local resources and adds its Registry advertisement separately. The test uses a small corpus. A complete List walk on that corpus does not establish that a larger manifest contains every resource. The campaign exercises anonymous upstream discovery only. It does not qualify upstream federation, private-resource import or resource installation.
