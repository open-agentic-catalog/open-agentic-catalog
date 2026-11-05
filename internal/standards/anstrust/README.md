# Offline ANS registration profile

`ans-registration-v1` verifies registration evidence from ANS revision `85f16728a6b7444d6a9522daf01b6090a5e32e72`. It supports V1 and V2 agent events. This is a Catalog verification procedure, not an upstream ARD profile or general software trust result. [ADR-0024](../../../documentation/src/content/docs/architecture/decisions/0024-optional-ans-registration-verification.md) owns the decision.

## Authority and signed bytes

An operator selects exact registration-authority and log names, public P-256 keys, publisher domains and an HTTPS evidence origin. Registration key IDs select configured full keys. COSE and checkpoint short IDs are hints; accepted full SPKI SHA-256 fingerprints establish authority. Duplicate keys and colliding configured log hints are rejected. Keys from evidence, embedded JWKs, delegation and remote key retrieval are unsupported.

The verifier checks these independent layers:

| Layer | Required input and verification |
| --- | --- |
| Receipt | Tagged [COSE Sign1](https://www.rfc-editor.org/rfc/rfc9052.html), attached payload, protected ES256 algorithm and four-byte SPKI hint; original protected bytes remain signed. |
| Producer | Detached ES256 JWS over [RFC 8785](https://www.rfc-editor.org/rfc/rfc8785) canonical event JSON; matching configured RA name and producer key ID. |
| Log envelope | Detached ES256 JWS over the complete canonical envelope without its outer `signature`; matching configured log name and an accepted log key. |
| Checkpoint | Exact [c2sp signed-note](https://c2sp.org/signed-note@v1.1.0) body, including final newline; configured origin, decimal tree size and canonical base64 root. A signature is four SPKI-hash bytes followed by ASN.1 DER ECDSA over SHA-256. |
| Inclusion | [RFC 6962](https://www.rfc-editor.org/rfc/rfc6962.html) SHA-256 leaf/path against the authenticated checkpoint's same size and root. The receipt's unsigned proof root supplies no authority. |
| Target | Exact Finder-derived AIR, sanitized display name and version, signed ANS name, TL event ID, publisher identity and one exact receipt reference. |
| Status | Tagged ES256 COSE with the native content type, accepted log key, exact ANS subject/name, supported status and bounded validity. |

Both event schemas support `AGENT_REGISTERED`, `AGENT_RENEWED`, `AGENT_REVOKED` and `AGENT_DEPRECATED`. Unknown schemas or lifecycle types are unsupported. Preserve authenticated unknown JSON members; never rebuild signed payloads from reduced structs. RFC 8785 numeric rules apply only to signed JSON, not ordinary discovery metadata.

Protocol field names are case-sensitive in the envelope, producer and event. Unknown signed extensions cannot replace required fields through a case variant. Log event IDs contain 1-128 ASCII letters, digits, periods, underscores or hyphens.

The raw checkpoint can include the native producer's additional JWS signature. It cannot replace the required accepted DER signature. When the latest checkpoint has advanced, one history object must authenticate the proof's exact tree. Verify its `checkpointText` and c2sp signature. Ignore unsigned `valid`, `publicKeyPem` and `createdAt` claims.

Finder removes Unicode Cc/Cf characters from names and versions. It derives AIR labels from whitespace-separated display names. Compare the display name too: `alpha beta` and `alpha-beta` can produce the same AIR. Require the selected entry's `metadata.ansName` and `metadata.logId`. A renewal with a different event ID requires reselection.

The receipt URI must equal the configured origin plus `/v1/agents/<signed-ansId>/receipt`. Only one `ANS-Registration` reference is supported. Unknown explicit trust schemas remain unsupported. When acquisition refuses such a framework, a missing receipt does not erase the `trust_framework_unsupported` target result. Endpoint URLs, descriptions, tags and native format variants are not authenticated registration claims.

## Current status and limits

Default maximum status age is one hour; runtime settings permit one second through 24 hours.
Default future issuance allowance is 60 seconds, with a 60-second ceiling; explicit zero permits no future issuance.
The CLI defaults omitted values and also accepts zero status age as one hour.
The allowance never extends token, registration or certificate expiry.
Expiry and maximum age are exclusive upper bounds.

Require signed registration issuance/expiry and dated identity/server certificate sets. V1 uses `validIdentityCerts` and `validServerCerts`; V2 uses `identityCerts` and `serverCerts`. Missing dates cannot establish current eligibility. Certificate overlap can retain eligibility while a successor remains valid. Contradictory status certificate claims fail verification. Status metadata hashes must equal the signed event's `attestations.metadataHashes`. Matching hashes authenticate producer claims; this offline profile does not retrieve or compare artifact bytes.

Only an `ACTIVE` status with an eligible event can establish a current registration. Signed `REVOKED`, `EXPIRED` and `DEPRECATED` status cannot. An HTTP error or unsigned response is not signed revocation evidence. Status tokens have no event digest or TL event ID; do not claim they provide either.

| Input | Hard byte ceiling |
| --- | ---: |
| Complete JSON bundle | 1 MiB |
| Exact entry | 256 KiB |
| COSE receipt | 256 KiB |
| Raw checkpoint | 32 KiB |
| Checkpoint history | 256 KiB |
| Status token | 64 KiB |
| Policy file | 64 KiB |
| Each public PEM key | 4 KiB |
| CLI result | 16 KiB |

CBOR rejects indefinite lengths, duplicate keys and invalid UTF-8. Limits are 16 nested levels, 128 array elements and 64 map pairs. Proof paths have at most 64 SHA-256 siblings. Policies permit eight keys per role and 64 exact publisher domains. JSON validation rejects duplicate keys, malformed Unicode and trailing values before canonicalization.

## Offline command

Build with `make build`. Use explicit regular local files; relative key paths resolve beside the policy file. Symlinks and special input files are rejected. No runtime configuration, DSN, issuer, network access or enabled Catalog service is required.

```sh
./bin/catalogctl trust verify-bundle \
  --policy internal/standards/anstrust/testdata/native/policy.json \
  --file internal/standards/anstrust/testdata/native/v1.json \
  --at 2026-10-09T14:31:30Z
```

Omit `--at` to assess at the current UTC time. The example checks archived synthetic evidence at its recorded capture time. It does not establish a current real-world registration.

The policy file uses `profile`, `registrationAuthority`, `logOrigin`, `evidenceOrigin`, `publisherDomains`, `registrationKeys: [{"id": "...", "file": "..."}]` and `logKeyFiles: ["..."]`. Optional `maxStatusAgeSeconds` and `futureSkewSeconds` select bounded freshness settings. The command defaults to one hour and 60 seconds; explicit zero skew permits no future issuance. The checked-in policy is a complete example with synthetic authority.

Stdout contains one JSON result for an assessment. Invalid input or policy produces safe stderr and a nonzero exit without a result. A negative, unsupported or incomplete assessment produces its JSON result and a nonzero exit. Exit zero requires `currentRegistration: true`.

Each stage has a `state` and fixed `code`. States are `valid`, `invalid`, `unavailable`, `unsupported` and `not_assessed`. Unperformed stages use `prerequisite_failed`. Results include exact entry and policy digests, assessment time, authenticated subject/event and accepted signature-key fingerprints. `logKeyFingerprint` identifies the COSE receipt key; `envelopeLogKeyFingerprint` identifies the outer JWS key. Both can differ during accepted key overlap. `validUntil` is bounded by status freshness, expiry and signed registration/certificate limits.

A sparse entry can lack profile-required fields while remaining valid ordinary discovery. Its assessment reports unavailable evidence. Historical signatures can remain valid after current status expires. No result changes Catalog trust or rights, publishes a badge, or grants approval, installation or execution authority.

## Tests and fixture refresh

Run the ordinary `make test-unit`, `make test-functional-local`, `make test-fuzz` and source/dependency gates. Pure units use local public fixtures and injected time. Tagged CLI functional tests build and execute the actual command.

Use `make capture-ans-trust-fixtures` to capture new evidence through the existing owned Minikube ANS supervisor. Install its documented prerequisites first. The target deploys the pinned native producers, exports only their public keys, registers two synthetic local subjects and cleans up owned resources. It does not enroll a source or implement a Catalog assessment. Inspect the report and independently review expected outcomes before replacing maintained fixtures.

[Fixture provenance](testdata/native/provenance.json) records source, binary/image identities, exact bundle hashes and local requests. [The ANS harness guide](../../../test/interoperability/ans/README.md) explains scope and cleanup. ANS is MIT licensed; its [retained license](../../../test/interoperability/ans/LICENSE) applies to source-derived material. The fixtures contain synthetic public evidence, not private signing keys or proof of public domain ownership.

Use `make test-trust-verification` for real local API/worker/PostgreSQL qualification against unchanged ANS services. It also verifies captured evidence with the command exported from the actual Catalog image. The [ANS guide](../../../test/interoperability/ans/README.md#real-local-assessment-qualification) defines activation, private results, faults, recovery and cleanup.

Measure complete offline verification with:

```sh
go test ./internal/standards/anstrust -run '^$' -bench '^BenchmarkVerifyNative$' -benchtime=1s -count=3
```

The benchmark validates native V1/V2 evidence and reports allocations. It excludes fixture loading, key parsing, network retrieval and Job queue time.

## Bounded acquisition support

`Reference` rejects ambiguous or out-of-policy receipt references before network contact.
`RequiredHistory` authenticates the receipt, target, proof and latest checkpoint before selecting one exact-size lookup.
These functions perform no I/O. The [platform adapter](../../platform/trustevidence/README.md) owns acquisition.
Entry field names are case-sensitive. Unknown extensions cannot replace fields used for subject matching.
The explicit ARD `TrustManifest` alias is supported; entries containing both spellings are rejected.
An advanced valid checkpoint with no same-size history reports `checkpoint_history_missing` as unavailable evidence.
It never bypasses inclusion authentication.
