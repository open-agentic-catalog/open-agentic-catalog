# Catalog UI screenshots

These images illustrate controls with synthetic identities and disposable local fixtures. Keep retained Compose data, credentials, session material and private customer records out of captures. Discovery does not imply execution.

Use the table to find each image's capture workflow. Browser cases are under `catalog-ui/tests/browser/`; the local-password journey is under `catalog-ui/scripts/`. Raw captures and hash manifests stay under ignored `output/`. An earlier capture or review does not establish current qualification.

## Screenshot sources

| Image | Existing capture workflow | What it illustrates |
| --- | --- | --- |
| `jobs-filtered.png` | `jobs.spec.ts`, root exact-kind/state/parent selection before cursor recovery. | Applied filters, a synthetic failed child, reported progress and the separate exact-ID opener. |
| `source-confirmation.png` | `source-configuration.spec.ts`, root expiry/reinspection journey before cancellation. | The image shows the exact inspection, captured configuration, network and credential choices, and reason. The test sends no connection request during capture. |
| `search-indexing-confirmation.png` | `indexing-guidance.spec.ts`, root explicit active-snapshot selection before cancellation. | Captured public population, exact Snapshot ID, observed host/audience and reason. |
| `webhook-rotation.png` | `webhooks.spec.ts`, read-only/rotation-only delegate journey after secret clearing. | Non-secret observed signing version and overlap deadline, separate from last-loaded endpoint metadata. |
| `my-work.png` | `personal.spec.ts`, root personal workflow. | Contributor-owned empty Resource, unsealed Versions and a current Listing candidate with exact continuation links. |
| `saved-search.png` | The same root personal workflow, after confirmed save and reload. | Last-loaded context, private label, guided text/optional filters, advanced JSON, update/remove controls and explicit Search staging. |
| `troubleshooting.png` | `troubleshooting.spec.ts`, root guidance after an explicit preview/copy/close and reload. | Public recovery guidance, signed-in internal links and the collapsed diagnostic preview. No diagnostic payload enters the image. |
| `publication-contents.png` | `publication-contents.spec.ts`, authenticated retained candidate. | Exact audience, separate ARD Registry/resource and AI Catalog counts, and a tenant release omitted only from ARD for missing queries. |
| `about.png` | `help-session.spec.ts`, public root route. | Product boundaries, Apache License 2.0, one Author line and exact browser notices without authenticated build data. |
| `getting-started.png` | The same public Help journey. | Workflow orientation before sign-in, with permissions beside each task. |
| `document-first-authoring.png` | `authoring-continuation.spec.ts`, root Agent journey before Listing details. | The tutorial Agent fields, including JSON input, Text output and an order-status ability. |
| `authoring-protocol-choices.png` | `authoring-layout.spec.ts`, root guided choices. | Custom binding, an A2A version preset and preferred-interface movement. |
| `authoring-file-stages.png` | The same case, staged JSON file. | The actual chosen filename and separate apply/discard controls before application. |
| `authoring-mobile-preview.png` | The same case, 375-pixel preview. | Current document title, filename, exact download and return control. |
| `authoring-basics.png` | `guided-authoring.spec.ts`, root Agent journey before Content. | Namespace selection, permanent identity and the expanded AIR builder before any write. |
| `authoring-required-fields.png` | `authoring-validation.spec.ts`, root Agent journey after Listing preflight. | Missing required name and description, with inline errors and a highlighted border. No save occurs. |
| `authoring-saved-draft.png` | `authoring-continuation.spec.ts`, root Agent journey after independent API readback. | Complete content validation, a saved Listing and separate inspection/correction links. The records remain drafts. |
| `authoring-new-version.png` | The same file, sealed-source journey. | Exact source revision, a new version label and populated Agent fields before saving another Version. |
| `mcp-field-authoring.png` | `authoring-continuation.spec.ts`, root MCP Server journey before Listing details. | The tutorial hosted Server Card fields and Streamable HTTP endpoint. |
| `skill-field-authoring.png` | `authoring-continuation.spec.ts`, root Skill journey before Listing details. | The tutorial Skill name, description and Markdown instructions. |
| `plugin-field-authoring.png` | `authored-plugin-package.spec.ts`, before downloads. | The tutorial manifest, Skill and MCP fields before actual download, packaging and HTTPS submission. |
| `guided-listing-details.png` | `authoring-continuation.spec.ts`, root Agent journey. | Optional Listing metadata before the final content save. The shared checkbox stays beside its label. |
| `guided-review-approved.png` | `guided-publication.spec.ts`, after explicit current readback. | The independent Reviewer's approved corrected submission, exact validation and separate Publisher authority. |
| `version-comparison.png` | `authoring-guidance.spec.ts`, complete current-draft reuse and immutable comparison journey. | Representation changes between exact Version revisions and an explicit unavailable lossless-document comparison. |
| `listing-lifecycle.png` | The same file, subpath lifecycle journey. | Exact selected Version/Listing revisions, approved review, published release and a separate not-inspected discovery observation. |
| `catalog-browse.png` | The curated layout case in `catalog.spec.ts`. | Reader Cards view, twelve curated releases, four resource-kind selectors, empty rating labels and search-ranking disclosure. |
| `catalog-table-sorted.png` | The column-ordering case in `catalog.spec.ts`, after selecting ascending Kind. | Curated Table results, sortable headings, separate Kind/Status badges and compact Namespace filters. |
| `catalog-comparison.png` | Root comparison case in `comparison.spec.ts`. | Two local summaries from one loaded page, exact references, missing interface/authentication assessment and separate rating facts. |
| `catalog-record.png` | Root section/bookmark/category journey in `record-reader.spec.ts`. | Independent My private feedback link, local overview, native section links, canonical identifier and complete category filters. Lower sections continue below the viewport. |
| `help-documentation.png` | `documentation.spec.ts`, controlled bootstrap/navigation case. | Documentation above API reference, with keyboard focus. Packaged gates separately qualify real manual delivery. |
| `catalog-filters.png` | The same curated layout case in `catalog.spec.ts`. | Positive local category counts, category input and Apply. Lower controls require scrolling. |
| `access-roles.png` | `visual.spec.ts`, reviewed `access-roles-light.png` reference. | Built-in definitions and a disposable custom metadata-reader role, with separate name, state and scope. |
| `my-access-reader.png` | `access-selection.spec.ts`, exact Reader. | Read-only Authoring/Reviews, baseline human tasks and verified built-in template help, without administration access. |
| `resource-ratings.png` | `ratings.spec.ts`, human rating journey. | Explicit refresh, last-loaded exact target, numeric summary, private sharing choice and owner controls for a synthetic Agent. |
| `feedback-case.png` | `moderation-cases.spec.ts`, root feedback case after confirmed response. | Exact references, updated time, synthetic response, current readback, same-tab list return and separate replay acknowledgement with audit handoff. |
| `artwork-gallery.png` | `artwork.spec.ts`, owned TLS artwork case, `artwork-gallery-real-narrow.png`. | Optional icon and screenshot, alternative text, caption and declared rights in a narrow preview. The blue/gold artwork is an owned synthetic fixture. |
| `authoring-exact-identifier.png` | `findability.spec.ts`, root Authoring case. | One exact identifier found beyond the initial page, combined with kind. |
| `overview-content.png` | `visual.spec.ts`, controlled Content summaries at desktop width. | Four Resource kinds, current releases, directories, distinct indexed records, reviews and publication configuration. Values are synthetic. |
| `overview-administration.png` | `visual.spec.ts`, controlled Administration summaries at desktop width. | One membership state table for People and Service identities, active access records, feedback categories and rating states. Values are synthetic. |
| `overview-webhooks.png` | `visual.spec.ts`, controlled enabled-webhook summary at desktop width. | Endpoint states, retained production deliveries, latest retained failure and separate public notification counters. Values are synthetic. |
| `overview-operations.png` | `visual.spec.ts`, controlled Source and Job summaries at desktop width. | Source status, current-revision sync evidence, Job state table and existing index status. Counts are synthetic. |
| `overview-review-activity.png` | `visual.spec.ts`, controlled review summary at desktop width. | Pending age, elapsed median and daily submission/decision bars. Values are synthetic; database qualification is separate. |
| `review-queue-context.png` | `findability.spec.ts`, root review case. | Resource identifier and exact submitted listing name after a later candidate rename. |
| `catalog-preview.png` | Explicit consumer-handoff capture in `visual.spec.ts`. | Agent Overview, missing categories, independent validation/assessments, one save entry and full-record navigation. Handoff continues below. |
| `development-sign-in.png` | `access-selection.spec.ts`. | Naturally wrapped title and introduction above development bundles; exact templates are below in the scrollable identity roster. |
| `overview-service-activity.png` | `activity.spec.ts`, root enabled scenario. | Real process counters, exact manual-refresh delta, bounded breakdowns and separate HTTP/Search/MCP populations. Values depend on the owned fixture. |
| `catalog-api-areas.png` | `reference.spec.ts`, `api-areas-overview.png`. | Declared area sections and counts, responsive jump index, combined local filters and the compact connected-contract header. |
| `catalog-api-reference.png` | `reference.spec.ts`, `api-groups-reference.png`. | Groups and members section with one expanded membership operation and readable authentication, inherited parameters and response headers before complete data. Browsing does not call its endpoint. |
| `publisher-dns-verification.png` | `publisher-verification.spec.ts`, optional verification journey after issuance. | Separate approval, proof, pending challenge and last-check states before management instructions are requested. No TXT value is pictured. |
| `catalog-api-definitions.png` | `reference-definitions.spec.ts`, `api-definition-workspace.png`. | Independent name/description search, component-kind filtering, local pages and declared Resource property facts beside complete-data access. |
| `tenant-settings.png` | `settings.spec.ts`, initial administrator view. | Inheritance, saved/default/effective comparison, bounded editing, explicit history read and independently authorized related controls. |
| `ans-assessment.png` | `trust-result.spec.ts`, private result journey. | Saved-result Refresh, optional check help and separate signature, inclusion, target and status results for owned synthetic ANS evidence. |
| `release-history.png` | `replacement.spec.ts`. | Published and superseded releases with exact references. |
| `release-details.png` | The same replacement case as the history image. | Superseded release identity, immutable revisions, replacement guidance and revocation eligibility. |
| `regular-sign-in.png` | `make test-local-auth`, `local-password-journey.mjs`, before provider login. | The regular Catalog sign-in screen, without development shortcuts. |
| `local-keycloak-sign-in.png` | The same local-password journey, after the Sign in redirect. | The local Keycloak username/password form, without entered credentials. |
| `draft-import-preview.png` | `draft-files.spec.ts`, root Agent import before applying the draft. | Parsed reusable draft input before any Resource write. |
| `draft-import-destination.png` | The same file, existing Resource import with imported Listing metadata selected. | New-Version destination and the explicit Listing metadata choice. |
| `mcp-client-configuration.png` | `mcp-client-export.spec.ts`, root client-configuration journey. | Synthetic remote metadata, omitted header names and inert Cursor configuration. No discovered endpoint is contacted. |

