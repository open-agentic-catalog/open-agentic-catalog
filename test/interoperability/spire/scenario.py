"""Acceptance scenarios using actual JWT-SVIDs and current Catalog grants."""
import base64
import json
import time

from fixture import DOMAIN, AUDIENCE, ISSUER, TENANTS, until
from tutorial_oidc import activate_publication, claims, create_draft_listing, publish_reviewed_release

MANIFEST = "/.well-known/ard.json"
ME = "/api/v1/me"


def check(condition, message):
    if not condition:
        raise ValueError(message)


def key(token):
    segment = token.split(".")[0]
    header = json.loads(base64.urlsafe_b64decode(segment + "=" * (-len(segment) % 4)))
    check(header["alg"] == "ES256", "Expected ES256 JWT-SVID")
    return header["kid"]


def refusal(client, route, status, code, *, challenge=None, if_none_match=None):
    body, etag = client.request("GET", route, expected=status, expected_challenge=challenge, if_none_match=if_none_match)
    if if_none_match is not None:
        check(etag is None, "Denied conditional read disclosed the publication ETag")
    if route == MANIFEST:
        check(set(body) == {"errorCode", "message"} and body["errorCode"] == code and body["message"],
              "Incorrect ARD refusal")
    else:
        required = {"type", "title", "status", "detail", "code", "requestId"}
        check(required <= body.keys() <= required | {"instance"}
              and body["status"] == status and body["code"] == code and body.get("instance", route) == route
              and all(isinstance(body[k], str) and body[k] for k in ("type", "title", "detail", "requestId")),
              "Incorrect Control refusal")
    return body


def publication(client, identifier):
    manifest, etag = client.request("GET", MANIFEST)
    check(any(item["identifier"] == identifier and item.get("version") == "1.0.0" for item in manifest["entries"]),
          "Missing published version")
    return etag


def member(admin, name, role, **scope):
    return admin.request("POST", "/api/v1/role-assignments", {"subject": DOMAIN + name, "actorType": "service",
                         "role": role, "reason": "SPIRE workload qualification", **scope}, 201)[0]


def private_resource(admin, slug):
    ns, _ = admin.request("POST", "/api/v1/namespaces", {"slug": slug, "displayName": slug}, 201)
    resource, _ = admin.request("POST", "/api/v1/resources", {"namespaceId": ns["id"], "kind": "agent",
                                "canonicalIdentifier": "urn:air:tutorial.catalog.example:agents:" + slug}, 201)
    return resource


def read_private(client, resource):
    data, _ = client.request("GET", "/api/v1/resources/" + resource["id"])
    for field in ("id", "canonicalIdentifier", "namespaceId", "kind", "tenantId", "revision"):
        check(data[field] == resource[field], "Incorrect private Resource " + field)


def advertised(f, authority):
    return f.dc("run", "--rm", "--no-deps", "-T", "reader", "--issuer-key", authority, check=False).returncode == 0


def rotation(f, published):
    def rotate():
        prepared = json.loads(f.server("localauthority", "jwt", "prepare", "-output", "json"))
        authority = prepared["prepared_authority"]["authority_id"]
        f.server("localauthority", "jwt", "activate", "-authorityID", authority)
        return authority

    def signed_by(name, authority):
        value = f.issue(name)
        return value if key(value) == authority else None

    # Keep a long-lived known-key control valid through both independent rotations.
    before = f.issue("control")
    publication(f.client(before), published)
    rotated_key = rotate()
    check(rotated_key != key(before), "Signer did not rotate")
    until(lambda: advertised(f, rotated_key))
    rotated = until(lambda: signed_by("reporter", rotated_key))
    publication(f.client(rotated), published)
    f.dc("stop", "issuer")
    publication(f.client(rotated), published)
    unseen = rotate()
    check(unseen not in (rotated_key, key(before)), "Missing new signer during provider outage")
    # The Agent may reuse a JWT-SVID. The reporter renews within the test deadline.
    after = until(lambda: signed_by("reporter", unseen))
    publication(f.client(before), published)
    refusal(f.client(after), MANIFEST, 503, "DEPENDENCY_UNAVAILABLE")
    refusal(f.client(after), ME, 503, "DEPENDENCY_UNAVAILABLE", challenge="")
    f.dc("start", "issuer")

    until(lambda: advertised(f, unseen))
    check(time.time() < claims(after)["exp"], "Workload credential expired before issuer recovery")
    publication(f.client(after), published)
    me, _ = f.client(after).request("GET", ME)
    check(me["subject"] == DOMAIN + "reporter" and me["tenantId"] == TENANTS["a"], "Wrong identity after issuer recovery")
    return {"initial": key(before), "rotated": rotated_key, "duringOutage": unseen}


