use open_agentic_catalog::*;
use reqwest::header::{HeaderMap, HeaderValue};
use serde::{Serialize, de::DeserializeOwned};
use serde_json::{Value, json};
use std::{env, fs, time::Duration};

fn typed<T: DeserializeOwned>(value: Value) -> T {
    serde_json::from_value(value).unwrap()
}
fn config(origin: &str, token: Option<&str>) -> Config {
    let mut config = Config::new(origin);
    config.allow_loopback_http = true;
    config.token = token.map(str::to_owned);
    config
}
fn data<T: Serialize>(response: Response<T>, status: u16) -> Value {
    assert_eq!(response.status, status);
    serde_json::to_value(response.data.unwrap()).unwrap()
}
async fn call(
    client: &Client,
    method: &str,
    path: &str,
    key: &str,
    etag: Option<&str>,
    body: Value,
    status: u16,
) -> Response<Vec<u8>> {
    let mut headers = HeaderMap::new();
    headers.insert("content-type", HeaderValue::from_static("application/json"));
    if !key.is_empty() {
        headers.insert("idempotency-key", key.parse().unwrap());
    }
    if let Some(etag) = etag {
        headers.insert("if-match", etag.parse().unwrap());
    }
    let bytes = if method == "GET" {
        vec![]
    } else {
        serde_json::to_vec(&body).unwrap()
    };
    let result = client.raw(method, path, headers, bytes).await.unwrap();
    assert_eq!(result.status, status, "{method} {path}");
    result
}
async fn workflow(origin: &str) {
    let admin = Client::new(config(origin, Some("admin"))).unwrap();
    let reviewer = Client::new(config(origin, Some("reviewer"))).unwrap();
    let reader = Client::new(config(origin, Some("reader"))).unwrap();
    let other = Client::new(config(origin, Some("other"))).unwrap();
    let mut namespaces = Vec::new();
    for i in 0..2 {
        let result = admin
            .create_namespace(
                format!("rust-ns-{i}"),
                CreateNamespace::new(format!("Rust {i}"), format!("rust{i}")),
            )
            .await
            .unwrap();
        assert_eq!(result.status, 201);
        assert!(result.headers.contains_key("x-request-id"));
        namespaces.push((
            result.data.unwrap().id.to_string(),
            result.headers["etag"].to_str().unwrap().to_owned(),
        ));
    }
    let (ns, old_etag) = &namespaces[0];
    let patch = json!({"displayName":"Updated Rust", "reason":"SDK qualification"});
    let first = admin
        .update_namespace(ns, "rust-update", old_etag, typed(patch.clone()))
        .await
        .unwrap();
    let replay = admin
        .update_namespace(ns, "rust-update", old_etag, typed(patch.clone()))
        .await
        .unwrap();
    assert_eq!(replay.headers["idempotency-replayed"], "true");
    assert_eq!(first.raw_body, replay.raw_body);
    assert_eq!(first.headers["etag"], replay.headers["etag"]);
    match admin
        .update_namespace(ns, "rust-stale", old_etag, typed(patch.clone()))
        .await
        .unwrap_err()
    {
        ApiOpError::Api(e) => assert_eq!(e.status, 412),
        _ => panic!("Expected stale revision"),
    }
    match admin
        .update_namespace(
            ns,
            "rust-update",
            old_etag,
            typed(json!({"description":"changed","reason":"different"})),
        )
        .await
        .unwrap_err()
    {
        ApiOpError::Api(e) => assert_eq!(e.status, 409),
        _ => panic!("Expected key conflict"),
    }
    let exact = admin
        .list_namespaces(Some(1), None::<&str>, Some(" RUST0 "), Some("Updated Rust"))
        .await
        .unwrap()
        .data
        .unwrap();
    assert_eq!(exact.items.len(), 1);
    assert_eq!(exact.items[0].id.to_string(), *ns);
    assert!(exact.next_cursor.is_none());
    let page = admin
        .list_namespaces(Some(1), None::<&str>, None::<&str>, None::<&str>)
        .await
        .unwrap()
        .data
        .unwrap();
    assert_eq!(page.items.len(), 1);
    let second = admin
        .list_namespaces(Some(1), page.next_cursor, None::<&str>, None::<&str>)
        .await
        .unwrap()
        .data
        .unwrap();
    assert_eq!(second.items.len(), 1);
    assert!(second.next_cursor.is_none());
    assert_ne!(page.items[0].id, second.items[0].id);
    let mut resource = String::new();
    for kind in ["agent", "mcp_server", "skill", "plugin"] {
        let result = admin.create_resource(format!("rust-{kind}"),typed(json!({"namespaceId":ns,"kind":kind,"canonicalIdentifier":format!("urn:air:catalog.example.com:rust:{kind}")}))).await.unwrap();
        let item = data(result, 201);
        if kind == "agent" {
            resource = item["id"].as_str().unwrap().to_owned();
        }
    }
    let native = json!({"role":"primary","mediaType":"application/a2a-agent-card+json","data":{
        "name":"Rust Agent","description":"Exact evidence","version":"1.0.0",
        "supportedInterfaces":[{"url":"https://agents.example.com/rust","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],
        "capabilities":{"extensions":[{"uri":"https://example.com/rust","params":{"large":9007199254740993u64,"nested":[null,false,""]}}]},
        "defaultInputModes":["application/json"],"defaultOutputModes":["application/json"],
        "skills":[{"id":"rust","name":"Rust","description":"Evidence","tags":["sdk"]}]}});
    let version = data(
        admin
            .create_resource_version(
                &resource,
                "rust-version",
                typed(json!({"version":"1.0.0","representations":[native.clone()]})),
            )
            .await
            .unwrap(),
        201,
    );
    assert_eq!(version["evidence"]["state"], "complete");
    let exact = call(
        &admin,
        "GET",
        &format!(
            "/api/v1/version-revisions/{}",
            version["revision"]["id"].as_str().unwrap()
        ),
        "",
        None,
        Value::Null,
        200,
    )
    .await;
    assert!(String::from_utf8_lossy(&exact.raw_body).contains("9007199254740993"));
    let retained: RevisionEvidenceEnvelope = serde_json::from_slice(&exact.raw_body).unwrap();
    assert_eq!(retained.namespace_id.to_string(), *ns);
    // Exercise the generated reusable-file binding against retained evidence.
    let revision_id = version["revision"]["id"].as_str().unwrap();
    let exported = admin
        .export_version_revision_draft(revision_id, None::<&str>)
        .await
        .unwrap_or_else(|error| match error {
            ApiOpError::Api(e) => panic!("Draft export HTTP {}: {}", e.status, e.body),
            other => panic!("Draft export failed: {other}"),
        });
    assert_eq!(exported.status, 200);
    assert!(exported.headers.contains_key("x-request-id"));
    let draft = exported.data.unwrap();
    let document = serde_json::to_value(&draft).unwrap();
    assert_eq!(document["format"], "open-agentic-catalog-draft-v1");
    assert_eq!(document["kind"], "agent");
    assert_eq!(document["version"], "1.0.0");
    assert_eq!(draft.representations.len(), 1);
    assert_eq!(
        draft.canonical_identifier,
        "urn:air:catalog.example.com:rust:agent"
    );
    // The text preserves exact native numbers; export does not fetch the URL.
    assert!(String::from_utf8_lossy(&exported.raw_body).contains("9007199254740993"));
    match other
        .export_version_revision_draft(revision_id, None::<&str>)
        .await
        .unwrap_err()
    {
        ApiOpError::Api(e) => assert_eq!(e.status, 404),
        _ => panic!("Expected cross-tenant export denial"),
    }
    call(
        &reader,
        "POST",
        "/api/v1/namespaces",
        "rust-denied",
        None,
        json!({"slug":"denied","displayName":"Denied"}),
        403,
    )
    .await;
    let denied = call(
        &reader,
        "PATCH",
        &format!("/api/v1/namespaces/{ns}"),
        "rust-update",
        Some(old_etag),
        patch,
        403,
    )
    .await;
    assert!(!denied.headers.contains_key("idempotency-replayed"));
    let readback = admin.get_namespace(ns).await.unwrap();
    assert_eq!(readback.raw_body, first.raw_body);
    let uncertain = json!({"description":"Recovered uncertain write","reason":"SDK qualification"});
    let original_validator = first.headers["etag"].to_str().unwrap();
    let mut headers = HeaderMap::new();
    headers.insert("content-type", HeaderValue::from_static("application/json"));
    headers.insert(
        "idempotency-key",
        HeaderValue::from_static("rust-uncertain"),
    );
    headers.insert("if-match", original_validator.parse().unwrap());
    assert!(matches!(
        admin
            .raw(
                "PATCH",
                &format!("/api/v1/namespaces/{ns}"),
                headers,
                serde_json::to_vec(&uncertain).unwrap()
            )
            .await,
        Err(Error::RequestFailed)
    ));
    let recovered = admin.get_namespace(ns).await.unwrap();
    let recovered_data = serde_json::to_value(recovered.data.as_ref().unwrap()).unwrap();
    assert_eq!(recovered_data["description"], "Recovered uncertain write");
    assert_eq!(recovered_data["revision"], 3);
    let explicit_replay = admin
        .update_namespace(ns, "rust-uncertain", original_validator, typed(uncertain))
        .await
        .unwrap();
    assert_eq!(explicit_replay.headers["idempotency-replayed"], "true");
    assert_eq!(explicit_replay.raw_body, recovered.raw_body);

    let scoped = Client::new(config(origin, Some("scoped"))).unwrap();
    let identity = data(
        scoped
            .get_current_access(None::<&str>, None, None::<&str>)
            .await
            .unwrap(),
        200,
    );
    let roles = call(
        &admin,
        "GET",
        "/api/v1/access/roles?limit=100",
        "",
        None,
        Value::Null,
        200,
    )
    .await
    .json::<Value>()
    .unwrap();
    let role = roles["items"]
        .as_array()
        .unwrap()
        .iter()
        .find(|r| r["key"] == "reader" && r["builtin"] == true)
        .unwrap();
    let grant = admin.grant_access_assignment("rust-scoped-grant",typed(json!({
        "roleDefinitionId":role["id"],"membershipId":identity["membershipId"],"namespaceId":ns,"reason":"SDK qualification"
    }))).await.unwrap();
    let validator = grant.headers["etag"].to_str().unwrap().to_owned();
    let assignment = data(grant, 201);
    let visible = scoped
        .list_namespaces(Some(100), None::<&str>, None::<&str>, None::<&str>)
        .await
        .unwrap()
        .data
        .unwrap();
    assert_eq!(visible.items.len(), 1);
    assert_eq!(visible.items[0].id.to_string(), *ns);
    admin
        .revoke_access_assignment(
            assignment["id"].as_str().unwrap(),
            "rust-scoped-revoke",
            validator,
            typed(json!({"reason":"SDK qualification"})),
        )
        .await
        .unwrap();
    let revoked = scoped
        .list_namespaces(Some(100), None::<&str>, None::<&str>, None::<&str>)
        .await
        .unwrap()
        .data
        .unwrap();
    assert!(revoked.items.is_empty());
    match scoped.get_namespace(ns).await.unwrap_err() {
        ApiOpError::Api(e) => assert_eq!(e.status, 403),
        _ => panic!("Expected current grant refusal"),
    }
    let after = admin
        .list_namespaces(None, None::<&str>, None::<&str>, None::<&str>)
        .await
        .unwrap()
        .data
        .unwrap();
    assert_eq!(after.items.len(), 2);
    call(
        &other,
        "GET",
        &format!("/api/v1/namespaces/{ns}"),
        "",
        None,
        Value::Null,
        404,
    )
    .await;
    let anonymous = Client::new(config(origin, None)).unwrap();
    call(
        &anonymous,
        "GET",
        "/api/v1/namespaces",
        "",
        None,
        Value::Null,
        401,
    )
    .await;
    let job = call(
        &admin,
        "POST",
        "/api/v1/jobs/00000000-0000-7000-8000-000000000090:retry",
        "rust-job-retry",
        None,
        json!({"reason":"SDK qualification"}),
        202,
    )
    .await;
    let job_id = job.json::<Value>().unwrap()["id"]
        .as_str()
        .unwrap()
        .to_owned();
    let queued = data(admin.get_job(&job_id).await.unwrap(), 200);
    assert_eq!(queued["state"], "queued");
    call(
        &other,
        "GET",
        &format!("/api/v1/jobs/{job_id}"),
        "",
        None,
        Value::Null,
        404,
    )
    .await;

    let publisher = call(
        &admin,
        "POST",
        "/api/v1/publishers",
        "rust-publisher",
        None,
        json!({"displayName":"Rust Publisher","domains":["catalog.example.com"]}),
        201,
    )
    .await;
    let publisher_id = publisher.json::<Value>().unwrap()["id"]
        .as_str()
        .unwrap()
        .to_owned();
    let exact_publisher = admin
        .list_publishers(
            Some(1),
            None::<&str>,
            Some("Rust Publisher"),
            Some(" CATALOG.Example.COM. "),
        )
        .await
        .unwrap()
        .data
        .unwrap();
    assert_eq!(exact_publisher.items.len(), 1);
    assert_eq!(exact_publisher.items[0].id.to_string(), publisher_id);
    assert!(exact_publisher.next_cursor.is_none());
    call(
        &admin,
        "POST",
        &format!(
            "/api/v1/publishers/{publisher_id}/publication-domains/catalog.example.com:approve"
        ),
        "rust-domain",
        Some(publisher.headers["etag"].to_str().unwrap()),
        json!({"reason":"SDK fixture"}),
        200,
    )
    .await;
    let extensions = json!({"large":9007199254740993u64,"null":null,"values":[null,false,""],"additional_properties":{"must":"stay nested"},"model_extra":"wire value"});
    let mut releases = Vec::new();
    for i in 0..2 {
        let (resource_id, version_id) = if i == 0 {
            (
                resource.clone(),
                version["revision"]["id"].as_str().unwrap().to_owned(),
            )
        } else {
            let result = data(admin.create_resource("rust-second-agent",typed(json!({"namespaceId":ns,"kind":"agent","canonicalIdentifier":"urn:air:catalog.example.com:rust:second"}))).await.unwrap(),201);
            let id = result["id"].as_str().unwrap().to_owned();
            let version = data(
                admin
                    .create_resource_version(
                        &id,
                        "rust-second-version",
                        typed(json!({"version":"1.0.0","representations":[native.clone()]})),
                    )
                    .await
                    .unwrap(),
                201,
            );
            (id, version["revision"]["id"].as_str().unwrap().to_owned())
        };
        let listing = admin
            .create_catalog_listing(
                format!("rust-listing-{i}"),
                typed(json!({"namespaceId":ns,"resourceId":resource_id})),
            )
            .await
            .unwrap();
        let listing_etag = listing.headers["etag"].to_str().unwrap().to_owned();
        let listing_id = data(listing, 201)["id"].as_str().unwrap().to_owned();
        let revision = admin.create_listing_revision(&listing_id,format!("rust-listing-revision-{i}"),listing_etag,typed(json!({"versionRevisionId":version_id,"displayName":format!("Rust Agent {i}"),"description":"SDK publication","publisherId":publisher_id,"visibility":"tenant","tags":["rust-sdk"],"extensions":extensions.clone()}))).await.unwrap();
        assert_eq!(revision.status, 201);
        let submission = call(
            &admin,
            "POST",
            &format!("/api/v1/catalog-listings/{listing_id}/submissions"),
            &format!("rust-submit-{i}"),
            None,
            json!({}),
            201,
        )
        .await;
        let submission_id = submission.json::<Value>().unwrap()["id"]
            .as_str()
            .unwrap()
            .to_owned();
        call(
            &reviewer,
            "POST",
            &format!("/api/v1/review-submissions/{submission_id}:approve"),
            &format!("rust-approve-{i}"),
            Some(submission.headers["etag"].to_str().unwrap()),
            json!({"reason":"Reviewed Rust evidence"}),
            200,
        )
        .await;
        let current = call(
            &admin,
            "GET",
            &format!("/api/v1/catalog-listings/{listing_id}"),
            "",
            None,
            Value::Null,
            200,
        )
        .await;
        let published = call(
            &admin,
            "POST",
            &format!("/api/v1/catalog-listings/{listing_id}:publish"),
            &format!("rust-publish-{i}"),
            Some(current.headers["etag"].to_str().unwrap()),
            json!({"submissionId":submission_id}),
            201,
        )
        .await;
        releases.push(
            published.json::<Value>().unwrap()["release"]["id"]
                .as_str()
                .unwrap()
                .to_owned(),
        );
    }
    let mut cursor: Option<String> = None;
    let mut seen = Vec::new();
    for i in 0..2 {
        let page = data(
            reader
                .search_catalog(typed(
                    json!({"limit":1,"tags":["rust-sdk"],"origins":["local"],"cursor":cursor})
                        .as_object()
                        .unwrap()
                        .iter()
                        .filter(|(_, v)| !v.is_null())
                        .map(|(k, v)| (k.clone(), v.clone()))
                        .collect::<serde_json::Map<_, _>>()
                        .into(),
                ))
                .await
                .unwrap(),
            200,
        );
        assert_eq!(page["items"].as_array().unwrap().len(), 1);
        assert_eq!(page["partial"], false);
        let item = &page["items"][0];
        assert_eq!(item["type"], "local");
        assert_eq!(item["kind"], "agent");
        let id = item["local"]["release"]["id"].as_str().unwrap().to_owned();
        assert!(releases.contains(&id) && !seen.contains(&id));
        let record = reader.get_catalog_record(&id).await.unwrap();
        assert!(String::from_utf8_lossy(&record.raw_body).contains("9007199254740993"));
        let record = data(record, 200);
        assert_eq!(record["listingRevision"]["extensions"], extensions);
        seen.push(id);
        cursor = page["nextCursor"].as_str().map(str::to_owned);
        assert_eq!(cursor.is_some(), i == 0);
    }
    assert_eq!(seen.len(), 2);
    let first = call(
        &reader,
        "GET",
        &format!("/api/v1/catalog/resources?namespaceId={ns}&kind=agent&limit=1"),
        "",
        None,
        Value::Null,
        200,
    )
    .await;
    let next = first.json::<Value>().unwrap()["nextCursor"]
        .as_str()
        .unwrap()
        .to_owned();
    let second = call(
        &reader,
        "GET",
        &format!("/api/v1/catalog/resources?namespaceId={ns}&kind=agent&limit=1&cursor={next}"),
        "",
        None,
        Value::Null,
        200,
    )
    .await;
    assert!(second.json::<Value>().unwrap().get("nextCursor").is_none());
    call(
        &reader,
        "GET",
        &format!("/api/v1/catalog/resources?namespaceId={ns}&kind=skill&limit=1&cursor={next}"),
        "",
        None,
        Value::Null,
        400,
    )
    .await;
    call(
        &other,
        "GET",
        &format!("/api/v1/catalog/resources/{}", releases[0]),
        "",
        None,
        Value::Null,
        404,
    )
    .await;
    call(
        &other,
        "GET",
        &format!("/api/v1/catalog/resources?namespaceId={ns}&kind=agent&limit=1&cursor={next}"),
        "",
        None,
        Value::Null,
        400,
    )
    .await;
    let audit = call(
        &admin,
        "GET",
        "/api/v1/audit-events?action=resource.created&limit=1",
        "",
        None,
        Value::Null,
        200,
    )
    .await
    .json::<Value>()
    .unwrap();
    assert_eq!(audit["items"].as_array().unwrap().len(), 1);
    assert_eq!(
        audit["items"][0]["details"]["principalType"],
        "catalog_membership"
    );
    let next = audit["nextCursor"].as_str().unwrap();
    let second = call(
        &admin,
        "GET",
        &format!("/api/v1/audit-events?action=resource.created&limit=1&cursor={next}"),
        "",
        None,
        Value::Null,
        200,
    )
    .await
    .json::<Value>()
    .unwrap();
    assert_ne!(audit["items"][0]["id"], second["items"][0]["id"]);
    qualify_reviewed_corpus().await;
    println!(
        "Rust Catalog workflow passed: four kinds, exact evidence, governed publication, Search/Browse/audit continuation, replay/conflicts, current authorization, tenant isolation and private Jobs"
    );
}

// The shared fixture supplies source expectations before Catalog admission.
async fn qualify_reviewed_corpus() {
    let corpus: Value =
        serde_json::from_slice(&fs::read(env::var("CATALOG_SDK_CORPUS").unwrap()).unwrap())
            .unwrap();
    let origin = corpus["origin"].as_str().unwrap();
    let reader = Client::new(config(origin, Some("reader"))).unwrap();
    let other = Client::new(config(origin, Some("other"))).unwrap();
    let cases = corpus["cases"].as_array().unwrap();
    assert_eq!(cases.len(), 6);
    for expected in cases {
        let id = expected["releaseId"].as_str().unwrap();
        let record = data(reader.get_catalog_record(id).await.unwrap(), 200);
        assert_eq!(record["resource"]["kind"], expected["kind"]);
        assert_eq!(
            record["resource"]["canonicalIdentifier"],
            expected["identifier"]
        );
        assert_eq!(record["release"]["id"], expected["releaseId"]);
        let representations = record["versionRevision"]["representations"]
            .as_array()
            .unwrap();
        assert_eq!(representations.len(), 1);
        let rep = &representations[0];
        for (key, value) in expected["representation"].as_object().unwrap() {
            assert_eq!(&rep[key], value, "{}: {key}", expected["name"]);
        }
        if let Some(source) = expected.get("data") {
            assert_eq!(&rep["data"], source);
        }
        if let Some(description) = expected.get("description") {
            assert_eq!(&rep["normalizedFacts"]["description"], description);
        }
        if let Some(text) = expected.get("text") {
            let artifact = reader
                .get_artifact_content(rep["artifactId"].as_str().unwrap())
                .await
                .unwrap();
            assert_eq!(artifact.status, 200);
            assert_eq!(artifact.raw_body, text.as_str().unwrap().as_bytes());
        }
        if let Some(count) = expected.get("fileCount") {
            let key = if expected["kind"] == "plugin" {
                "plugin"
            } else {
                "package"
            };
            assert_eq!(
                rep[key]["files"].as_array().unwrap().len() as u64,
                count.as_u64().unwrap()
            );
        }
        if let Some(documents) = expected.get("documents") {
            let actual = rep["plugin"]["documents"].as_array().unwrap();
            assert_eq!(actual.len(), documents.as_object().unwrap().len());
            let projected: serde_json::Map<String, Value> = actual
                .iter()
                .map(|d| (d["path"].as_str().unwrap().to_owned(), d["content"].clone()))
                .collect();
            assert_eq!(&Value::Object(projected), documents);
        }
        match other.get_catalog_record(id).await.unwrap_err() {
            ApiOpError::Api(error) => assert_eq!(error.status, 404),
            _ => panic!("Expected cross-tenant refusal"),
        }
    }
    println!(
        "Rust installed SDK: six reviewed upstream cases, exact native content and tenant denial passed"
    );
}

async fn transport(origin: &str, ca: &str) {
    // Public ARD reads need no credential. This separate client must not inherit
    // the authenticated client's identity or issue any follow-up resource fetch.
    let mut anonymous = config(origin, None);
    anonymous.root_certificates.push(fs::read(ca).unwrap());
    let anonymous = Client::new(anonymous).unwrap();
    let manifest = anonymous
        .get_ard_manifest(None::<&str>, None::<&str>)
        .await
        .unwrap();
    assert_eq!(manifest.headers["etag"], "\"anonymous-snapshot\"");
    let manifest = data(manifest, 200);
    assert_eq!(manifest["entries"].as_array().unwrap().len(), 1);
    assert_eq!(manifest["entries"][0]["metadata"]["license"], "Apache-2.0");
    let unchanged = anonymous
        .get_ard_manifest(Some("\"anonymous-snapshot\""), None::<&str>)
        .await
        .unwrap();
    assert_eq!(unchanged.status, 304);
    assert!(unchanged.data.is_none() && unchanged.raw_body.is_empty());
    let query =
        json!({"query":{"text":"Resolve a disputed invoice"},"pageSize":1,"federation":"none"});
    let first = anonymous
        .search_ard_registry(typed(query.clone()))
        .await
        .unwrap();
    let first = data(first, 200);
    assert_eq!(first["results"].as_array().unwrap().len(), 1);
    assert_eq!(first["results"][0]["score"], 37);
    assert_eq!(
        first["results"][0]["x-extension"]["number"],
        9007199254740993u64
    );
    let mut continued = query;
    continued["pageToken"] = first["pageToken"].clone();
    let last = anonymous
        .search_ard_registry(typed(continued))
        .await
        .unwrap();
    assert_eq!(data(last, 200), json!({"results":[]}));
    let facets = anonymous
        .explore_ard_registry(typed(
            json!({"resultType":{"facets":[{"field":"tags","minCount":0}]}}),
        ))
        .await
        .unwrap();
    assert_eq!(
        data(facets, 200),
        json!({"resultType":"facets","facets":{"tags":{"buckets":[]}}})
    );
    match anonymous
        .search_ard_registry(typed(json!({"query":{"text":"refuse"}})))
        .await
        .unwrap_err()
    {
        ApiOpError::Api(error) => {
            assert_eq!(error.status, 400);
            assert!(error.parse_error.is_none());
            match error.typed.as_ref().unwrap() {
                SearchArdRegistryApiError::Default(problem) => {
                    assert_eq!(problem.error_code, "INVALID_ARGUMENT");
                    assert_eq!(problem.message, "Unsupported filter");
                }
            }
            assert_eq!(
                serde_json::from_slice::<Value>(&error.raw_body).unwrap(),
                json!({"errorCode":"INVALID_ARGUMENT","message":"Unsupported filter"})
            );
            assert!(!format!("{error:?} {error}").contains("Unsupported filter"));
        }
        _ => panic!("Expected typed ARD refusal, not a Control problem"),
    }
    let mut setup = config(origin, Some("synthetic-transport-token"));
    setup.root_certificates.push(fs::read(ca).unwrap());
    let client = Client::new(setup).unwrap();
    let get = |path: &'static str| {
        let client = client.clone();
        async move { client.raw("GET", path, HeaderMap::new(), vec![]).await }
    };
    let good = get("/success").await.unwrap();
    assert_eq!(good.status, 200);
    assert_eq!(good.raw_body, b"exact-success");
    assert_eq!(good.headers["etag"], "\"transport\"");
    assert_eq!(get("/truncated").await.unwrap_err(), Error::RequestFailed);
    assert_eq!(get("/success").await.unwrap().raw_body, b"exact-success");
    assert_eq!(get("/redirect").await.unwrap().status, 302);
    let retry = get("/unavailable").await.unwrap();
    assert_eq!(retry.status, 503);
    assert_eq!(retry.headers["retry-after"], "7");
    let limited = get("/rate-limited").await.unwrap();
    assert_eq!(limited.status, 429);
    assert_eq!(limited.headers["retry-after"], "5");
    let small = client
        .with_limits(Limits {
            response_bytes: 8,
            ..Limits::default()
        })
        .unwrap();
    for path in ["/too-large", "/chunked"] {
        assert_eq!(
            small
                .raw("GET", path, HeaderMap::new(), vec![])
                .await
                .unwrap_err(),
            Error::ResponseTooLarge
        );
    }
    assert_eq!(
        small
            .raw("GET", "/exact", HeaderMap::new(), vec![])
            .await
            .unwrap()
            .raw_body,
        b"12345678"
    );
    assert_eq!(
        get("/compressed").await.unwrap_err(),
        Error::UnsupportedEncoding
    );
    let headers = client
        .with_limits(Limits {
            header_bytes: 80,
            ..Limits::default()
        })
        .unwrap();
    assert_eq!(
        headers
            .raw("GET", "/headers", HeaderMap::new(), vec![])
            .await
            .unwrap_err(),
        Error::HeadersTooLarge
    );
    let slow = client
        .with_limits(Limits {
            timeout: Duration::from_millis(100),
            ..Limits::default()
        })
        .unwrap();
    assert_eq!(
        slow.raw("GET", "/slow", HeaderMap::new(), vec![])
            .await
            .unwrap_err(),
        Error::Timeout
    );
    let cancel_client = client.clone();
    let task = tokio::spawn(async move {
        cancel_client
            .raw("GET", "/cancel", HeaderMap::new(), vec![])
            .await
    });
    let deadline = tokio::time::Instant::now() + Duration::from_secs(3);
    loop {
        if get("/started").await.unwrap().raw_body == b"yes" {
            break;
        }
        assert!(tokio::time::Instant::now() < deadline);
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    task.abort();
    assert!(task.await.unwrap_err().is_cancelled());
    let mut key = HeaderMap::new();
    key.insert("idempotency-key", HeaderValue::from_static("lost-write"));
    assert_eq!(
        client
            .raw("POST", "/lost", key.clone(), b"exact-command".to_vec())
            .await
            .unwrap_err(),
        Error::RequestFailed
    );
    assert_eq!(get("/lost-count").await.unwrap().raw_body, b"1");
    assert_eq!(
        client
            .raw("POST", "/lost", key, b"exact-command".to_vec())
            .await
            .unwrap()
            .raw_body,
        b"committed"
    );
    assert_eq!(get("/lost-count").await.unwrap().raw_body, b"1");
    let untrusted = Client::new(config(origin, Some("must-not-arrive"))).unwrap();
    assert_eq!(
        untrusted
            .raw("GET", "/must-not-arrive", HeaderMap::new(), vec![])
            .await
            .unwrap_err(),
        Error::RequestFailed
    );
    let mut wrong = config(
        &origin.replace("127.0.0.1", "localhost"),
        Some("must-not-arrive"),
    );
    wrong.root_certificates.push(fs::read(ca).unwrap());
    assert_eq!(
        Client::new(wrong)
            .unwrap()
            .raw("GET", "/must-not-arrive", HeaderMap::new(), vec![])
            .await
            .unwrap_err(),
        Error::RequestFailed
    );
    println!("Rust real TLS/HTTP transport controls passed");
}

// The Go driver validates both sides against the unchanged OpenAPI schema.
fn codec(schema: &str, wire: &str) {
    fn roundtrip<T: DeserializeOwned + Serialize>(wire: &str) {
        let decoded: T = serde_json::from_str(wire).unwrap();
        println!("{}", serde_json::to_string(&decoded).unwrap());
    }
    match schema {
        "CreateNamespace" => roundtrip::<CreateNamespace>(wire),
        "CurrentAccess" => roundtrip::<CurrentAccess>(wire),
        "PersonalInput" => roundtrip::<PersonalInput>(wire),
        "PersonalResolution" => roundtrip::<PersonalResolution>(wire),
        "RepresentationInput" => roundtrip::<RepresentationInput>(wire),
        "TrustAssessmentRequest" => roundtrip::<TrustAssessmentRequest>(wire),
        "ARDEntry" => roundtrip::<ARDEntry>(wire),
        "CatalogSearchResult" => roundtrip::<CatalogSearchResult>(wire),
        _ => panic!("Unselected codec"),
    }
}

#[tokio::main]
async fn main() {
    let args: Vec<_> = env::args().collect();
    match args.get(1).map(String::as_str) {
        Some("workflow") => workflow(&args[2]).await,
        Some("codec") => codec(&args[2], &args[3]),
        Some("transport") => transport(&args[2], &args[3]).await,
        _ => panic!("Expected an owned fixture mode"),
    }
}