## Refresh and verify screenshots

Install the locked UI and Chromium prerequisites when needed: run `make ui-install`, then `make ui-browser-install`. Run `make test-ui` for the complete current functional suite. Require a passing invocation and successful owned cleanup before selecting its images.

For a focused refresh, run the existing launcher from `catalog-ui/`:

```sh
npm run test:browser -- authoring-guidance.spec.ts
npm run test:browser -- personal.spec.ts
npm run test:browser -- findability.spec.ts
npm run test:browser -- catalog.spec.ts --grep 'comfortable Catalog'
npm run test:browser -- reference.spec.ts replacement.spec.ts
npm run test:browser -- draft-files.spec.ts mcp-client-export.spec.ts
```

Run `make test-local-auth` from the repository root for the regular and local Keycloak sign-in images. This workflow owns its test identity provider and removes its fixtures.

Run `make ui-visual` for the separate visual references. Reuse its passing Roles reference and review its emitted Preview capture. Do not regenerate expected images merely to make a comparison pass. Manual illustrations and visual-test references have separate purposes.

Review each selected image before copying it. Use benign synthetic names and descriptions. Keep the sticky header clear of the pictured controls. Restore the normal viewport after a special capture. Preserve the populated curated Browse and Filters examples; do not replace them with empty fixtures.