def claim_controls(f, published, signing_key):
    for issuer in ("", "https://different.example"):
        f.server_issuer(issuer)
        f.dc("restart", "server")

        def issued():
            value = f.issue("reporter")
            body = claims(value)
            matches = body.get("iss") == issuer if issuer else "iss" not in body
            return value if matches else None

        token = until(issued)
        check(time.time() < claims(token)["exp"] and key(token) == signing_key and claims(token)["sub"] == DOMAIN + "reporter"
              and claims(token)["aud"] in (AUDIENCE, [AUDIENCE]), "Unexpected key or claims in issuer control")
        refusal(f.client(token), MANIFEST, 401, "UNAUTHENTICATED")
        refusal(f.client(token), ME, 401, "UNAUTHENTICATED")
    f.server_issuer(ISSUER)
    f.dc("restart", "server")

    def restored():
        publication(f.client(f.issue("reporter")), published)
        return True

    until(restored)
    # The same valid control credential spans both restarts and the return to dedicated mode.
    token = f.issue("control")
    publication(f.client(token), published)
    path = f.path / "a/api.json"
    config = json.loads(path.read_text())
    dedicated = config["auth"].copy()
    check(all(k not in claims(token) for k in ("scope", "organization")), "Unexpected deployment-specific claims")
    config["auth"]["enforceScopes"] = True
    path.write_text(json.dumps(config))
    f.dc("restart", "api-a")
    def scoped():
        me, _ = f.client(token).request("GET", ME)
        return me["scopeEnforcement"] and me["subject"] == DOMAIN + "control" and not me["scopes"]
    until(scoped)
    refusal(f.client(token), MANIFEST, 403, "FORBIDDEN", challenge='Bearer error="insufficient_scope", scope="catalog.read"')
    config["auth"].update(tenancyMode="shared", organizationClaim="organization")
    del config["auth"]["dedicatedTenantId"]
    path.write_text(json.dumps(config))
    f.dc("restart", "api-a")
    until(lambda: refusal(f.client(token), ME, 401, "UNAUTHENTICATED"))
    refusal(f.client(token), MANIFEST, 401, "UNAUTHENTICATED")
    config["auth"] = dedicated
    path.write_text(json.dumps(config))
    f.dc("restart", "api-a")
    until(lambda: f.client().request("GET", "/readyz"))
    publication(f.client(token), published)


