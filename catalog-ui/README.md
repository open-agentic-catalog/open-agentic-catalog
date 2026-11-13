# Catalog UI

The Catalog UI is an optional standalone browser client for Catalog discovery, authoring, review and administration. The Go host serves React assets and forwards authenticated requests to one configured Catalog API. It has no database access. The backend works without this directory or Node.js. See the [test strategy](tests/README.md) for workflow and deployment checks.

Catalog marks unapplied text, refreshes applied criteria deliberately and compares two already loaded results without detail or peer requests. Saved searches retain their tab-local workspace through exact-detail navigation. Provider consent covers local suggestions as well as Search; explicit live-only Search keeps its separate disclosure consent. Local record and rating actions require replies bound to their requested references. Follow the [reader procedures](../documentation/src/content/docs/guides/search-cookbook.mdx#apply-text-refresh-and-compare-results) and [saved-search guide](../documentation/src/content/docs/guides/catalog-ui.mdx#save-resources-and-searches).

Full records offer fixed section links, keyboard focus below the measured header and a final return action. Readable Listing categories apply to captured criteria without submitting pending text. Downloads explain independent content admission beside the action; one selected-namespace lookup supplies hints for handoff and workflow links. A missing hint does not block another authorized reference. Current indexed reads retain labelled last-loaded metadata, while live artwork/rating refresh explicitly repeats its captured search and preserves drafts. See [result evidence and handoff](../documentation/src/content/docs/guides/understand-results.mdx).

The host has independent startup fields: `logLevel` (`debug|info|warn|error`, default `info`) and `logFormat` (`text|json`, default `text`). Empty/null/unknown values are rejected. `ui-dev` and local Compose render `CATALOG_UI_LOG_LEVEL`/`CATALOG_UI_LOG_FORMAT`; Helm uses `ui.config`.

Backend logging overrides do not configure the UI. Logs go to stdout; terminal failures go to stderr. Restart the host to change settings. Use external tools for capture and rotation. `make ui-build` and UI images embed `VERSION`/`COMMIT` into the safe startup record; direct Go builds default to `dev`/`unknown`.

See the [logging runbook](../documentation/src/content/docs/operations/observability.mdx#configuration-and-output).

UI host configuration, mounted key/user JSON and development-login controls use exact field names and reject unknown fields, explicit typed nulls and malformed Unicode. Omit optional settings instead of setting them to null; `scopes` may be omitted or an empty array when no extra OIDC scopes are configured. Arbitrary upstream standards fields keep their own contracts.

## Document comparison limits

Revision comparison uses `diff` (jsdiff) on verified original text. Line differences select blocks; character differences locate edits within each block. It does not use a JSON serializer, ignore whitespace or retrieve external content. The original text view keeps its independent 2 MiB limit per document.

Detailed navigation enforces these fixed bounds. Exceeding any bound discards all detailed output and leaves the original view available.

| Bound | Limit |
| --- | --- |
| Input per side | 65,536 UTF-16 code units and 2,000 LF-delimited lines |
| Line edit distance | 128 insertions/deletions |
| Character refinement | 8,192 code units per changed block, 32,768 across all blocks |
| Character edit distance | 256 insertions/deletions per block |
| Rendered changes | 100 regions, each with up to 60 Unicode code points of context per edge |
| Algorithm deadline | 50 ms for lines; 25 ms per character block, in addition to hard work bounds |

Excerpts mark invisible whitespace and retain original token spelling. Line/column locations count Unicode code points. Plain documents retain exact decoded text, including a leading BOM; equality uses verified SHA-256 artifact identities. Read/selection/session lifetimes remain owned by `RevisionComparison`. There is no persistent cache, worker or backend comparison service. See the [reader procedure](../documentation/src/content/docs/guides/resource-authoring.mdx#compare-revisions-and-inspect-lifecycle).

## Current scope

The lazy [Content overview](../documentation/src/content/docs/guides/catalog-ui.mdx#read-the-content-overview) shows independent Resource, current-release, indexed-record, Namespace and Publisher summaries. Each read applies its own permission and OAuth scope. Four kind tiles show Resource counts without a duplicate table. Explicit refresh, limited counts and independent failures retain their meanings. Fixed Resource-kind and Catalog-origin URL presets apply actual filters without storing private criteria. Review summaries show the queue, waiting times and median review time. Chart and Daily breakdown controls switch between a 30-day graph and its exact outcomes. The approved queue supports an exact unreleased filter. Publication summaries separate configured host audiences from recorded active metadata and known differences.

The Overview Administration tab has separate access, feedback and rating summaries. Each uses its own current permission and `tenant.admin` scope. No Resource-read permission or `catalog.read` scope is required. A single table shows People and Service identities by membership state. Compact panels show active access records, open feedback and rating states. Fixed state/type/category URLs initialize the existing directories and survive Reload and Back/Forward.

The Overview Operations tab uses private Source and Job summaries. Webhook availability and index status share one capabilities read. Enabled webhooks add endpoint counts, retained delivery states, latest retained failures and existing notification counters. Webhook-only access needs no `catalog.read`. Current permissions still gate each panel.

Endpoint status and exact delivery links preserve applied URL filters through Reload and Back/Forward. Empty sections use short messages. Populated Jobs use one exact state table; webhook evidence stays available when no endpoints remain. Source status links restore applied URL filters. Exact family and target permissions still govern Job counts. See the [Operations guide](../documentation/src/content/docs/guides/catalog-ui.mdx#read-the-operations-overview).

Federation source and observation inventories use staged filters with explicit Apply/Clear and exact-reference openers. Source names and states filter before server paging. Test receipts show recorded retrieval outcomes; source confirmations compare captured settings. Preview replies must match the captured Source, tenant, revision and intent and contain correctly formatted fingerprints. Inspection/preview expiry updates locally without network calls and blocks new sends, including open dialogs; uncertain commands retain exact-key recovery. Explicit reinspection on the same page preserves connection settings and reason while requiring review of fresh evidence. See [source operations](../documentation/src/content/docs/operations/federation.mdx#find-configurations-and-retained-evidence).

Authoring keeps identity and content in one workspace. Next/Back preserve local input without writes; one confirmation saves the Resource and Version through separate captured commands. An AIR builder, local Agent/MCP/Skill examples and optional previews/downloads help prepare content. Identity-only creation, local JSON/Markdown import and advanced representation settings remain available. Import draft JSON stages a reusable file before replacing fresh editor input. Download draft JSON captures active fields; saved revision and local Catalog downloads use exact API bytes. Destination selection and publication remain separate. Lazy editors generate Agent Cards, MCP metadata, Skill Markdown and Plugin files from fields. Imports preserve untouched metadata; signed Agent Cards need explicit signature removal before guided changes. Authors package and host complete Plugin archives. Optional Listing details share the final confirmation. New Versions can reuse exact sealed or unsealed sources. Save receipts retain validation, correction input and the Version ETag. Listing saves use acknowledged content references. See [local authoring](../documentation/src/content/docs/guides/catalog-ui.mdx#load-a-local-json-or-markdown-document) for limits and steps.

Start with the manual's [Agent, MCP Server and Skill field tutorials](../documentation/src/content/docs/guides/resource-authoring.mdx#create-an-agent-from-fields), or its [Plugin tutorial](../documentation/src/content/docs/guides/agent-plugins.mdx#create-a-review-tools-plugin-in-the-browser). The [publication tutorial](../documentation/src/content/docs/guides/publishing.mdx#publish-an-agent-in-the-browser) covers the separate author, Reviewer, Publisher and snapshot-administrator steps. It requires no two-Catalog setup. Recovery instructions preserve acknowledged writes and exact retry keys. A new Version leaves its sealed source and visible release unchanged.

Skill authoring uses labelled name, description and Markdown fields, with optional license, compatibility, string metadata and allowed-tool declarations. Preview/download and Advanced edit the same document. Unsupported YAML stays unchanged in Advanced. Complete-package guidance lists the inspected root, bounds and immutable Git/OCI handoff. Adding a primary document preserves the archive and makes its role distribution. See [Skill authoring](../documentation/src/content/docs/guides/resource-authoring.mdx#write-a-skill-from-fields).

Implemented: development identity selection, OIDC code/PKCE login, encrypted cookie sessions, authenticated self-context, unified Search, kind and metadata filters, bounded Previous/First/Next cursor navigation, cards/table views, local record previews with reference copy and retained-document downloads, deep links, external metadata previews, appearance preferences and sign-out. My access pages through the caller's namespace grants and assignment sources, with permission-based task links, without loading the full tenant assignment inventory.

The sidebar orders Discover, Manage content, Operations, Administration and Help. Catalog stays first and remains the landing page when discovery is permitted. Overview follows Catalog. The top-right Account disclosure contains My access, permitted personal pages and Sign out. Appearance remains separate and available before sign-in. Each destination checks current permissions and applicable OAuth scopes.

Bounded role, group and direct/group scoped-assignment screens support reasons, optimistic revisions, impact and retained revocation history; built-ins remain read-only. Readers can inspect governed history without creation links implying write permission.

Incomplete namespace grants offer a neutral namespace chooser; only the selected namespace is resolved before enabling its command. Fixed page titles and parent breadcrumbs omit private record/query text. My access retains exact tenant and identity context.

See the [navigation guide](../documentation/src/content/docs/guides/catalog-ui.mdx#choose-a-workspace-area).

The responsive workspace presents Agents, MCP Servers, Skills and Plugins with shared labels and local icons, compact kind selectors, a search/filter toolbar and a side-panel preview. All kinds clears only the kind selection and cursor, preserving the other submitted criteria. Categories, Origin and permitted Namespace buttons open one shared filter draft. Categories use one bounded local Explore read for the applied search; external-only searches use manual entry. Counts exclude external results. More filters groups license, visibility and exact publisher selection; draft changes take effect on Apply. Blank text uses name order; submitted text uses relevance order.

Kind selections match any selected kind, while every exact tag must match. Result-card tags apply immediately without submitting draft search text. Applied search criteria have individually removable chips; Clear all resets the search.

Appearance in the header controls theme and density. Filters and previews support Escape with focus restored to their opener. Kind glyphs and styles are bundled locally. Optional listing and source artwork is read through the authenticated Catalog API, checked against its Content-Digest and displayed from revocable Blob URLs; the browser never requests a publisher image origin. Card icons load lazily with at most four active image reads. Detail previews provide alternative text, captions, rights attribution and keyboard gallery navigation.

See the [browser guide](../documentation/src/content/docs/guides/catalog-ui.mdx) for filters, demo visibility, session behavior and troubleshooting.

Full local records show the identifier, version and Publisher ID. Independently permitted metadata, content and history actions link to Resource/Listing metadata, retained revisions, approved review and release details. Unknown exact namespace hints are resolved only for the selected namespace. Use this resource provides explicit copy actions and authenticated, digest-verified downloads of retained artifacts; each representation can disclose already-loaded decoded metadata. That display reconstructs JSON and can round numbers; it has no copy/export action. Inline JSON and transient archives are not original-byte downloads. Links preserve the configured UI base path and do not prefetch private evidence. Release history links back to a discoverable record only for the current published or deprecated release; retired, superseded and revoked releases remain inspectable through their history screens.

Namespaces support bounded list/detail, creation and metadata updates. Publishers support bounded list/detail, creation, metadata/domain updates and publication-domain approval/revocation. Jobs support bounded list/detail, explicit refresh, eligible cancellation and retry. Service information displays the actual build, profiles, capabilities, limits and degradations. Navigation and controls use current action-permission and OAuth-scope hints. The API remains authoritative.

Search indexing opens and focuses the existing Service section with its own active navigation state. Service shows labelled capability/provider observations, limits with units, bounded build diagnostics and explicit process-reference copies. Indexing counts retain their tenant permission and OAuth ceiling.

Reindexing can select one observed active public snapshot from a bounded host inventory under independent publication-read access; exact-ID entry remains available. Confirmation and the accepted Job retain the captured population and any inspected host/audience. Reload does not infer private request context. Follow the [indexing procedure](../documentation/src/content/docs/operations/hybrid-search.mdx#indexing-and-recovery).

Feedback and rating inventories provide explicit refresh and exact case-ID lookup. Their same-tab detail return retains one applied list context in private memory and rereads it through current authorization. Reload and identity/access changes clear that context. Case reads have a separate `catalog.read` ceiling; moderation inboxes and commands retain `tenant.admin`. See [feedback](../documentation/src/content/docs/guides/feedback.mdx#open-an-exact-case) and [ratings](../documentation/src/content/docs/guides/ratings.mdx#edit-withdraw-and-moderate).

Namespace and Publisher inventories offer bounded exact name plus slug/declared-domain filters. Apply and Clear are explicit; both supplied filters must match, unused fields are omitted, and Refresh preserves the applied criteria. Duplicate names keep their stable IDs. Administration inventories and choice pagers have distinct accessible names and busy states, retain selected off-page IDs and keep unrelated cursors and unsent drafts. Feedback, Rating moderation and Audit headings link their owning guides through the configured manual policy. See [directory lookup](../documentation/src/content/docs/guides/resource-authoring.mdx#manage-namespaces-and-publishers).

The Jobs menu requires tenant `job.read`, a readable Job family and `catalog.read` when scopes are enforced. Family access comes from `source.read`, `source.read_evidence`, `publication.read` or `operations.reindex`. It does not guarantee matching Jobs exist; the API filters each target. A direct route explains missing family access without requesting Job data.

Jobs provides exact kind/state/retry-parent filters and a UUID opener. Apply and Clear restart pagination; browser history retains applied filters. State and progress guidance explains cooperative cancellation, retry children and lease reclaims. Command receipts bind the current tenant, expected kind and exact Job or distinct child; malformed acknowledgements retain exact-key recovery. Completed indexing can show a recorded generation and accounted input count, including reused vectors and failed inputs. See [Job lookup, results and recovery](../documentation/src/content/docs/operations/observability.mdx#manage-jobs-in-the-ui).

ANS Job cancel/retry requires `source.read_evidence`, `source.manage`, `federation.discover`, the selected Job action and enforced `catalog.write`. Cancellation does not require current assessment enqueue eligibility; retry rechecks eligibility on the server. Both actions require confirmation and a reason.

An uncertain response retains the same command/key; retry creates a new Job linked to its original. Private ANS results remain a separate explicit read.

Authoring covers Resources, immutable version/listing revisions, validation, retained documents, Skill package uploads and Plugin HTTPS archive inspection. Plugin views show retained manifest/component findings and archive evidence; the backend fetches and validates the archive without installing its components. Bounded histories reopen saved drafts without browser-only ID storage. Authoring also finds exact canonical identifiers across authorized pages and combines this with kind. Reviews show independently authorized resource identity and exact submitted listing name, retaining the ID/state queue when context is unavailable. Reviews inspect exact submission-bound evidence, enforce independent review and publish approved listings; release disposition and host-bound discovery snapshots have separate controls. Explore offers local browse and facets without mixing in federation counts.

Listing metadata edits retain the current version revision by default, with an explicit selector for replacement. Guided artwork fields capture HTTPS URLs, alternative text and declared rights without asking authors to calculate digests. A native icon choice comes only from already loaded native metadata and still requires an explicit rights declaration when used in a listing. Source artwork defaults off. Source administrators can inherit, replace or suppress each slot for an exact foreign entry; indexed groups offer only their actual bounded source contributions, and selecting an alternate source does not change presentation ordering. Guided relationship rows cover every supported relationship type and optional target version. Listing revisions and submitted review evidence share a readable metadata summary, with raw JSON available separately. The review summary reads only the exact submitted revision when inspection is requested; it does not prefetch evidence for queue rows.

Form validation opens collapsed sections containing invalid fields before the browser focuses them. Relationship add/remove controls move focus to the new or remaining row, or back to Add relationship when none remain. Hiding submitted evidence confirms before discarding an unsaved review reason. Shared authoring controls live in `src/views/authoring.tsx`; `src/views/listing-metadata.tsx` owns relationship input and the listing/review metadata summary.

Federation covers inspection, connection, scope/disclosure settings, preview-bound updates/removal, test/sync/pause/resume, snapshots, observations, exact raw/occurrence reads, conflict groups, adoption and indexed/live diagnostics. Members and access covers direct grants and membership lifecycle, compact grouped permission editing, named bounded role/member/group/namespace choices with manual exact-ID fallback, explicit assignment scope, and one selected revocation editor. A membership binds an existing issuer identity. It does not create an issuer account or issue tokens.

The [development roster](../documentation/src/content/docs/guides/catalog-ui.mdx#development-identities-and-role-handoffs) preserves six personas and adds fourteen exact-template identities. Display text and exact templates are configuration-driven; production OIDC is unchanged. Webhooks honor the disabled capability, expose one-time signing secrets only in memory, and provide endpoint, delivery and reconciliation controls.

Audit supports bounded investigation and exact export downloads with independent read/export scopes.

Audit event references can be copied or staged as exact filters without a read until Search. Staging preserves the applied UTC interval in local-time controls. Administration acknowledgements retain operation, target, returned revision and HTTP attempt ID, with a private audit handoff for current auditors. Retry IDs are not used as original mutation filters. Settings history reads exact paged audit records on request; validated before/after overrides become a private draft against a freshly read base and API defaults. Restoration still requires a new reason and save confirmation. See the [Audit guide](../documentation/src/content/docs/operations/audit.md#investigate-an-acknowledged-administration-change) and [Settings history procedure](../documentation/src/content/docs/reference/configuration.mdx#read-history-and-restore-overrides-to-a-draft).

Help includes **Getting started**, **About**, **Troubleshooting**, **API reference** and **Documentation** when available. The first three pages work before sign-in after valid bootstrap; API reference requires authentication.

Documentation opens the public manual in a new tab. The UI image serves its same-source manual at `/docs/`, even with a non-root UI base path. An explicit credential-free HTTPS `documentationUrl` overrides the link. Without a bundle or override, the Documentation link is absent. External manual availability does not affect login or readiness.

API reference groups the connected backend's OpenAPI operations into declared API areas. A desktop index or narrow-screen selector jumps to a section without changing filters. Combine local text search, HTTP method and exact all-tag area filtering, then use Clear filters to recover. Each operation has one display home under its final tag, so section counts differ from overlapping tag matches. Native section and operation disclosures preserve operation expansion through filtering.

Operations show readable authentication alternatives, inherited parameter facts, request media types and response codes/headers before complete decoded data. Required, optional and anonymous admission remain contract declarations, not current permission checks. Missing or malformed metadata stays unavailable. Local schema/shared-definition links preserve their exact names.

Copy controls retain method/path placeholders, the declared operation ID and supplied examples; strings copy verbatim and other decoded examples use JSON encoding. Download OpenAPI YAML retains the exact served bytes.

The UI host's authenticated, same-origin `/_reference` read converts only the fixed upstream `/openapi/control-v1.yaml` document, bounded to 1 MiB of YAML and 4 MiB of JSON output. No external references are fetched, HTML is not rendered, and browsing never executes documented operations. Documentation configuration is not required for API reference.

Definitions has a complete-index name/description search, component-kind filter, 25-row local pages and an independently retained selected detail. All component kinds remain inspectable. Schema summaries keep declared properties, requiredness, annotations, composition and boolean values distinct, with explicit depth/node/row limits and complete decoded data. Known nested links and bookmarks preserve exact local references; hidden origins or linked operations require explicit Reveal before filters clear.

Refresh retains the last loaded document/YAML pair during pending or failed reads and reconciles compatible state against every successful replacement, including the same API version. Removed choices/targets are explained. Session/access changes clear the mounted workspace through the existing shell key.

Parameter disclosures follow the resolved location/name through insertion, removal and reordering. Declarations without a usable identity reset their disclosure on successful replacement. Pending and failed reads retain the loaded declarations and their state.

The [operation-to-workflow inventory](src/api/workflows.test.ts) classifies every API operation as a direct client interaction, equivalent capability, supporting read, public discovery transport, health/metrics endpoint. This is an interaction inventory, not proof that every role, error and packaged browser scenario has passed. Optional container/Compose/Helm wiring is implemented. The [workflow matrix](tests/README.md#workflow-matrix) identifies browser assertions and shared lower-layer tests.

Source settings accept a named ANS policy or an explicit clear through preview/apply. The ANS registration tab selects current indexed entries through explicit audited raw reads. Live diagnostic results retain their original query text and server digest for selection. Assessment requests require source management, evidence and discovery permissions; source editing retains its separate admission. Confirmation freezes the selection and reason. Unknown outcomes retain the original key/body; stale selections need a fresh source/query. Job acceptance is not a positive registration result. ANS assessments provides explicit audited reads, historical lookup and exact evidence downloads under its own permissions and OAuth scope. Follow the [UI procedure](../documentation/src/content/docs/guides/catalog-ui.mdx#request-an-ans-registration-assessment).

The isolated Playwright suite exercises record-to-workflow links, member grants/revocation and suspend/reactivate/offboard, independent approval followed by publishing, release deprecate/retire/revoke, and discovery snapshot build/activate/rollback/unpublish. Its enabled-webhook fixture generates a temporary encryption key and verifies creation/editing, masked one-time secrets, emergency rotation, a policy-denied test delivery and disabling. It uses literal loopback to test denial before connection, not an external receiver or successful-delivery claim. All fixtures are removed after each journey; existing local catalogs are not changed.

Webhook metadata fields stay disabled for read-only delegates and follow the latest successful endpoint read. Editable fields retain their private draft through readback. Permitted actions have separate explanations and a usable initial selection. Update confirmation compares saved/proposed configuration and explains queued-work effects. Permanent disable can seed a private replacement draft without re-enabling the original or replaying old events. New signing material resets masking and copy acknowledgements, and replacing an uncleared signer requires deliberate acknowledgement. Follow [endpoint management](../documentation/src/content/docs/operations/webhooks.mdx#manage-endpoints-in-the-ui) for readback, replacement and clipboard limits.

Common event choices and **More public event types** cover the complete public vocabulary with exact selectors. Advanced entry normalizes documented lowercase aliases and rejects unknown names before confirmation. The setup checklist links to the configured manual without reading private deployment policy. Non-secret rotation receipts retain the exact observed signing version and known deadline after plaintext is cleared; a later different-version/disabled read invalidates that guidance. Full-page reloads do not recover deadlines or keys. Routine conflicts never automatically select emergency rotation.

Commands require explicit confirmation and retain their exact idempotency key/body/ETag for a deliberate retry after an uncertain response. There are no automatic mutation retries. Reload after a stale ETag; the UI does not overwrite a newer revision.

Overview includes an optional [Activity view](../documentation/src/content/docs/guides/catalog-ui.mdx#read-live-api-activity) at `/overview?view=activity`. The backend gate defaults off. Current tenant-wide `access.manage` and enforced `tenant.admin` are required. Exact counters describe one API process across tenants. Manual refresh is the default; visible-tab live refresh is opt-in. Samples remain in memory, with no retained history or collector dependency.

Unsaved form input stays in memory, with navigation/sign-out warnings; reload, session expiry or another tab's session change can discard it. Successful writes are stored by the backend, not browser storage. Jobs and delivery status use manual refresh without background polling.

Use the shared Field hints and About controls for technical input guidance. Keep consent, privacy and destructive-action warnings visible. Optional help must not submit forms, change settings or trigger reads. Reuse semantic theme/density variables and wrapping action toolbars; verify spacing in browser tests, not jsdom. Keep short table states readable while long identifiers wrap inside horizontally scrollable tables.

See the [browser guide](../documentation/src/content/docs/guides/catalog-ui.mdx) for workflow details and permissions.

Retained assignment attribution is available for active and revoked inventory rows. My access shows each current role's permissions and direct/group provenance; OAuth ceilings remain separate. Publisher-domain revocation receipts retain exact release IDs and withdrawn hosts with copy controls and no extra detail reads. Receipts clear with the publisher or identity.

Source readers can explicitly inspect retained artwork heads and bounded history in an external-entry panel. Read and edit permissions remain independent, and image delivery keeps its discovery admission. Names, descriptions and reasons use API code-point limits. Indexed diagnostic text retains its byte budget; live diagnostic text uses its character budget.

Completed commands focus their acknowledgement. Explicit Grant/Add another controls preserve the last receipt and prepare no request until the next submission. Listing revision acknowledgements reconcile the current candidate and validator; review submission still selects the server-current candidate at execution. Webhook management requires explicit endpoint readback after a revision change, without discarding drafts or signing secrets. Rating and artwork receipts survive readback failures. See [form recovery](../documentation/src/content/docs/guides/catalog-ui.mdx#complete-forms-and-recover-safely) for the user controls.

## Build identity

`make ui-build` and managed UI image builds share the application VERSION policy with the backend, including full commit and clean/modified/unknown source state. Bare Go/raw Containerfile builds use dev/unknown defaults. `catalog-ui version` and `--version` print only embedded identity without loading configuration.

The same-origin GET `/_build` route follows the configured base path and returns these three bounded fields with no-store caching; it does not contact the API or expose sessions. Service information reads coherent process observations from the authenticated API `/api/v1/service-information` and the host's separate `/_status`. Each includes build identity, execution ID, UTC startup/observation times and monotonic uptime. Both compact timing handlers read process memory; API authentication still applies. The host timing read shares the `/_build` origin and drain protections. Build metadata is not deployment time or image attestation; process startup does not establish when an upgrade occurred. The browser shows receipt age and offers manual Refresh without polling or node enumeration.

See the [build guide](../documentation/src/content/docs/contributing/build-and-release.md#application-version-and-release-checks) for tools, version edits and artifact inspection.

## Local development

Use Node.js 22.23.4 and separately installed npm 12.2.2 for reproducible builds. `.nvmrc` pins Node; `package.json` records npm. With nvm, run `nvm install` and `nvm use` in `catalog-ui/` first, then install `npm@12.2.2` into that Node installation. The compatible Node range is 22.22.2+ within major 22, as required by npm and the DOM test tools. `packageManager` does not install or enforce npm; check `node --version` and `npm --version` when selecting a toolchain. Go uses the repository toolchain.

Install build dependencies once, then start the backend and client in separate terminals:

```sh
make ui-install
make run
```

```sh
make ui-dev
```

Open `http://127.0.0.1:4180` and choose a local identity. The launcher reads the existing synthetic identities from `config/local.json`, creates temporary session keys and removes its temporary files when stopped. Restarting this launcher requires signing in again. It does not create, seed, migrate or reset the backend. Use [the populated development catalog](../documentation/src/content/docs/guides/sample-catalog.mdx) against an owned local instance when sample resources are useful.

Source runs do not build the manual automatically. Prepare and select it when needed:

```sh
make docs-install
make docs-build DOCS_BASE=/docs/
make ui-dev UI_DOCUMENTATION_DIRECTORY="$PWD/documentation/dist"
```

The launcher validates and copies admitted manual files into private temporary assets without changing the source bundle. Ordinary tests need no documentation installation. Set `CATALOG_UI_DOCUMENTATION_URL=https://docs.example.com/catalog/` with `ui-dev` or `compose-up LOCAL_UI=on` for an external override. Explicit Make arguments override inherited environment and workflow env-file settings. Restart or recreate the UI after a change. The host reads JSON configuration, not environment variables.

`ui-dev` builds and serves the actual client through its Go authentication host, using `http://127.0.0.1:8080` as the default backend. Stop the UI launcher before rebuilding. Restart it after source changes. A rebuild replaces hashed assets, but a running host retains its old entry page in memory. Hot module replacement is not implemented. Stopping the UI does not stop or reset the backend.

For a separately started local API or a subpath test, run these commands from the repository root:

```sh
make ui-build
node catalog-ui/scripts/dev.mjs --api-url http://127.0.0.1:28183 --port 14180 --base-path /catalog-ui
```

This example serves `http://127.0.0.1:14180/catalog-ui/catalog` and requires the existing backend on port 28183. The launcher accepts only literal-loopback HTTP API origins and UI ports 1024 through 65535; it does not accept `localhost` as an API URL. Use the configured UI hostname and port exactly in the browser. The standalone Vite development server does not supply login/session or API forwarding; `make ui-dev` is the complete local workflow.

Only point the local launcher at an owned development Catalog using the matching synthetic identities. Ports must be free. The frontend bundle contains no bearer token or internal API address; environment-specific settings are loaded by the Go host.

## Build and verification

| Target                      | Purpose                                                                                                                                      |
| --------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------- |
| `make ui-install`           | Install the pinned npm lockfile without lifecycle scripts.                                                                                   |
| `make ui-deps-check`        | Scan the full lockfile with installed Trivy, including build/test dependencies; retain reports and fail on HIGH/CRITICAL findings.           |
| `make ui-deps-audit`        | Audit the full lockfile through npm's configured registry; retain reports and fail on high/critical findings.                                |
| `make ui-build`             | Type-check and build browser assets plus the stripped static `bin/catalog-ui` host.                                                          |
| `make ui-container-build`   | Build the independent UI image; accepts `UI_IMAGE` and `CONTAINER_PLATFORM=linux/amd64` or `linux/arm64`.                                    |
| `make ui-lint`              | Run ESLint for TypeScript, React hooks and Node tooling, plus Prettier formatting checks; never edits files.                                 |
| `make ui-check`             | Run UI lint and both dead-code scopes, type-check client/browser tests and verify generated OpenAPI types.                                   |
| `make ui-dead-code-report`  | Display full application/test/tooling and production-browser unused files, exports and dependencies; tool/configuration failures still fail. |
| `make ui-dead-code-check`   | Enforce both Knip scopes; included once in `ui-check` and `test-ui`, separate from isolated units and backend checks.                        |
| `make ui-client-check`      | Generate into a temporary directory and compare against checked-in types.                                                                    |
| `make ui-test-unit`         | Run client coverage, isolated Node helpers and shuffled Go host unit tests; no listener or subprocess fixtures.                              |
| `make ui-test`              | Run client coverage, all Node unit/functional fixtures and shuffled Go host tests with `functional`.                                         |
| `make ui-coverage`          | Run frontend unit/component tests and write HTML, LCOV and JSON coverage under `output/coverage/catalog-ui/`.                                |
| `make ui-coverage-100`      | Diagnose the exact 100% frontend target; residual defensive/fallback branches remain uncovered.                                              |
| `make ui-dev`               | Build and launch the local client on loopback port 4180.                                                                                     |
| `make ui-browser-install`   | Download the pinned Playwright Chromium browser once.                                                                                        |
| `make test-ui`              | Run the complete local UI gate: static checks, client/fixture and Go host tests, an isolated build and real-browser regressions.             |
| `make ui-container-inspect` | Inspect an existing `UI_IMAGE`: static host, runtime settings, licenses and frontend build hashes.                                           |
| `make ui-sbom`              | Generate an image-bound SPDX inventory of host Go and locked production JavaScript dependencies.                                             |
| `make test-ui-compose`      | Build owned images and check root/subpath HTTPS browser access against disposable PostgreSQL.                                                |
| `make test-ui-oidc`         | Check real human OIDC code/PKCE login, permissions and saved-work re-entry with the pinned external test provider.                           |
| `make test-minikube-ui`     | Build and qualify the optional Helm UI, subpath and shared sessions across two replicas in an owned disposable cluster.                      |

For the complete local UI check, run these commands from the repository root:

```sh
make ui-install          # Initial setup, and after lockfile changes
make ui-browser-install  # Initial setup, and after Playwright upgrades
make test-ui
```

`test-ui` runs the static and unit checks before starting the browser suite and exits nonzero if any stage fails. Dependencies and Chromium are installed explicitly, not during testing. Use `ui-check` or `ui-test` for faster iteration without launching a browser.

`ui-lint` checks maintained application and test TS/TSX, Node scripts and tool configuration with JavaScript/TypeScript rules, React hook checks, typed promise checks and selected Playwright/Testing Library rules. Warnings fail the gate. Generated OpenAPI declarations, build output and npm dependencies are excluded; no application screen is excluded.

Prettier covers CSS and formatting. ESLint does not replace runtime validation, rendered accessibility checks or dependency scanning. The lint target does not download tools, apply fixes or write a cache.

Run `make ui-install` after lockfile changes; the [test strategy](tests/README.md#targeted-lint-and-accessibility-checks) records exact rule scope and limitations.

Frontend coverage uses Vitest's version-matched V8 provider. `ui-test` and `test-ui` enforce current whole-client regression floors and 100% per-file coverage for completed modules; `ui-coverage-100` separately checks 100% lines, statements, functions and branches across all application TS/TSX. Only test files and declaration-only types are excluded. The [unit coverage guide](tests/README.md#unit-and-component-coverage) records the measured baseline, remaining work and report interpretation. Go host and Node fixture tests are separate from the frontend percentage. `ui-check` type-checks tests; production builds use `tsconfig.build.json` to type-check application code without depending on unit fixtures.

After an OpenAPI change, run `npm run generate` in this directory and review the generated diff. Do not edit `src/api/schema.d.ts` by hand. Node checks are explicit, separate from backend-only builds. Ordinary `make check` and `make verify` include host Go code; they do not install npm packages or build the frontend. Browser output and dependencies are ignored. No publishing is configured.

### Repeatable browser tests

The [UI test strategy and workflow matrix](tests/README.md) maps local journeys and packaged workflows to their assertions and test boundaries. A classified API operation or successful page load is not complete workflow coverage. The main suite runs Chromium against isolated processes; `make test-ui-compose` and `make test-ui-oidc` add the packaged checks below.

Use the separate `make ui-performance` target for production asset/work budgets and report-only browser timings. `UI_PERFORMANCE_TIMEOUT_SCALE` defaults to `3` and accepts integers `1` through `10`; `make ui-performance UI_PERFORMANCE_TIMEOUT_SCALE=6` doubles the default execution deadlines for slower runners. See [production performance measurements](tests/README.md#production-performance-measurements) for the fixture, reports and limits.

After `make ui-install` and `make ui-browser-install`, run `make test-ui`. The POSIX launcher builds the three Go binaries and browser assets in a private temporary directory, leaving `bin/`, `catalog-ui/dist`, running development hosts and their data untouched. Each test gets a fresh in-memory Catalog, its own five-item demo, random loopback ports, temporary session keys and a new browser context.

Ownership checks run before seeding or browser login. There is no existing-server URL or database override. Processes and temporary files are cleaned on success, failure or a handled interruption; failure screenshots and test diagnostics remain under ignored `output/playwright/ui-tests/run-*`.

Do not kill the launcher with SIGKILL if cleanup is needed.

The checked-in Chromium journeys cover discovery, Agent/MCP/Skill authoring/history, review/publication, publisher/domain administration, unused-resource deletion, scoped/task-only access, real self-context pagination, live membership suspension, Reader-grant revocation, pending cross-identity search cancellation and browser-timer expiry/re-entry. Audit tests check explicit filtering and exact export bytes. Search tests cover submitted text/visibility/reset, real Previous-page rereads, stale-cursor recovery, preview copy/download without repeating Search or detail loading, browser history without query URLs, missing bookmarks and system-theme changes.

Preview dismissal preserves the current query, filters, layout and focus; same-tab local/indexed detail/back navigation preserves memory-only criteria, cursor, draft text, layout and scroll and rereads results, while reloads and direct URLs start fresh. Skill upload tests use an owned synthetic tarball, reject an empty file and verify persisted inspection/handoff metadata; GNU tar is required for that fixture. Plugin tests cover default-policy private-download rejection, a selected real prepared-archive Resource/version/listing/review/publication/Reader workflow and separately response-fixtured retained-document rendering.

The selected fixture reuses existing development-only demo archive admission and invocation-owned TLS/allowlist files at a dynamic loopback origin; Python 3 standard-library ZIP inspection independently checks the archived documents. Retained read/reload and Reader detail succeed after publication stops the owned archive listener. Backend tests retain the archive-security matrix; the browser case does not qualify PostgreSQL durability or production public delivery.

Mutations use the real API. Other response interception covers deliberate failed reads, lost-response replay, stale edits and exact export comparison. The network fixture fails unexpected HTTP/transport failures and unused expected-failure declarations.

Tests are independent, retries are disabled and assertions wait for observable results rather than fixed sleeps. Repeat or focus a case with `npm run test:browser -- --repeat-each=2` or `npm run test:browser -- --grep 'independent review'`. Traces, videos and saved sessions are disabled.

This process suite does not qualify PostgreSQL or a human OIDC provider.

Rendered accessibility checks run in the normal suite. Use `make ui-visual` separately for the reviewed screenshot baselines, or `make ui-visual-update` to deliberately regenerate them. Missing or changed baselines fail normal comparisons. Review every changed image before accepting it; use the documented [reference environment and workflow](tests/browser/snapshots/README.md). Neither command rebuilds a running development UI's assets.

### Existing UI image inspection

After `make ui-container-build`, use `make ui-container-inspect UI_IMAGE=open-agentic-catalog-ui:dev` and `make ui-sbom UI_IMAGE=open-agentic-catalog-ui:dev`. Supply the image's `VERSION`, `COMMIT` and `CONTAINER_PLATFORM` when they differ from the defaults. These commands inspect the selected local image without rebuilding, running it or publishing it. SPDX output goes to `ARTIFACT_OUTPUT`; existing files are not overwritten.

The image retains `/licenses/frontend/inventory.json` from its frontend build: locked production packages, versions, declared licenses, notice paths, the lockfile hash and hashes of all React assets. The separate static manual lives under `/app/assets/docs/` and is served publicly at `/docs/`, independently of the UI base path. Its build evidence and notices live under `/licenses/documentation/`. Inspection requires both inventories and verifies every file. The SBOM combines the host's Go dependencies, frontend production packages and reviewed manual browser suppliers. These conservative package inventories are not exact module lists after bundling. Node, build/test tools and the external OIDC test provider are not runtime dependencies. See [build and release](../documentation/src/content/docs/contributing/build-and-release.md#optional-ui-image).

### Packaged browser checks

Run `make test-ui-compose` for image/PostgreSQL browser workflows, including source lifecycle, adoption, Jobs, signed webhook delivery and independent publication and `make test-ui-oidc` for human authorization-code/PKCE login. Both require the locked npm dependencies, installed Chromium, Docker Compose and OpenSSL. They build private local image tags and create a disposable Compose project; neither publishes images or connects to an existing deployment.

Root and `/browser` hosting run through a temporary HTTPS proxy. API/UI issuer trust and Chromium's certificate exception are restricted to the generated fixture certificate. Containers, volumes, private tags and temporary credentials are cleaned after the run; a cleanup failure reports the exact owned project and retains its inputs for inspection.

Provider tests save no screenshots or authentication state. Compose failures can save synthetic-only screenshots under ignored `output/playwright/`; traces remain disabled.

The OIDC profile reuses the external Keycloak image pin in `test/functional/compose.oidc.yaml`, not a shipped dependency. Its synthetic confidential client requires S256 PKCE, disables password/implicit grants and requests separate UI identity and Catalog access-token audiences. Human access tokens require the provider's `basic` subject mapper; see the [Keycloak 25 migration notes](https://www.keycloak.org/docs/latest/upgrading/index.html#sub-claim-is-added-to-access-token-via-protocol-mapper). Successful sign-in still requires a Catalog membership. The gate checks denied membership, scope ceilings, provider outage and local sign-out/re-entry to a namespace saved through the UI; it does not test provider-wide logout or arbitrary provider configurations. The [test matrix](tests/README.md) identifies asserted workflows and lower-layer protocol tests.

Run packaged browser gates sequentially, without another test creating or removing Docker networks at the same time. Host Chromium reacts to interface changes with `ERR_NETWORK_CHANGED`, which can cancel an in-flight login POST. The harness completes its own container changes before launching each browser and closes the browser before changing authentication configuration or stopping the provider. The CLI login browser uses the same bounded host-network readiness check as the UI browsers. Unexpected transport errors fail the gate, even if Chromium subsequently recovers. Earlier shared-host runs reported ERR_NETWORK_CHANGED. Preserve failed and passing reports separately under ignored `output/`. No login retries or fixed settling sleeps hide these failures.

## Compose and Helm

Local `make compose-up` enables webhook management by default, independently of `LOCAL_UI`. Use `LOCAL_WEBHOOKS=off` to disable it; an explicit `CATALOG_WEBHOOKS_ENABLED` takes precedence. No receiver is registered automatically. The development backend uses its existing stable development-only key unless an explicit key is injected. Production and Helm retain their disabled defaults and production key requirements. See [Webhook operations](../documentation/src/content/docs/operations/webhooks.mdx).

The separate [Containerfile](Containerfile) builds browser assets with digest-pinned Node 22 and the host with the repository's digest-pinned Go toolchain. Its non-root scratch runtime contains the static host, assets and dependency notices under `/licenses`; no Node, shell, development users, credentials or database client are shipped. The host embeds public TLS roots. Build from the repository root with `make ui-container-build`; use `CONTAINER_PLATFORM=linux/arm64 UI_IMAGE=open-agentic-catalog-ui:arm64` for ARM64. No registry publication is performed.

Start the optional local UI with PostgreSQL-backed Compose:

```sh
make compose-up LOCAL_UI=on
make compose-ps
```

Open `http://127.0.0.1:4180`. If that port is occupied, use `make compose-up LOCAL_UI=on CATALOG_UI_PORT=14180` and open port 14180 instead. `LOCAL_MONITORING=on` can be combined with `LOCAL_UI=on`. Startup builds both images; no host npm installation is needed. The UI waits for API readiness, binds only to loopback and uses the synthetic identities in `config/postgres.json`. It does not seed data automatically; use the [receipt-bound demo guide](../documentation/src/content/docs/guides/sample-catalog.mdx) with the explicitly configured local Plugin fixture when examples are needed.

The Compose launcher generates non-secret configuration, a development identity file and random session keys under ignored `output/ui-compose/<project>/inputs`. The parent directory is private; mounted files are readable by the non-root container. Keys survive ordinary restarts.

Stop/down/ps/logs include an existing UI even without `LOCAL_UI=on`. Re-running `compose-up` on a UI-enabled project requires the flag, so it cannot silently leave a stale UI running. To disable it, run `make compose-down` and restart without the flag; PostgreSQL volumes and local UI keys are retained.

`compose-reset` deletes database volumes, so it is not a UI toggle.

For Kubernetes, enable `ui.enabled=true` in the existing [application Helm chart](../deployments/helm/open-agentic-catalog/README.md#optional-catalog-ui). Supply the UI image, exact public origin, OIDC issuer/client ID and an existing Secret with `keys.json` and `client-secret`. The chart defaults to OIDC; enabling it never creates test users.

An explicit loopback-only development override is available for local port-forward tests. Optional ingress preserves the configured base path without rewriting it. The UI receives neither database credentials nor backend configuration, needs no PVC, and uses its own tokenless ServiceAccount and health probes.

API/issuer outages do not fail liveness.

Replicas share the same session keys, origin, base path, upstream and cookie name; no sticky sessions are required. Key files and trust are read at startup: follow the [key rotation instructions](#host-configuration) and roll out all UI Pods after changing an existing Secret or CA ConfigMap. Config changes rendered by Helm trigger a checksum rollout. UI network policy is optional; when enabled, configure ingress peers and issuer egress for your environment. Kubernetes NetworkPolicy does not select DNS names. See the chart guide for details.

## Host configuration

Start the direct host with `bin/catalog-ui --config /path/to/ui.json`. The required strict JSON file is independent of backend configuration and environment overrides. Local launchers and Helm generate it from their own inputs.

The [UI host configuration reference](../documentation/src/content/docs/reference/ui-host-configuration.md) owns all fields, defaults, bounds, mounted files, asset checks and restart behavior. Use its [configuration fields](../documentation/src/content/docs/reference/ui-host-configuration.md#configuration-fields) and [file inputs](../documentation/src/content/docs/reference/ui-host-configuration.md#file-inputs) when preparing an operator-managed host.

## Authentication and forwarding

See [authentication and forwarding](../documentation/src/content/docs/reference/ui-host-configuration.md#authentication-and-forwarding) for OIDC clients, session cookies, fixed API transport and browser request protections. Follow [sign-in setup](../documentation/src/content/docs/operations/sign-in.md) for provider users and Catalog access. The [restart and rotation reference](../documentation/src/content/docs/reference/ui-host-configuration.md#restart-and-rotation) covers mounted-input replacement, cookie-key rollout and listener drain.

## Layout and dependencies

Desktop navigation uses compact 32-pixel rows, reduced to 28 pixels on short windows, and small section gaps independently of content density. Short windows retain a scrollable navigation area so every destination stays reachable; mobile navigation keeps larger controls and visible group labels behind the workspace menu.

Read-only documents use `src/views/CodeBlock.tsx` and `code-document.ts` for theme-aware JSON highlighting, optional indentation and line wrapping. Raw evidence starts with its original whitespace. Formatting changes whitespace only, preserving duplicate keys, number literals and escapes; it never changes stored data or downloads. Highlighting is bounded to 131,072 JavaScript string code units, 6,000 tokens and 40 nested levels, with a separate whitespace-expansion ceiling; documents exceeding these limits and non-JSON documents remain selectable plain text. No editor or highlighting dependency is needed.

- [API boundary](src/api/README.md): generated OpenAPI types, session-bound transport, command retry rules, permission hints and the operation-to-workflow inventory.
- [Session and navigation shell](src/shell/README.md): session lifecycle, route selection, navigation, preferences, error recovery and shared icons.
- [Workflow screens](src/views/README.md): workflow screens and shared controls. `workspace.tsx` owns `WorkspaceProps`, explicit reads, confirmation and unsaved-input handling; `authoring.tsx` builds forms and bounded selectors on those controls. Screens own their typed API queries and commands.
- `src/main.tsx`: runtime configuration loading and application bootstrap.
- [UI host boundary](internal/web/README.md): strict configuration, sessions, OIDC, fixed forwarding and static serving.
- [UI process entry point](cmd/catalog-ui/README.md): listener and process shutdown.
- [Build and test tooling](scripts/README.md): reproducible type checking and the local-only launcher.
- [Browser journeys](tests/browser/README.md): Playwright workflows and fresh-service fixtures.
- [Component fixtures](tests/unit/README.md): shared unit-test fixtures; unit/component tests stay beside the source they exercise.

Use the shared screen context instead of copying client/access/route props. Read keys must include inputs that change the query; callback identity alone does not trigger a reload. Prepare commands by capturing the target, body and ETag before confirmation; retries reuse that captured input and the original idempotency key. Permission helpers control UI hints, while the API authorizes every request. Keep workflow-specific payloads and validation in their owning screen rather than adding a generic CRUD framework. Comments should explain these contracts and non-obvious behavior, not narrate JSX or repeat function names.

Federation screens are split by workflow: `Federation.tsx` handles inspection, connection and preview-bound source changes; `SourceSnapshots.tsx` handles synchronization history; `Observations.tsx` handles provenance, explicit raw evidence and adoption; `FederationSearch.tsx` handles indexed and live diagnostics. `SourceTrust.tsx` handles private assessment requests from indexed and live selections. `trust-selection.ts` validates their consumed binding fields without reconstructing entry digests. `federation-inputs.ts` owns shared scope/topology/policy input validation. The shell imports routed screens directly, without a re-export module that loads all federation workflows together. Snapshot detail, raw evidence and adoption remain explicit actions, not list prefetches. Keep related detail/editor components beside their workflow and extract shared code only when it has real callers.

`src/styles.css` owns the light/dark semantic tokens, shared button/link sizing and responsive shell, kind selectors, kind-accented cards, tables, access panels and preview styles. `src/views/workspace.css` owns form fields, separated action rows and admin panels; compact density reduces panel and table spacing without shrinking form controls. Use `form-actions` for form submissions and `workspace-toolbar` for grouped navigation or list actions.

Use `--control-border` for interactive fields and `--line` for panel dividers. Preserve density tokens at responsive breakpoints. Long metadata must wrap inside its container without truncating copied values or changing code whitespace. Native checkboxes need explicit dimensions and a usable label hit area. ValueList actions stay adjacent to their fields; help retains its label relationship. Pressed buttons show an inset selection marker in forced-colors mode, separately from the outer keyboard focus ring. Browser geometry and visual comparisons verify these rules; statement coverage does not measure CSS.

ValueList stacks from its own available width and font size, including enlarged text beside the desktop sidebar. Header controls can wrap. The shell measures the header and sets its workspace offset so sticky navigation cannot hide a focused link beneath it. The observer disconnects when that workspace leaves the screen.

Do not add separate sizing or colors for links styled as buttons. `src/views/SearchFilters.tsx` and `search-filters.css` own the collapsible desktop filter panel, native modal drawer at widths up to 900px and search-first toolbar; `search-filters.ts` contains tested query updates and tag bounds. One draft survives resizing; Apply submits it, while dismissal discards unapplied changes.

Suggestions use the current bounded results page without extra requests. `src/shell/Icon.tsx` contains the small inline SVG set; icons are decorative alongside text or accessible control labels. Appearance uses native `details` and radio groups with visual theme previews and arrow-key selection; the modal preview uses native `dialog` and scrolls metadata separately from its close and full-record controls.

No component library or external font is required.

The [UI runtime and test dependency inventory](../documentation/src/content/docs/reference/dependencies.md#ui-runtime-and-test-inputs) owns package roles and licenses. Follow [UI dependency maintenance](../documentation/src/content/docs/reference/dependencies.md#ui-dependency-maintenance) for scans, lock updates and qualification.

## Private feedback

Local item and Resource pages offer an on-demand **Item feedback** panel for humans with `feedback.own`; new submissions also require access to that resource. **Account > My feedback** keeps existing submissions available for editing or withdrawal after resource-read revocation. The independent **Feedback inbox** requires `feedback.moderate`; named resource choices and context links require their own read permissions. An optional exact resource-ID filter works without resource reads. With OIDC scope enforcement, own reads need `catalog.read`, own writes need `feedback.write`, and moderation needs `tenant.admin`. These workflows reuse the existing private API, authentication host and command confirmation. See the [feedback guide](../documentation/src/content/docs/guides/feedback.mdx) for lifecycle and privacy details.

Catalog cards lead with name/description and use small bundled kind icons, matching kind colors, text-labelled origin/lifecycle badges and compact Namespace filters. Cards and Table share Name, Kind and Publisher sorting across server pages; Table separates Kind from Status and exposes keyboard-accessible sortable headings. Explicit ordering uses keyword matches, with local and synced results before a separately sorted live window. Default relevance retains optional meaning-based search. Saved searches preserve the selected order. Local namespace/license and visibility remain distinct from external metadata; tag and namespace buttons stay above the stretched title-button preview target. Long values wrap, and descriptions are visually clamped with full text retained in previews. No artwork requests or additional metadata reads are introduced.

Catalog previews and local/indexed details group content into Overview, Use this resource, and Technical details and evidence. Documentation and safe Open/Copy references accompany use guidance; authentication, external provenance and conflict warnings remain visible. Representation checksums/profiles and exact release/revision IDs use native disclosure controls. Opening evidence does not fetch documents; retained downloads still require an explicit action and checksum verification.

## Public Help and quick navigation

After runtime settings validate, signed-out users can open About, Getting started, Troubleshooting and the host-approved Documentation destination. About serves the project license and exact browser dependency notices under the configured UI base path at `legal/LICENSE.txt` and `legal/third-party-notices.txt`. These files ship with every browser build and work without the manual. Authenticated About adds API/UI host build observations and Service information; the public page makes no build or product read.

Signed-in users can choose Quick navigation or press Ctrl+K/Command+K outside an editor or dialog. The chooser matches permitted page names from the shared navigation owner. It uses normal links and preserves native unsaved-input protection. The session warns two minutes before absolute expiry without extending it or saving drafts. See the [UI guide](../documentation/src/content/docs/guides/catalog-ui.mdx#start-and-navigate).

Plugin authoring offers **Prepare Plugin files** and **Use an existing archive**. Fields generate a manifest, optional Skills and named MCP configuration. Authors download individual files, package them and supply an HTTPS archive. The ZIP/gzipped-tar selector sets the media type. Preparation, format changes and URL edits preserve any existing checksum. Advanced JSON and Markdown remain available. See [Plugin file preparation](../documentation/src/content/docs/guides/resource-authoring.mdx#prepare-plugin-files-from-fields).

Authoring puts document entry before **Advanced representation settings**. A pending or ready local file must be applied or discarded before saving. Unsealed Version editors can explicitly stage the complete current draft; incomplete reads and unsupported lossless reconstruction leave input unchanged. Reused delivery URLs are fetched again, Plugin checksums remain pinned and Skill archives require the complete original package and handoff.

Version and review views show readable exact validation findings and immutable reviewer decisions. **Prepare listing metadata** opens the Resource's one Listing workflow. Resource/Listing lifecycle inspection selects an exact Version and Listing revision, keeps history bounded and treats discovery as an explicit host/audience observation. Version, Listing and review comparisons use immutable IDs, require separate content permission and load exact documents only on request. See [resource authoring](../documentation/src/content/docs/guides/resource-authoring.mdx#author-in-the-ui) for permissions and fidelity limits.

**Help > Troubleshooting** is available before sign-in. Its authenticated diagnostic preview is explicit, with local clipboard copying of bounded build facts and safe response correlation only. See the [consumer procedure](../documentation/src/content/docs/guides/catalog-ui.mdx#troubleshooting). Job outcomes, delivery history, publication membership and applied investigation criteria have readable summaries before complete evidence. Exact raw text can be copied only in opted-in retained-text viewers; decoded metadata remains presentation only.

## Personal work and saved items

**Account > My work** reads bounded caller-owned draft and submission queues. **Account > Saved items and searches** manages the caller's server-owned library. `Personal.tsx` owns these screens and explicit save forms. Catalog record/preview controls save exact references. Named search controls capture submitted criteria; opening a saved search stages them without Search until fresh consent and explicit submission. No private query enters URL/history or a browser persistence cache.

Named searches use shared native fields and one guided/advanced JSON draft. Opening, editing and saving make no Search/Explore request. Quick resource save shows a read-only captured reference; saved detail retains deliberate advanced replacement. Library rows summarize retained facts without per-target reads, with explicit type/exact-label Apply/Clear and owner-bound continuation. Current reads preserve last-loaded data and drafts after failure; new saves/removals require successful read readiness. Confirmations capture item ID, validator, label and exact criteria/reference; uncertain commands retain their original input/key. Non-live rating refresh preserves score/consent drafts; My ratings and My work retain labelled pages during refresh. Local records link to independently authorized private feedback.

Library reads require human `library.own` and `catalog.read` when scope enforcement is enabled. Mutations also require `personal.write`, idempotency and current ETags. Configure this optional scope in the issuer and UI `scopes`, then sign in again. The [UI guide](../documentation/src/content/docs/guides/catalog-ui.mdx#my-work) owns queue semantics, attribution limits, quotas and exact-reference availability. Unit, HTTP/PostgreSQL, native browser and OIDC tests retain separate evidence.

Development sign-in groups the configured workflow bundles and exact templates using explicit host presentation, with search, counts and elevated/separate-tenant captions. My access explains read-only versus writing tasks, baseline human ownership, verified template help and current self group captions without granting administration. The access guide owns the separate active-snapshot path for published-only consumers; the web Catalog keeps its Control API contract.

Webhook inventory supports exact name/status filters, receiver/subscription summaries and exact-ID navigation. Delivery evidence has intersecting exact delivery/event/state filters, explicit first-page recovery and independent endpoint drafts. Test receipts show readable non-secret outcomes; only `WEBHOOK_TEST_BUSY` at HTTP 503 establishes a test-slot no-send refusal and permits deliberate fresh-key preparation. Other uncertain outcomes retain exact-key recovery.

Current local published/deprecated MCP records offer explicit client configuration preview and download for VS Code, Claude Code and Cursor. Select one supported HTTPS remote connection. The browser verifies stored documents on demand, omits header values and never contacts the selected server. See [client configuration setup](../documentation/src/content/docs/guides/understand-results.mdx#download-an-mcp-client-configuration).
