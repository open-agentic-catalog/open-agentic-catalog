---
title: "ADR-0024: Optional ANS registration verification"
---

- Status: Accepted

## Context

ARD permits a Trust Manifest and recommends verification through its declared framework. It does not define one signature format or trusted authority list. Structural validation alone cannot establish that an accepted authority signed a registration.

The Catalog needs one explicit procedure with a limited claim. It must preserve ordinary discovery when evidence is absent, unsupported or unavailable.

## Decision

Define the service-owned `ans-registration-v1` profile for the pinned [ANS implementation](https://github.com/agentnameservice/ans/tree/85f16728a6b7444d6a9522daf01b6090a5e32e72). Support its V1 and V2 registration events. This profile is separate from ARD format versions and operator policy names.

Operators supply registration-authority and transparency-log public keys, authority names, exact publisher domains and an evidence origin. Evidence cannot supply new authority. No hosted verification service or private signing key is required.

Verify these results separately:

1. The attached COSE receipt signature.
2. The registration-authority signature over the exact event's canonical JSON.
3. The log signature over the complete canonical envelope, excluding its outer signature member.
4. A signed checkpoint and inclusion proof for the same tree size, root and log origin.
5. The selected entry's publisher, name, version, ANS subject, event ID and receipt reference.
6. Signed status, freshness, lifecycle state and applicable registration and certificate expiry.

Use ES256 with P-256 keys. Use [RFC 8785](https://www.rfc-editor.org/rfc/rfc8785) for signed JSON only. Preserve unknown signed fields during canonicalization. Reject duplicate keys, unsupported critical headers, delegated keys, ambiguous references and malformed encodings.

Interpret signed JSON field names case-sensitively. Unknown signed extensions cannot replace required claims through a case variant.

Reuse Apache-2.0 `go-jose/v4` for detached JWS. Use MIT `ldclabs/cose` and `fxamacker/cbor`, the Apache-2.0 producer canonicalizer and Apache-2.0 `transparency-dev/merkle`. Use Go cryptography for checkpoint signatures. The feature adds no MPL runtime dependency and does not implement cryptographic primitives.

The [profile contract](../../../../../../internal/standards/anstrust/README.md) defines algorithms, bounds, result codes and unsupported cases. Authentic public V1/V2 fixtures come from the unchanged owned ANS producers. Private keys are not retained.

## Availability and integration boundary

The offline `catalogctl trust verify-bundle` command is implemented. It reads explicit local files and performs no network or database operation. Its result states the assessment time. Archived evidence can be checked at an explicit historical time.

Default-off configuration, startup public-key loading and bounded evidence acquisition are implemented. Acquisition uses the hardened fetcher and the same offline protocol checks. It accepts no credentials and permits only one exact-size historical checkpoint lookup.

Administrator-requested assessments reuse the durable Job lifecycle. Exact entry and source bindings fence enqueue and completion. Private results retain original signed bytes, with atomic completion audit evidence. Source, loaded policy, key generation and signed validity determine current applicability. Historical reads retain current authorization checks. Ordinary discovery does not fetch receipts or enqueue assessments. See [configuration](../../reference/configuration.mdx#ans-registration-verification) and [authority operations](../../operations/security.md#ans-verification-authorities).

The separately selected `make test-trust-verification` qualifies this workflow against pinned, unchanged local ANS services and real PostgreSQL. It reuses the existing deployment supervisor and tests activation, key changes, private results, cancellation and outage recovery. Native development identity controls cannot establish public publisher ownership. See the [qualification guide](../../../../../../test/interoperability/ans/README.md#real-local-assessment-qualification).

## Alternatives

Structural checks alone leave the selected registration question unanswered. A general trust framework would add unsupported authority and integration choices. Copying ANS internal packages would couple Catalog to peer implementation details and its full dependency graph. A hosted verifier would add an unnecessary external service.

## Consequences and limits

A valid registration establishes an accepted authority's claim about a specific subject and event. The configured publisher list authorizes that claim; it does not independently prove publisher ownership.

The result does not authenticate every ARD field, endpoint, artifact, software behavior, legal right or execution permission. Native variants can share a registration. Their exact entry digests remain distinct.

A valid inclusion proof establishes membership in one signed tree. It does not establish global log consistency or prevent equivocation. A fresh status token is bounded evidence, not immediate revocation detection.

An expired or revoked registration can retain valid historical signatures. Missing or unsupported evidence does not invalidate ordinary discovery. Broad trust and rights states remain `not_assessed`.

A general trust engine, automatic admission filter, trust ranking, public badge and additional frameworks are outside this decision.