Keep release-history and release-details images from the same successful invocation so their exact references agree. Keep controls and observations distinct: a displayed reference or historical assessment does not prove current authorization, availability or execution.

The [download allowlist](../../scripts/site.mjs) and relative image links control inclusion in root and subpath builds. Provide descriptive alt text, a caption and a full-size link. Keep required instructions in surrounding prose. Follow the [screenshot guidance](../../src/content/docs/contributing/documentation.mdx), then run `make check` and `make docs-site-check`. Run the owned Compose/OIDC gates when bundled manual assets change. These commands build and test locally; they do not publish the site.

### Capture requirements and limits

- Clear the one-time signing key before capturing `webhook-rotation.png`. Keep automatic webhook failure screenshots disabled. Use the root launcher: `node catalog-ui/scripts/browser-test.mjs webhooks.spec.ts --grep "read-only and rotation-only"`.
- Exclude private TXT values from the Publisher DNS panel and entered credentials from provider sign-in images.
- Keep the diagnostic payload out of `troubleshooting.png`. Capture its collapsed preview after the explicit preview/copy/close workflow.
- Capture the Source confirmation at 1440 by 2400 pixels to include its full summary. Cancel without connecting. The same journey separately checks narrow layouts with doubled text.
- Preserve unequal synthetic state counts in Overview figures. Operations and review charts use fixed fixture time; their images are separate from database and metric qualification.
- Treat the ANS panel as synthetic historical evidence. Its screenshot does not qualify cryptographic verification. The indexing picker reads an active snapshot from a disposable Catalog; its search-availability display is controlled for presentation.
- Preserve the independent Help destinations. Some authoring, publication and feedback fixtures use an external manual override labelled Documentation. Packaged Compose/OIDC tests qualify the bundled guide links separately.
- Treat API-reference images as captured contract views. Their operation counts can differ from the latest API. Browsing an operation does not call its endpoint.