def exercise(f, report):
    completed = report["completed"]
    admin = f.client(until(lambda: f.issue("admin")))
    admin_me, _ = until(lambda: admin.request("GET", ME))
    first = f.issue("reporter")
    control = f.issue("control")
    check(claims(first)["sub"] == DOMAIN + "reporter" and claims(control)["sub"] == DOMAIN + "control", "Wrong identities")
    f.issue("control", selected="reporter", denied=True)
    completed.append("distinct-attested-identities")
    for role in ("reviewer", "reader"):
        member(admin, "reviewer", role)
    published, publisher_id = publish_reviewed_release(admin, f.client(f.issue("reviewer")))
    private = private_resource(admin, "private-reporter")
    other = private_resource(admin, "private-other")
    # Both Listings exist when Catalog selects the snapshot. Only one has a reviewed release.
    draft = create_draft_listing(admin, private, publisher_id, "Unpublished reporter draft")
    check(draft["resourceId"] == private["id"] and draft["candidateRevisionId"] and not draft.get("currentReleaseId"),
          "Expected a draft Listing with no current release")
    activate_publication(admin, published)
    report["records"] = {"privateResourceId": private["id"], "privateIdentifier": private["canonicalIdentifier"],
                         "unpublishedListingRevisionId": draft["candidateRevisionId"], "otherNamespaceResourceId": other["id"],
                         "publishedIdentifier": published, "publishedVersion": "1.0.0", "foreignHost": "catalog-b.test"}
    reporter = member(admin, "reporter", "reader", namespaceId=private["namespaceId"])
    control_member = member(admin, "control", "service_client")
    client = f.client(f.issue("reporter"))
    me, _ = client.request("GET", ME)
    check((me["subject"], me["tenantId"], me["actorType"], me["membershipId"], me["realmId"], me["scopeEnforcement"])
          == (DOMAIN + "reporter", TENANTS["a"], "service", reporter["membershipId"], admin_me["realmId"], False),
          "Wrong reporter membership")
    route = "/api/v1/resources/" + private["id"]
    read_private(client, private)
    refusal(client, "/api/v1/resources/" + other["id"], 403, "FORBIDDEN")
    refusal(f.client(), MANIFEST, 401, "UNAUTHENTICATED")
    refusal(client, MANIFEST, 403, "FORBIDDEN")
    role, _ = admin.request("POST", "/api/v1/access/roles", {"name": "Publication reader", "permissions": ["discovery.read"],
                            "reason": "SPIRE authenticated publication"}, 201)
    assignments = "/api/v1/access/assignments"
    def grant(membership):
        return admin.request("POST", assignments, {"roleDefinitionId": role["id"], "membershipId": membership["membershipId"],
                             "reason": "Read publication"}, 201)
    assignment, assignment_etag = grant(reporter)
    grant(control_member)
    publication(f.client(control), published)
    refusal(f.client(control), route, 403, "FORBIDDEN")
    reader_token = f.issue("reporter")
    publication(f.client(reader_token), published)
    refusal(f.client(reader_token, "b"), MANIFEST, 403, "FORBIDDEN", challenge="")
    f.dc("run", "--rm", "--no-deps", "-T", "reader", private["id"], private["canonicalIdentifier"], published)
    completed.append("go-sdk-private-resource-and-published-version")
    cli = json.loads(f.dc("run", "--rm", "--no-deps", "-T", "reader", "--cli-capabilities").stdout)
    check(cli["meta"]["requestId"] and report["catalogBuild"]["ardProfile"] in cli["data"]["profiles"]["ard"]
          and "publication.ard" in cli["data"]["operations"], "CLI did not return this Catalog's ARD capabilities")
    completed.append("catalogctl-authenticated-capabilities")
    membership_page = "/api/v1/memberships?limit=20"
    initial_members, _ = admin.request("GET", membership_page)
    check(not initial_members.get("nextCursor") and all(m["subject"] != DOMAIN + "unassigned"
          for m in initial_members["items"]), "Unexpected initial memberships")
    wrong_audience = f.issue("control", audience="other-service")
    check(claims(wrong_audience)["aud"] in ("other-service", ["other-service"])
          and claims(wrong_audience)["sub"] == DOMAIN + "control" and claims(wrong_audience)["iss"] == ISSUER,
          "Wrong audience control did not isolate the audience claim")
    for value in (wrong_audience, f.issue("unassigned")):
        refusal(f.client(value), MANIFEST, 401, "UNAUTHENTICATED")
        refusal(f.client(value), ME, 401, "UNAUTHENTICATED")
    check(admin.request("GET", membership_page)[0] == initial_members, "Authentication changed a membership")
    report["jwtAuthorityIds"] = rotation(f, published)
    completed.append("signing-authority-rotation-provider-outage-and-recovery")
    claim_controls(f, published, report["jwtAuthorityIds"]["duringOutage"])
    completed.append("signed-issuer-and-deployment-claim-refusals")
    predecessor = f.issue("reporter")
    publication(f.client(predecessor), published)
    # Wait for the externally issued token's own expiry; keep the successor valid for revocation.
    expires = claims(predecessor)["exp"]
    until(lambda: time.time() > expires + 1, timeout=60)
    successor = f.issue("reporter")
    check(successor != predecessor and claims(successor)["exp"] > claims(predecessor)["exp"]
          and all(claims(successor)[k] == claims(predecessor)[k] for k in ("sub", "aud", "iss")), "Invalid renewal")
    reader = f.client(successor)
    cached_etag = publication(reader, published)
    check(cached_etag, "Missing ETag on authenticated publication")
    body, returned_etag = reader.request("GET", MANIFEST, expected=304, if_none_match=cached_etag)
    check(body is None and returned_etag == cached_etag, "Expected unchanged authenticated publication")
    refusal(f.client(predecessor), MANIFEST, 401, "UNAUTHENTICATED")
    refusal(f.client(predecessor), route, 401, "UNAUTHENTICATED")
    admin.request("POST", assignments + "/" + assignment["id"] + ":revoke", {"reason": "Withdraw discovery"}, etag=assignment_etag)
    refusal(reader, MANIFEST, 403, "FORBIDDEN")
    refusal(reader, MANIFEST, 403, "FORBIDDEN", if_none_match=cached_etag)
    read_private(reader, private)
    path = "/api/v1/memberships/" + reporter["membershipId"]
    _, etag = admin.request("GET", path)
    _, etag = admin.request("POST", path + ":suspend", {"reason": "Suspend reporting service"}, etag=etag)
    refusal(reader, ME, 401, "UNAUTHENTICATED")
    refusal(reader, route, 401, "UNAUTHENTICATED")
    refusal(reader, MANIFEST, 401, "UNAUTHENTICATED")
    refusal(reader, MANIFEST, 401, "UNAUTHENTICATED", if_none_match=cached_etag)
    publication(f.client(f.issue("control")), published)
    admin.request("POST", path + ":reactivate", {"reason": "Confirm membership caused the refusal"}, etag=etag)
    read_private(reader, private)
    refusal(reader, MANIFEST, 403, "FORBIDDEN")
    completed.append("conditional-publication-rechecks-current-access")
    completed.append("audience-membership-renewal-expiry-and-revocation")
