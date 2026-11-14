# IETF ARD draft-00 assessment

## Decision

Track the three reviewed individual Internet-Drafts. Continue to use `ard-0.91-aa3e598` for Catalog discovery and `ai-catalog-1.0-b062278` for separate AI Catalog compatibility. Do not select the drafts as a runtime profile or claim conformance to them.

The decision owner is the Catalog maintainers. The question is whether these exact drafts provide a useful, sufficiently specified replacement for the selected discovery contract. [Wellknown's documented v0.91 interface](https://wellknown.network/docs/ard) and [n1k0ver3E's reviewed v0.9 registry](https://github.com/n1k0ver3E/ard-registry/tree/0ba2b4bd18870202e741eb1ce9e3e74a3cdd7b40) do not establish a requirement for that replacement. Their native compatibility remains a separate qualification question.

This assessment closes the standards decision for these revisions. It does not close independent peer qualification or select a later implementation.

## Reviewed authorities

| Input | Exact revision and status | Authority in this assessment |
| --- | --- | --- |
| Catalog ARD profile | [`ard-0.91-aa3e598`](../../schemas/PROFILES.md#active-profiles), commit `aa3e598bb7752a9175897823234311216acfa864` | Selected discovery contract. Its retained CDDL gives the embedded JSON Schema precedence where they disagree. |
| Catalog AI Catalog profile | `ai-catalog-1.0-b062278`, commit `b062278fd7f5a83202d33208f1d07c5b5eb7ccb3` | Separate compatibility contract, not an automatic interpretation of the older well-known filename. |
| ARD upstream main | [Commit `f073b1e`](https://github.com/ards-project/ard-spec/tree/f073b1ea9b01a5f068d0b0986426f2ea49fa30b2) | Reviewed upstream comparison. This commit is neither Catalog's selected profile nor the IETF draft contract. |
| Discovery | [draft-bu-dawn-ard-discovery-00](https://datatracker.ietf.org/doc/html/draft-bu-dawn-ard-discovery-00), intended Standards Track | Candidate manifest, entry and Registry contract. |
| Initial Contact | [draft-jimenez-dawn-ard-initial-contact-00](https://datatracker.ietf.org/doc/html/draft-jimenez-dawn-ard-initial-contact-00), intended Standards Track | Candidate entry-point discovery contract. |
| Architecture | [draft-bu-dawn-ard-architecture-00](https://datatracker.ietf.org/doc/html/draft-bu-dawn-ard-architecture-00), intended Informational | Architectural context. It does not silently resolve conflicting discovery requirements. |

All three drafts were published on October 30, 2026 and expire on May 3, 2027. At review, Datatracker lists them as active Individual Submissions with I-D Exists status. They are work in progress, not RFCs. The code baseline is Catalog commit `76e6873f8657fc730eb4e909a164d8b2db03a3dc`. Later upstream changes require a new comparison against their exact revisions.

Compared with Catalog's pin, the reviewed ARD main clarifies optional namespace segments, common media types and Registry base versus referral endpoint URLs. It retains framework-defined Trust Manifest signing rather than selecting the drafts' concrete JWS/JCS scheme. Those upstream clarifications do not select either a new Catalog bundle or the IETF contract.

## Compatibility findings

The section references in this table refer to Discovery-00 unless another draft is named. MUST, SHOULD and optional behavior retain their specification meanings.

| Topic and requirement reference | Current implementation and compatibility impact | Disposition and owner |
| --- | --- | --- |
| Manifest media type, 3.6; Initial Contact 4.3.1 | Public ARD manifests and links advertise `application/json`. Ingestion already recognizes `application/ard+json`. Input recognition does not prove draft conformance. | Track the output/header difference. HTTP and federation maintainers own any selected change. |
| Search response continuation, 4.2.2.2 and 5 | Publication emits `pageToken`. The live Search decoder rejects unknown envelope members, including `nextPageToken`. Request tokens retain the `pageToken` name in the draft. | Confirmed Search wire difference. Any adoption must coordinate publication, peer decoding, OpenAPI and affected clients. Do not rename List tokens by inference. API/client and federation maintainers own it. |
| Errors, 4.2.5 | ARD uses `errorCode` and `message` in `application/json`. Control API errors already use RFC 9457. The draft's Problem Details guidance is SHOULD. | Track as a recommendation and client-compatibility choice, not an unconditional MUST or a selected-profile defect. HTTP maintainers own it. |
| Artifact digests, 3.2.3, 3.5.2 and 6.4 | Public entries have no dedicated artifact-digest field. The draft uses algorithm-prefixed artifact and evidence digests, with SHA-256 mandatory to implement. HTTP Content-Digest covers response bytes, not an entry's referenced artifact. | Keep artifact, attestation-document and transport digest subjects distinct. Publication and security maintainers must select exact bytes and binding rules before adoption. |
| Trust envelopes, 3.5, 5 and 6.2 | Validation checks structure and declared publisher alignment. Framework fields remain open. It does not verify publisher signatures. | Track the unresolved envelope and unsigned-entry requirements described below. Standards and security maintainers own the interpretation. |
| Trust field spelling, 3.5 and 5 | The selected schema names `TrustManifest`; its prose and context use `trustManifest`. Catalog accepts either alone, rejects both together and retains source spelling in raw evidence. The draft names lowercase `trustManifest` and `trustManifests`. | Standards and security maintainers must define recognized fields before any new profile. Current-profile aliases do not automatically become draft aliases; an unknown extension is not verified trust. |
| Independent signers, 3.5.1 | The single-envelope alignment check concerns the publisher or its subdomain. Retaining an unknown extension does not implement a third-party signer model. | Do not reuse publisher alignment as evaluator authority. Security maintainers own any selected multi-signer design. |
| Contexts and filters, 3.1 and 4.2.1 | JSON-LD normalization already starts with the pinned base context. Query contexts cannot initiate arbitrary remote fetches. Selected indexed terms and bounded filtering remain Catalog rules. | Preserve current limits and outbound authority. Standards and search maintainers own any demonstrated term-level compatibility change. |
| Identifiers, 3.4 and 5 | The selected schema and AIR parser allow terminal-only names. Draft prose shows a namespace component; its CDDL leaves identifiers as strings. | Keep the naming ambiguity explicit. No identifier rewrite or exclusion is selected. Standards and publication maintainers own a future eligibility decision. |
| Versions, 3.2.3 and 5 | Versions are optional strings in both contracts. Imported evidence records whether a version was present. A SemVer example does not require SemVer-only input. | Shared behavior, not a missing feature. Standards and federation maintainers preserve opaque values and absence. |
| Named collection rules, 5 | The draft's named `tags` and `capabilities` rules use nonempty arrays. The selected JSON Schema permits empty arrays; the old reference CDDL also differs from that schema. Both draft entries and the selected entry schema allow extensions. | Standards maintainers must resolve the draft's named-field and catch-all interpretation before changing admission. Keep the selected schema's behavior; do not infer validity from an unknown-extension path or the old CDDL. |
| Federation hops, 4.3 and 6.6 | Local traversal bounds fan-out/depth and tracks visited sources, endpoints and registry IDs. It does not exchange the proposed Hop-Count or query-trail fields. | Existing local protections do not establish the proposed wire protocol. Resolve field grammar and hop processing before implementation. Federation and security maintainers own it. |
| Registry base and HTTP, 4.2 | The public Registry uses `/ard/v1/`; inspection derives and binds peer interfaces. The hardened client attempts HTTP/2. Public HTTPS and negotiated HTTP versions also depend on the deployment edge. | Preserve relative endpoint paths. Any adopted transport claim needs actual client and edge qualification. HTTP and deployment maintainers own it. |
| TLS, 6.5 | The hardened client permits TLS 1.2 or later. Backend HTTP listeners rely on deployment TLS termination. | Security and deployment maintainers own scoped ARD outbound and public-edge qualification for a TLS 1.3-only selection. It does not impose changes on unrelated OIDC, webhook, telemetry or database traffic. |
| Initial contact, Initial Contact 4.1, 4.3.1 and 5 | Inspection supports configured URLs and advertised HTTP/HTML locations. It does not implement the complete DNS procedure. The shared fetcher permits up to three redirects by default. | The configured-only option avoids a new DNS/mDNS capability. If selected, assess the one-redirect, caching and failed-entry backoff requirements at that boundary. Federation maintainers own it. |
| Explore and List, 4.2.3, 4.2.4 and 5 | Catalog implements both. Its selected List contract supplies `items`, optional `total` and `pageToken`, without cross-page snapshot guarantees. Draft CDDL does not define complete Explore/List payloads. | Do not infer a new List envelope, terminal-token rule or authoritative withdrawal guarantee. Standards and federation maintainers own any clarified replacement. |
| Privacy recommendations, 7 | Query disclosure uses administrator-approved source participation. The draft recommends public-registry minimization, zero retention and anonymization, plus consumer-side query sanitization. Existing disclosure controls alone do not establish all those policies. | Security and deployment maintainers own any selected privacy policy. Do not claim zero retention, erase retained authoring data or introduce automatic query rewriting through this assessment. |
| Conditional registration security, 6.7 | Catalog has no ARD dynamic registration interface. Its governed Control authoring API is a separate contract. The draft's registration-token requirements apply when that interface is exposed. | Outside the selected discovery scope. API and security maintainers must assess any future registration requirement separately; no WIMSE dependency or new token issuer is selected. |

The owning code is [publication types](../../internal/publication/types.go), [snapshot construction and eligibility](../../internal/publication/manager.go), [public HTTP handlers](../../internal/platform/httpserver/public.go), [ARD errors](../../internal/platform/httpserver/problem.go), [source admission and parsing](../../internal/federation/manager.go), [bounded List retrieval](../../internal/federation/sync_retrieval.go), [live peer traversal](../../internal/federation/live_search.go), [schema and declaration checks](../../internal/standards/jsonschema.go), [AIR identifiers](../../internal/catalog/urn.go) and [hardened HTTP transport](../../internal/platform/fetcher/fetcher.go). These owners identify future change boundaries; the assessment does not require changes to those files.

## Unresolved requirements

### Publisher claims and unsigned entries

Discovery 3.2.3 and 3.5 make Trust Manifest use optional. Section 3.5 requires subject and signature members within an envelope, while section 5 marks them optional. Sections 3.5.1, 4.3.1 and 6.2 combine signature checks with treatment of entries lacking verified publisher proof as unverified hints. Those are distinct cases. The assessment does not turn that wording into a rule that rejects every unsigned entry.

Section 3.5 specifies a JSON Web Signature (JWS) over the JSON Canonicalization Scheme (JCS) serialization of the envelope without its signature member. It makes ES256 mandatory to implement and names protected `alg` and `kid` headers. This is more specific than the selected framework-defined signature field. Retaining a signature string or reusing an unrelated verifier does not implement that scheme.

Architecture 4.2 says same-origin HTTPS establishes domain authority. Discovery 6.2 explicitly rejects that inference, and Initial Contact 3 and 6 distinguish serving-origin authentication from publisher authority. Catalog retains its existing no-implicit-authority rule rather than inventing an exception from the architecture text.

Discovery 4.3.1 ties cross-origin descriptor checks to invocation. Section 6.2 also addresses verifying registries without the same timing qualification. The requirement cannot safely be summarized as invocation-only. Sections 3.5.2 and 6.4 separately address the integrity of fetched evidence when honoring its claims. None of these ambiguities selects automatic artifact, key or attestation retrieval during ordinary Catalog discovery.

The proposed JWS/JCS envelope is a different authority from Catalog's optional ANS registration assessment. A later signature implementation must select trusted keys, publisher authorization and exact subject/byte binding. Passing an ANS check, parsing an Agent Card signature or matching a declared identity domain does not establish an ARD publisher signature.

### Grammar and discovery boundaries

Search text is mandatory in Discovery 4.2.2.1 but optional in section 5's query model. Catalog currently requires nonempty Search text. The draft also leaves the query-trail header name and grammar unspecified. Its hop-count prose and decreasing Hop-Count header need one defined interoperable processing rule.

Initial Contact 4.1 expressly permits configured-only entry points. Its general procedure has additional redirect, cache, backoff and discovery requirements. Catalog can assess that bounded option without building DNS service discovery, multicast browsing or network-derived source selection.

The unchanged pinned conformance CLI tests the selected upstream profile. It cannot resolve these draft ambiguities or certify IETF draft conformance.

## Retained data and source authority

| Existing data or interface | Consequence of a later profile choice |
| --- | --- |
| Unsigned local publication | Current entries carry no generated publisher signature. A verified-only subset could change eligibility and requires an explicit decision. Approval does not become cryptographic proof. |
| Terminal-only AIR names | Preserve identifiers and history while the draft's naming requirements remain unresolved. Private opaque Resource identifiers are a separate Control API boundary; they do not become eligible public ARD identifiers. |
| Optional or opaque imported versions | Preserve exact values and presence. Neither the draft example nor this assessment selects conversion to SemVer. |
| Open framework trust declarations | Preserve permitted raw evidence and its unverified status. Do not reinterpret an old declaration as a new verified claim. |
| Approved Sources | A changed endpoint, profile or Registry interface requires fresh inspection and the existing replacement-Source boundary. Synchronization refuses drift rather than expanding approved authority. |
| Immutable publication snapshots | Profile checks bind retained snapshots to configured delivery. A later profile selection requires explicit new publication; old bytes cannot be relabeled under a new contract. |

No identifier rewrite, automatic backfill, evidence conversion or reclassification follows from this decision. There is no released predecessor requiring permanent parallel support under [ADR-0017](../src/content/docs/architecture/decisions/0017-first-release-baseline.md).

## Rationale and revisit conditions

Tracking preserves a working selected contract while the candidate's material authority, grammar and header questions remain unresolved. Immediate adoption would combine wire changes with a substantial trust decision, without a demonstrated peer requirement. Declining the work permanently would discard potentially useful convergence on shared discovery formats.

Revisit this decision when a selected peer requires an exact draft interface, a later revision resolves a material ambiguity, or an accepted requirement selects a concrete verification or transport change. A new publication date or a generic ARD compatibility claim alone does not select a profile.

Any later adopt decision must name the supported subset and remaining conformance limits. It must specify affected contracts, current-data eligibility, source replacement and explicit new publication. Keep discovery, invocation, publisher verification and independent evidence assessment separate. DNS crawling, a generic trust framework, a permanent dual stack and new runtime execution authority remain outside this decision.

## Verification boundary

Review this record against the exact official texts, selected profile and owning source. Verify documentation links and the built manual with `make check` and `make docs-site-check`. The latter checks both root and non-root hosting. `make profiles` verifies that the selected embedded bundle and checksums agree; it does not certify the IETF drafts.

If a later runtime adoption is selected, extend the existing tests for its actual boundary. Positive cases must cover emitted media types, Search continuation, selected entry shapes and approved-source discovery. Resolve trust-field casing and empty-collection expectations before adding those cases. Refusal cases must cover ambiguous or invalid envelopes, publisher/subject mismatches, unauthorized key or artifact destinations, loop/hop limits, source-interface drift and retained snapshot/profile mismatch. Preserve tenant/audience checks, partial-failure behavior and exact raw evidence.

Use the existing profile maintenance, unit/contract, conformance-fixture and applicable owned live/peer gates for that implementation. A passing static example or old-profile CLI does not establish new-profile conformance or native exchange. No prototype or live peer run is required to finish this assessment, and none is claimed here.