### Current capture provenance

Authoring captures retain passing sources from `run-U699mD`, with MCP and Skill fields from `run-m5iUcy`. The approved-review image retains its earlier passing source. The campaign's separate Plugin save failure and passing publication repeat remain distinct; no selected image comes from the failed case.

The draft-import images and Authoring basics use `run-91XgdQ`. The MCP client configuration uses `run-DWDic9`. The latest sortable Jobs and Reviews images use `run-Fv6Z0V`; My work uses `run-V7S6Jt`. Publication contents uses `run-Z5M3CI`.

The Content, Administration, Webhooks, Operations and Review activity figures match `run-Q6s9uu`. The Service activity figure uses the separate `activity.spec.ts` workflow. The API-reference figures use `run-2W6hfx` and show its captured contract. Earlier broader API and capture campaigns include separate assignment or Search refusals; passing screenshots do not convert those campaigns into clean aggregate results.

Other illustrations retain their reviewed source captures. Preserve useful image bytes when only synthetic IDs, timestamps or hover states differ. These images do not establish cryptographic verification, durable storage, permission enforcement or narrow-layout qualification. The underlying reports remain under ignored review output.

## Workflow and diagnostic illustrations

These captures use synthetic identities and records. Keep adversarial inputs and raw evidence out of the selected regions.

| Image | Capture owner and point | Meaning |
| --- | --- | --- |
| `access-assignment-confirmation.png` | `access.spec.ts`, authorization journey before confirmation. | Prepared custom-role group grant for one namespace; no committed grant yet. |
| `member-access-contributions.png` | The same authorization journey, after contribution assertions. | Additive group namespace and direct Library Member paths on one observed page. |
| `audit-investigation.png` | `audit-investigation.spec.ts`, desktop criteria and first result. | Applied UTC search with raw JSON closed; export needs a past end time. |
| `webhook-delivery-history.png` | `delivery-outcomes.spec.ts`, separate benign documentation case. | Controlled delivery summaries, not a real receiver qualification. |
| `publication-snapshot-comparison.png` | `snapshot-comparison.spec.ts`, root comparison through the first changed description. | Captured retired/candidate references and differences, not current activeness. |
| `job-detail-recovery.png` | `jobs.spec.ts`, Running retry child after reload. | Reported 25% progress, 1 / 3 attempts and parent reference, not completion. |
| `service-information-processes.png` | `reference.spec.ts`, asserted API/UI identities and expanded build details. | Two process observations with support payloads closed. |
| `source-retained-occurrence.png` | `make test-ui-compose`, `packaged-journeys.mjs`, exact references before raw entry read. | Retained historical occurrence and adoption selector. |
| `source-adoption-preparation.png` | The same packaged invocation, guided local name before license stress input. | Private draft preparation before confirmation. |

Run the focused browser selection through the existing launcher:

```sh
node catalog-ui/scripts/browser-test.mjs access.spec.ts audit-investigation.spec.ts delivery-outcomes.spec.ts snapshot-comparison.spec.ts jobs.spec.ts reference.spec.ts
make test-ui-compose
```

The packaged owner writes both source images under `output/playwright/<project>/`. Require its final success and scoped cleanup before promotion. Capture these tall regions at a 1440-by-3000 viewport, then restore the original viewport. Scroll the region below the sticky header before capture. Retain both source images from the same invocation so their occurrence references agree.

The access images use passing `run-RaoXVo`; the first campaign's separate clock-regression failure remains recorded privately. The focused audit, Job and process images use `run-L2KVNq`. Delivery history uses `run-3vwNME`; snapshot comparison uses `run-x96RKn`. These presentation records do not replace real boundary tests.

The two source images use passing packaged run `catalog-ui-packaged-4cfr7l`. Their exact occurrence references agree, and scoped container, network, volume and private-input cleanup succeeded. The earlier `catalog-ui-packaged-9jmvgj` run hit the known assignment clock-regression guard; its failure remains separate.
