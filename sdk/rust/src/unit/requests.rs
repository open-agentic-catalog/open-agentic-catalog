//! Typed request admission, retained response evidence and safe diagnostics.

use super::*;

const RESOURCE_ID: &str = "00000000-0000-7000-8000-000000000010";
const VERSION_ID: &str = "00000000-0000-7000-8000-000000000011";

#[tokio::test]
async fn snapshot_comparison_preserves_selection_members_cursor_and_refusal() {
    const BEFORE: &str = "00000000-0000-7000-8000-000000000001";
    const AFTER: &str = "00000000-0000-7000-8000-000000000002";
    const CURSOR: &str = "opaque+/=& café";
    let wire = br#"{"sourceId":"00000000-0000-7000-8000-000000000003","sourceRevision":7,"before":{"id":"00000000-0000-7000-8000-000000000001","memberCount":2,"startedAt":"2026-01-01T00:00:00Z","withdrawalAuthority":true},"after":{"id":"00000000-0000-7000-8000-000000000002","memberCount":1,"startedAt":"2026-01-02T00:00:00Z","withdrawalAuthority":false},"items":[{"position":1,"sourceKey":"a-missing-version","change":"absent","beforeCount":1,"afterCount":0,"before":{"observationId":"00000000-0000-7000-8000-000000000004","identifier":"urn:air:example.com:demo:agent","version":"","versionPresent":false,"kind":"agent","identityDigest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","occurrence":{"observationId":"00000000-0000-7000-8000-000000000004","sourceId":"00000000-0000-7000-8000-000000000003","sourceRevision":7,"snapshotId":"00000000-0000-7000-8000-000000000001","retrievalDigest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","entryLocation":"/entries/0","exactEntryDigest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","evidenceComplete":true,"observedAt":"2026-01-01T00:00:00Z"}}},{"position":2,"sourceKey":"b-empty-version","change":"added","beforeCount":0,"afterCount":1,"after":{"observationId":"00000000-0000-7000-8000-000000000005","identifier":"urn:air:example.com:demo:agent","version":"","versionPresent":true,"kind":"agent","identityDigest":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","occurrence":{"observationId":"00000000-0000-7000-8000-000000000005","sourceId":"00000000-0000-7000-8000-000000000003","sourceRevision":7,"snapshotId":"00000000-0000-7000-8000-000000000002","retrievalDigest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","entryLocation":"/entries/0","exactEntryDigest":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","evidenceComplete":true,"observedAt":"2026-01-02T00:00:00Z"}}}],"nextCursor":"opaque+/=& caf\u00e9"}"#;
    let mut terminal: Value = serde_json::from_slice(wire).unwrap();
    terminal.as_object_mut().unwrap().remove("nextCursor");
    let mut last = terminal["items"][0].clone();
    last["position"] = json!(3);
    last["sourceKey"] = json!("c-other");
    last["before"]["observationId"] = json!("00000000-0000-7000-8000-000000000006");
    last["before"]["identifier"] = json!("urn:air:example.com:demo:other");
    last["before"]["identityDigest"] = json!(format!("sha256:{}", "e".repeat(64)));
    last["before"]["occurrence"]["observationId"] = last["before"]["observationId"].clone();
    last["before"]["occurrence"]["entryLocation"] = json!("/entries/1");
    last["before"]["occurrence"]["exactEntryDigest"] = json!(format!("sha256:{}", "f".repeat(64)));
    terminal["items"] = json!([last]);
    let terminal_wire = serde_json::to_vec(&terminal).unwrap();
    let transport = Scripted::new(vec![reply(200, wire), reply(200, &terminal_wire), reply(400, br#"{"type":"about:blank","title":"Invalid argument","status":400,"code":"INVALID_ARGUMENT","detail":"Selected evidence unavailable","requestId":"comparison-refusal"}"#)]);
    let client = client(transport.clone());
    let response = client
        .compare_federation_source_snapshots(
            BEFORE,
            AFTER,
            Some(CompareFederationSourceSnapshotsView::Changes),
            Some(2),
            None::<&str>,
        )
        .await
        .unwrap();
    assert_eq!(response.status, 200);
    assert_eq!(response.raw_body, wire);
    let page = response.data.unwrap();
    assert_eq!(page.before.id.to_string(), BEFORE);
    assert_eq!(page.after.id.to_string(), AFTER);
    assert_eq!(page.source_revision, 7);
    assert!(!page.after.withdrawal_authority);
    assert_eq!(page.next_cursor.as_deref(), Some(CURSOR));
    assert_eq!(page.items.len(), 2);
    assert!(page.items[0].after.is_none());
    let absent = page.items[0].before.as_ref().unwrap();
    assert!(!absent.version_present);
    assert_eq!(absent.version, "");
    assert!(page.items[1].before.is_none());
    let added = page.items[1].after.as_ref().unwrap();
    assert!(added.version_present);
    assert_eq!(added.version, "");
    assert_eq!(
        transport.requests.lock().unwrap().len(),
        1,
        "must not continue automatically"
    );
    let completed = client
        .compare_federation_source_snapshots(
            BEFORE,
            AFTER,
            Some(CompareFederationSourceSnapshotsView::Changes),
            Some(2),
            page.next_cursor.as_deref(),
        )
        .await
        .unwrap();
    assert_eq!(completed.status, 200);
    assert_eq!(completed.raw_body, terminal_wire);
    let last_page = completed.data.unwrap();
    assert!(last_page.next_cursor.is_none());
    assert_eq!(last_page.before.member_count, 2);
    assert_eq!(last_page.items.len(), 1);
    assert_eq!(last_page.items[0].position, 3);
    assert_eq!(last_page.items[0].source_key, "c-other");
    assert_eq!(
        last_page.items[0]
            .before
            .as_ref()
            .unwrap()
            .observation_id
            .to_string(),
        "00000000-0000-7000-8000-000000000006"
    );
    assert_eq!(
        transport.requests.lock().unwrap().len(),
        2,
        "terminal page must not trigger another read"
    );
    let refused = client
        .compare_federation_source_snapshots(
            BEFORE,
            AFTER,
            Some(CompareFederationSourceSnapshotsView::Changes),
            Some(2),
            page.next_cursor.as_deref(),
        )
        .await
        .unwrap_err();
    match refused {
        ApiOpError::Api(error) => {
            assert_eq!(error.status, 400);
            assert_eq!(
                serde_json::from_slice::<Value>(&error.raw_body).unwrap()["code"],
                "INVALID_ARGUMENT"
            );
        }
        other => panic!("Expected HTTP refusal: {other:?}"),
    }
    let requests = transport.requests.lock().unwrap();
    assert_eq!(requests.len(), 3, "must not restart or retry");
    for (index, request) in requests.iter().enumerate() {
        assert_eq!(request.method(), reqwest::Method::GET);
        assert_eq!(
            request.url().path(),
            format!("/api/v1/federation/snapshots/{BEFORE}/comparison")
        );
        let query: std::collections::BTreeMap<_, _> =
            request.url().query_pairs().into_owned().collect();
        assert_eq!(
            query.get("otherSnapshotId").map(String::as_str),
            Some(AFTER)
        );
        assert_eq!(query.get("view").map(String::as_str), Some("changes"));
        assert_eq!(query.get("limit").map(String::as_str), Some("2"));
        assert_eq!(
            query.get("cursor").map(String::as_str),
            if index == 0 { None } else { Some(CURSOR) }
        );
        assert!(request.headers().get("Idempotency-Key").is_none());
    }
}

/// A complete Resource response keeps identifier text distinct from its API UUID.
fn resource_wire(identifier: &str) -> Value {
    json!({
        "id": RESOURCE_ID,
        "tenantId": "00000000-0000-7000-8000-000000000002",
        "namespaceId": "00000000-0000-7000-8000-000000000003",
        "canonicalIdentifier": identifier,
        "kind": "agent",
        "createdBy": "private-creator-sentinel",
        "revision": 1,
        "createdAt": "2026-01-01T00:00:00Z",
        "updatedAt": "2026-01-01T00:00:00Z"
    })
}

/// The successful revision control has every required envelope field.
fn revision_wire() -> Vec<u8> {
    let revision = "00000000-0000-7000-8000-000000000012";
    let evidence = "00000000-0000-7000-8000-000000000013";
    let digest = format!("sha256:{}", "a".repeat(64));
    serde_json::to_vec(&json!({
        "version": {
            "id": VERSION_ID, "tenantId": "00000000-0000-7000-8000-000000000002",
            "resourceId": RESOURCE_ID, "version": "1.0.0", "revision": 2,
            "candidateRevisionId": revision,
            "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z"
        },
        "revision": {
            "id": revision, "tenantId": "00000000-0000-7000-8000-000000000002",
            "resourceVersionId": VERSION_ID, "revisionNumber": 2,
            "representations": [{
                "id": "00000000-0000-7000-8000-000000000014", "role": "primary",
                "mediaType": "application/json", "profileId": "structural-v1",
                "contentDigest": digest, "size": 13, "validationState": "valid", "data": {"answer": 42}
            }],
            "contentSetDigest": digest, "evidenceSetId": evidence, "createdBy": "unit",
            "createdAt": "2026-01-01T00:00:00Z"
        },
        "evidence": {
            "id": evidence, "tenantId": "00000000-0000-7000-8000-000000000002",
            "subjectDigest": digest, "digest": digest, "state": "complete",
            "validationProfile": "structural-v1", "policyVersion": "unit",
            "legalRights": {"state": "not_assessed"}, "trust": {"state": "not_assessed"},
            "createdAt": "2026-01-01T00:00:00Z"
        }
    }))
    .unwrap()
}

/// A typed operation must preserve a transport refusal instead of inventing HTTP evidence.
fn assert_transport_error<E>(error: ApiOpError<E>, expected: Error) {
    let text = format!("{error:?} {error}");
    assert!(text.contains(&format!("{expected:?}")));
    match error {
        ApiOpError::Transport(actual) => assert_eq!(actual, expected),
        ApiOpError::Api(error) => panic!("Expected transport refusal, got HTTP {}", error.status),
    }
}

#[tokio::test]
async fn typed_transport_failures_do_not_retry_and_an_explicit_call_recovers() {
    for failure in [Error::RequestFailed, Error::Timeout] {
        let wire = serde_json::to_vec(&resource_wire("private-resource")).unwrap();
        let transport = Scripted::new(vec![Err(failure), reply(200, &wire)]);
        let client = client(transport.clone());

        assert_transport_error(client.get_resource(RESOURCE_ID).await.unwrap_err(), failure);
        assert_eq!(transport.requests.lock().unwrap().len(), 1);

        let recovered = client.get_resource(RESOURCE_ID).await.unwrap();
        assert_eq!(recovered.status, 200);
        assert_eq!(recovered.raw_body, wire);
        assert_eq!(
            recovered.data.unwrap().canonical_identifier,
            "private-resource"
        );
        assert_eq!(transport.requests.lock().unwrap().len(), 2);
    }
}

#[tokio::test]
async fn malformed_typed_command_header_is_refused_before_credentials_or_transport() {
    let wire = br#"{"id":"00000000-0000-7000-8000-000000000001","tenantId":"00000000-0000-7000-8000-000000000002","slug":"unit","displayName":"Unit","revision":1,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}"#;
    let transport = Scripted::new(vec![reply(201, wire)]);
    let calls = Arc::new(AtomicUsize::new(0));
    let acquired = calls.clone();
    let mut config = Config::new("https://catalog.example.com");
    config.token_provider = Some(Arc::new(move || {
        acquired.fetch_add(1, Ordering::SeqCst);
        Box::pin(async { Ok("unit-provider-token".into()) })
    }));
    let client = Client::with_transport(config, transport.clone()).unwrap();

    let error = client
        .create_namespace(
            "private-command\r\ninjected: value",
            CreateNamespace::new("Unit".into(), "unit".into()),
        )
        .await
        .unwrap_err();
    assert!(!format!("{error:?} {error}").contains("private-command"));
    assert_transport_error(error, Error::InvalidRequest);
    assert_eq!(calls.load(Ordering::SeqCst), 0);
    assert!(transport.requests.lock().unwrap().is_empty());

    let result = client
        .create_namespace(
            "unit-command",
            CreateNamespace::new("Unit".into(), "unit".into()),
        )
        .await
        .unwrap();
    assert_eq!(result.status, 201);
    assert_eq!(result.data.unwrap().slug, "unit");
    assert_eq!(calls.load(Ordering::SeqCst), 1);
    let requests = transport.requests.lock().unwrap();
    assert_eq!(requests.len(), 1);
    assert_eq!(requests[0].headers()["idempotency-key"], "unit-command");
}

#[tokio::test]
async fn conflicting_shared_representation_fields_fail_before_credential_acquisition() {
    let representation = json!({
        "role": "primary", "mediaType": "application/json", "data": {"answer": 42}
    });
    let original: RepresentationInput = serde_json::from_value(representation.clone()).unwrap();
    assert_eq!(serde_json::to_value(&original).unwrap(), representation);
    let mut conflicting = original.clone();
    // Public field edits must not silently choose between the base and retained variant.
    conflicting.media_type = "application/a2a-agent-card+json".into();

    let transport = Scripted::new(vec![reply(201, &revision_wire())]);
    let calls = Arc::new(AtomicUsize::new(0));
    let acquired = calls.clone();
    let mut config = Config::new("https://catalog.example.com");
    config.token_provider = Some(Arc::new(move || {
        acquired.fetch_add(1, Ordering::SeqCst);
        Box::pin(async { Ok("unit-provider-token".into()) })
    }));
    let client = Client::with_transport(config, transport.clone()).unwrap();
    let error = client
        .create_version_revision(
            VERSION_ID,
            "revision-command",
            "\"revision-1\"",
            CreateVersionRevisionRequest {
                representations: vec![conflicting],
            },
        )
        .await
        .unwrap_err();
    assert_transport_error(error, Error::InvalidRequest);
    assert_eq!(calls.load(Ordering::SeqCst), 0);
    assert!(transport.requests.lock().unwrap().is_empty());

    let result = client
        .create_version_revision(
            VERSION_ID,
            "revision-command",
            "\"revision-1\"",
            CreateVersionRevisionRequest {
                representations: vec![original],
            },
        )
        .await
        .unwrap();
    assert_eq!(result.status, 201);
    assert_eq!(result.data.unwrap().revision.revision_number, 2);
    assert_eq!(calls.load(Ordering::SeqCst), 1);
    let requests = transport.requests.lock().unwrap();
    assert_eq!(requests.len(), 1);
    assert_eq!(requests[0].headers()["idempotency-key"], "revision-command");
    assert_eq!(requests[0].headers()["if-match"], "\"revision-1\"");
    assert_eq!(
        serde_json::from_slice::<Value>(requests[0].body().unwrap().as_bytes().unwrap()).unwrap(),
        json!({"representations": [representation]})
    );
}

#[tokio::test]
async fn opaque_resource_identifiers_remain_data_and_response_debug_hides_private_evidence() {
    for identifier in [
        "private-resource",
        "cafe\u{301}-日本語-🙂",
        "https://uncontacted.example/private?token=opaque-identifier-sentinel",
    ] {
        let wire = serde_json::to_vec(&resource_wire(identifier)).unwrap();
        let mut response = reply(200, &wire).unwrap();
        response.headers.insert(
            "x-private-header",
            HeaderValue::from_static("private-header-sentinel"),
        );
        let transport = Scripted::new(vec![Ok(response)]);
        let result = client(transport.clone())
            .get_resource(RESOURCE_ID)
            .await
            .unwrap();
        assert_eq!(result.status, 200);
        assert_eq!(result.raw_body, wire);
        assert_eq!(
            result.headers["x-private-header"],
            "private-header-sentinel"
        );
        let resource = result.data.as_ref().unwrap();
        assert_eq!(resource.id.to_string(), RESOURCE_ID);
        assert_eq!(resource.canonical_identifier, identifier);
        assert_eq!(resource.kind, ResourceKind::Agent);
        assert_eq!(resource.created_by, "private-creator-sentinel");

        let debug = format!("{result:?}");
        assert!(debug.contains("200"));
        assert!(!debug.contains(&format!("{:?}", result.raw_body)));
        for private in [
            identifier,
            "private-creator-sentinel",
            "private-header-sentinel",
        ] {
            assert!(
                !debug.contains(private),
                "response Debug disclosed {private}"
            );
        }
        let requests = transport.requests.lock().unwrap();
        assert_eq!(
            requests.len(),
            1,
            "identifier metadata must not cause another request"
        );
        assert_eq!(requests[0].method(), "GET");
        assert_eq!(
            requests[0].url().as_str(),
            format!("https://catalog.example.com/api/v1/resources/{RESOURCE_ID}")
        );
    }
}

#[tokio::test]
async fn typed_decode_errors_retain_private_parser_evidence_without_formatting_it() {
    let original = resource_wire("private-resource");
    let good = serde_json::to_vec(&original).unwrap();
    let mut invalid = original;
    invalid["kind"] = json!("private-invalid-kind-sentinel");
    let invalid = serde_json::to_vec(&invalid).unwrap();
    let mut refused = reply(200, &invalid).unwrap();
    refused.headers.insert(
        "x-private-header",
        HeaderValue::from_static("private-header-sentinel"),
    );
    let transport = Scripted::new(vec![reply(200, &good), Ok(refused), reply(200, &good)]);
    let client = client(transport.clone());
    assert_eq!(
        client
            .get_resource(RESOURCE_ID)
            .await
            .unwrap()
            .data
            .unwrap()
            .kind,
        ResourceKind::Agent
    );

    let error = client.get_resource(RESOURCE_ID).await.unwrap_err();
    let outer = format!("{error:?} {error}");
    assert!(outer.contains("200"));
    for private in [
        "private-invalid-kind-sentinel",
        "private-header-sentinel",
        "private-creator-sentinel",
    ] {
        assert!(!outer.contains(private));
    }
    match error {
        ApiOpError::Api(error) => {
            assert_eq!(error.status, 200);
            assert_eq!(error.raw_body, invalid);
            assert!(error.body.contains("private-invalid-kind-sentinel"));
            assert!(
                error
                    .parse_error
                    .as_deref()
                    .unwrap()
                    .contains("private-invalid-kind-sentinel")
            );
            assert_eq!(error.headers["x-private-header"], "private-header-sentinel");
            assert!(error.typed.is_none());
            let inner = format!("{error:?} {error}");
            assert!(inner.contains("200"));
            assert!(!inner.contains(&format!("{:?}", error.raw_body)));
            assert!(!inner.contains("private-invalid-kind-sentinel"));
            assert!(!inner.contains("private-header-sentinel"));
        }
        _ => panic!("Expected an HTTP decode error with retained evidence"),
    }
    assert_eq!(
        transport.requests.lock().unwrap().len(),
        2,
        "decoding must not retry"
    );
    assert_eq!(
        client.get_resource(RESOURCE_ID).await.unwrap().raw_body,
        good
    );
    assert_eq!(transport.requests.lock().unwrap().len(), 3);
}
