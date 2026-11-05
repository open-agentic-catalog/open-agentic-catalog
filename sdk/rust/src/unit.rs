use crate::*;
use reqwest::header::{HeaderMap, HeaderValue};
use serde_json::{Value, json};
use std::{
    collections::VecDeque,
    sync::{
        Arc, Mutex,
        atomic::{AtomicUsize, Ordering},
    },
    time::Duration,
};

struct Scripted {
    replies: Mutex<VecDeque<Result<BufferedResponse, Error>>>,
    requests: Mutex<Vec<reqwest::Request>>,
}
impl Scripted {
    fn new(replies: Vec<Result<BufferedResponse, Error>>) -> Arc<Self> {
        Arc::new(Self {
            replies: Mutex::new(replies.into()),
            requests: Mutex::new(Vec::new()),
        })
    }
}
impl Transport for Scripted {
    fn send<'a>(
        &'a self,
        request: reqwest::Request,
        _: Limits,
    ) -> BoxFuture<'a, Result<BufferedResponse, Error>> {
        self.requests.lock().unwrap().push(request);
        Box::pin(async {
            self.replies
                .lock()
                .unwrap()
                .pop_front()
                .expect("unexpected transport call")
        })
    }
}
fn reply(status: u16, body: &[u8]) -> Result<BufferedResponse, Error> {
    Ok(BufferedResponse {
        status,
        headers: HeaderMap::new(),
        body: body.to_vec(),
    })
}
fn client(transport: Arc<Scripted>) -> Client {
    let mut config = Config::new("https://catalog.example.com");
    config.token = Some("synthetic-unit-token".to_owned());
    Client::with_transport(config, transport).unwrap()
}

#[test]
fn settings_validate_without_contact_or_secret_diagnostics() {
    for origin in [
        "",
        "https://user:secret@example.com",
        "https://example.com/path",
        "https://example.com?q=secret",
        "https://example.com#private",
        "ftp://example.com",
        "http://example.com",
        "http://localhost",
        " https://example.com",
    ] {
        let error = Client::new(Config::new(origin)).unwrap_err();
        assert_eq!(error, Error::InvalidConfig);
        assert!(!format!("{error:?} {error}").contains("secret"));
    }
    for origin in ["http://127.0.0.1:8080", "http://[::1]:8080"] {
        let mut config = Config::new(origin);
        config.allow_loopback_http = true;
        assert!(Client::new(config).is_ok());
    }
    for origin in [
        "http://127.1",
        "http://2130706433",
        "http://0x7f000001",
        "http://localhost",
        "https://@example.com",
        "https://example.com/../",
        "https://example.com/\n",
    ] {
        let mut config = Config::new(origin);
        config.allow_loopback_http = true;
        assert_eq!(Client::new(config).unwrap_err(), Error::InvalidConfig);
    }
    let mut config = Config::new("https://example.com");
    config.token = Some("secret".into());
    config.token_provider = Some(Arc::new(|| Box::pin(async { Ok("other".into()) })));
    assert_eq!(Client::new(config).unwrap_err(), Error::InvalidConfig);
    for token in [
        "line\nbreak".to_owned(),
        "x y".to_owned(),
        "\u{80}".to_owned(),
        "a".repeat((32 << 10) + 1),
    ] {
        let mut config = Config::new("https://example.com");
        config.token = Some(token);
        assert_eq!(Client::new(config).unwrap_err(), Error::InvalidCredential);
    }
    let mut config = Config::new("https://example.com");
    config.token = Some("redact-me".into());
    config
        .root_certificates
        .push(b"private malformed certificate".to_vec());
    assert!(!format!("{config:?}").contains("redact-me"));
    assert_eq!(Client::new(config).unwrap_err(), Error::InvalidConfig);
    let mut config = Config::new("https://example.com");
    config.root_certificates = vec![Vec::new(); 33];
    assert_eq!(Client::new(config).unwrap_err(), Error::InvalidConfig);
}

#[test]
fn finite_limits_and_immutable_call_overrides() {
    let client = client(Scripted::new(vec![]));
    for limits in [
        Limits {
            timeout: Duration::ZERO,
            ..Limits::default()
        },
        Limits {
            timeout: Duration::from_secs(86401),
            ..Limits::default()
        },
        Limits {
            request_bytes: 0,
            ..Limits::default()
        },
        Limits {
            response_bytes: (1 << 30) + 1,
            ..Limits::default()
        },
        Limits {
            header_bytes: 0,
            ..Limits::default()
        },
    ] {
        assert_eq!(
            client.with_limits(limits).unwrap_err(),
            Error::InvalidConfig
        );
    }
    let changed = client
        .with_limits(Limits {
            response_bytes: 1 << 30,
            ..Limits::default()
        })
        .unwrap();
    assert!(format!("{changed:?}").contains("1073741824"));
    assert!(format!("{client:?}").contains("8388608"));
    assert!(!format!("{client:?}").contains("synthetic-unit-token"));
}

#[tokio::test]
async fn request_validation_precedes_identity_and_transport() {
    let calls = Arc::new(AtomicUsize::new(0));
    let observed = calls.clone();
    let transport = Scripted::new(vec![]);
    let mut config = Config::new("https://example.com");
    config.limits.request_bytes = 3;
    config.token_provider = Some(Arc::new(move || {
        observed.fetch_add(1, Ordering::SeqCst);
        Box::pin(async { Ok("token".into()) })
    }));
    let client = Client::with_transport(config, transport.clone()).unwrap();
    assert_eq!(
        client
            .raw("POST", "/api/v1/namespaces", HeaderMap::new(), vec![0; 4])
            .await
            .unwrap_err(),
        Error::RequestTooLarge
    );
    for path in [
        "https://other.example/api",
        "//other.example/api",
        "/api#secret",
        "/\\other",
    ] {
        assert_eq!(
            client
                .raw("GET", path, HeaderMap::new(), vec![])
                .await
                .unwrap_err(),
            Error::InvalidRequest
        );
    }
    assert_eq!(
        client
            .raw("bad\nmethod", "/", HeaderMap::new(), vec![])
            .await
            .unwrap_err(),
        Error::InvalidRequest
    );
    for name in ["authorization", "cookie", "proxy-authorization", "host"] {
        let mut headers = HeaderMap::new();
        headers.insert(name, HeaderValue::from_static("forbidden"));
        assert_eq!(
            client.raw("GET", "/", headers, vec![]).await.unwrap_err(),
            Error::InvalidRequest
        );
    }
    assert_eq!(calls.load(Ordering::SeqCst), 0);
    assert!(transport.requests.lock().unwrap().is_empty());
}

#[tokio::test]
async fn generated_request_preserves_command_binding_and_response_metadata() {
    let body = br#"{"id":"00000000-0000-7000-8000-000000000001","tenantId":"00000000-0000-7000-8000-000000000002","slug":"unit","displayName":"Unit","revision":1,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}"#;
    let mut response = reply(201, body).unwrap();
    response
        .headers
        .insert("etag", HeaderValue::from_static("\"revision-1\""));
    response
        .headers
        .insert("x-request-id", HeaderValue::from_static("unit-request"));
    let transport = Scripted::new(vec![Ok(response)]);
    let result = client(transport.clone())
        .create_namespace(
            "unit-command",
            CreateNamespace::new("Unit".into(), "unit".into()),
        )
        .await
        .unwrap();
    assert_eq!(result.status, 201);
    assert_eq!(result.headers["etag"], "\"revision-1\"");
    assert_eq!(result.headers["x-request-id"], "unit-request");
    assert_eq!(result.raw_body, body);
    assert_eq!(result.data.unwrap().slug, "unit");
    let requests = transport.requests.lock().unwrap();
    assert_eq!(requests.len(), 1);
    let request = &requests[0];
    assert_eq!(request.method(), "POST");
    assert_eq!(
        request.url().as_str(),
        "https://catalog.example.com/api/v1/namespaces"
    );
    assert_eq!(request.headers()["idempotency-key"], "unit-command");
    assert_eq!(
        request.headers()["authorization"],
        "Bearer synthetic-unit-token"
    );
    assert_eq!(request.headers()["accept-encoding"], "identity");
    let body: Value = serde_json::from_slice(request.body().unwrap().as_bytes().unwrap()).unwrap();
    assert_eq!(body, json!({"slug":"unit", "displayName":"Unit"}));
}

#[tokio::test]
async fn response_refusals_are_bounded_and_never_retried() {
    for (reply, expected) in [
        (reply(200, b"1234"), Error::ResponseTooLarge),
        (reply(0, b""), Error::RequestFailed),
        (Err(Error::RequestFailed), Error::RequestFailed),
        (
            Ok(BufferedResponse {
                status: 200,
                headers: [("content-encoding", "gzip")]
                    .into_iter()
                    .map(|(k, v)| (k.parse().unwrap(), v.parse().unwrap()))
                    .collect(),
                body: vec![],
            }),
            Error::UnsupportedEncoding,
        ),
        (
            Ok(BufferedResponse {
                status: 200,
                headers: [("x-long", "123456789")]
                    .into_iter()
                    .map(|(k, v)| (k.parse().unwrap(), v.parse().unwrap()))
                    .collect(),
                body: vec![],
            }),
            Error::HeadersTooLarge,
        ),
        (
            Ok(BufferedResponse {
                status: 200,
                headers: [("content-length", "4")]
                    .into_iter()
                    .map(|(k, v)| (k.parse().unwrap(), v.parse().unwrap()))
                    .collect(),
                body: vec![],
            }),
            Error::ResponseTooLarge,
        ),
    ] {
        let transport = Scripted::new(vec![reply]);
        let limits = Limits {
            response_bytes: 3,
            header_bytes: if expected == Error::HeadersTooLarge {
                5
            } else {
                32768
            },
            ..Limits::default()
        };
        assert_eq!(
            client(transport.clone())
                .with_limits(limits)
                .unwrap()
                .raw("GET", "/api/v1/me", HeaderMap::new(), vec![])
                .await
                .unwrap_err(),
            expected
        );
        assert_eq!(transport.requests.lock().unwrap().len(), 1);
    }
}

#[tokio::test(start_paused = true)]
async fn credential_deadline_failure_and_invalid_output_prevent_transmission() {
    for kind in 0..3 {
        let transport = Scripted::new(vec![]);
        let mut config = Config::new("https://example.com");
        config.token_provider = Some(Arc::new(move || {
            Box::pin(async move {
                match kind {
                    0 => std::future::pending().await,
                    1 => Err(Error::RequestFailed),
                    _ => Ok("bad\nsecret".into()),
                }
            })
        }));
        let result = Client::with_transport(config, transport.clone())
            .unwrap()
            .raw("GET", "/", HeaderMap::new(), vec![])
            .await
            .unwrap_err();
        assert_eq!(
            result,
            [
                Error::Timeout,
                Error::CredentialFailed,
                Error::InvalidCredential
            ][kind]
        );
        assert!(transport.requests.lock().unwrap().is_empty());
    }
}

#[tokio::test]
async fn empty_results_errors_and_exact_raw_bytes_remain_inspectable() {
    let transport = Scripted::new(vec![reply(304,b""), reply(503,br#"{"type":"about:blank","title":"Unavailable","status":503,"code":"DEPENDENCY_UNAVAILABLE","detail":"secret-body","requestId":"unit-request"}"#), reply(200,b"not JSON secret"), reply(200,&[0,255,10])]);
    let client = client(transport.clone());
    let result = client
        .get_ard_manifest(Some("\"etag\""), None::<&str>)
        .await
        .unwrap();
    assert_eq!(result.status, 304);
    assert!(result.data.is_none());
    let error = client
        .get_current_access(None::<&str>, None, None::<&str>)
        .await
        .unwrap_err();
    assert!(!format!("{error:?} {error}").contains("secret-body"));
    match error {
        ApiOpError::Api(e) => {
            assert_eq!(e.status, 503);
            assert!(e.typed.is_some());
            assert!(e.body.contains("secret-body"));
        }
        _ => panic!("Expected HTTP error"),
    }
    let error = client
        .get_current_access(None::<&str>, None, None::<&str>)
        .await
        .unwrap_err();
    match error {
        ApiOpError::Api(e) => {
            assert_eq!(e.status, 200);
            assert!(e.parse_error.is_some());
            assert_eq!(e.raw_body, b"not JSON secret");
            assert!(!format!("{e}").contains("secret"));
        }
        _ => panic!("Expected decode evidence"),
    }
    let raw = client
        .raw(
            "GET",
            "/api/v1/artifacts/a/content",
            HeaderMap::new(),
            vec![],
        )
        .await
        .unwrap();
    assert_eq!(raw.raw_body, [0, 255, 10]);
    assert_eq!(raw.json::<Value>().unwrap_err(), Error::DecodeFailed);
    assert_eq!(transport.requests.lock().unwrap().len(), 4);
}

#[tokio::test]
async fn typed_json_rejects_invalid_utf8_without_repairing_evidence() {
    let valid = br#"{"entries":[{"identifier":"urn:air:example.com:a","displayName":"Valid","type":"application/json","data":{}}]}"#;
    // A literal replacement character is valid Unicode. An invalid byte inside
    // the same field must fail decoding, rather than silently becoming U+FFFD.
    let valid = String::from_utf8(valid.to_vec())
        .unwrap()
        .replace("Valid", "Valid \u{fffd} café");
    let mut invalid = valid.as_bytes().to_vec();
    invalid[valid.find("café").unwrap()] = 0xff;
    let problem = b"{\"errorCode\":\"DEPENDENCY_UNAVAILABLE\",\"message\":\"private \xff\"}";
    let transport = Scripted::new(vec![
        reply(200, valid.as_bytes()),
        reply(200, &invalid),
        reply(503, problem),
    ]);
    let client = client(transport.clone());
    let response = client
        .get_ard_manifest(None::<&str>, None::<&str>)
        .await
        .unwrap();
    assert_eq!(response.raw_body, valid.as_bytes());
    assert_eq!(
        serde_json::to_value(response.data.unwrap()).unwrap()["entries"][0]["displayName"],
        "Valid \u{fffd} café"
    );
    for (status, bytes) in [(200, invalid.as_slice()), (503, problem.as_slice())] {
        match client
            .get_ard_manifest(None::<&str>, None::<&str>)
            .await
            .unwrap_err()
        {
            ApiOpError::Api(error) => {
                assert_eq!(error.status, status);
                assert_eq!(error.raw_body, bytes);
                assert!(error.typed.is_none());
                assert!(error.parse_error.is_some());
                assert!(!format!("{error:?} {error}").contains("private"));
            }
            _ => panic!("Expected original response evidence"),
        }
    }
    assert_eq!(transport.requests.lock().unwrap().len(), 3);
}

#[test]
fn codecs_preserve_original_evidence_and_closed_control_boundaries() {
    let native = json!({"role":"primary", "mediaType":"application/a2a-agent-card+json", "data":{"number":9007199254740993u64,"nested":[null,false,""]}});
    let value: RepresentationInput = serde_json::from_value(native.clone()).unwrap();
    assert_eq!(serde_json::to_value(value).unwrap(), native);
    let wire = r#"{"identifier":"urn:air:example.com:agent","displayName":"Example","type":"application/a2a-agent-card+json","data":{},"@context":null,"x-extension":{"large":1234567890123456789012345678901234567890,"tiny":1e-400},"additional_properties":{"must":"stay nested"}}"#;
    let value: ARDEntry = serde_json::from_str(wire).unwrap();
    assert_eq!(
        serde_json::to_value(value).unwrap(),
        serde_json::from_str::<Value>(wire).unwrap()
    );
    let unknown = json!({"slug":"unit","displayName":"Unit","extra":"must reject"});
    assert!(serde_json::from_value::<CreateNamespace>(unknown).is_err());
    let missing = json!({"displayName":"Unit"});
    assert!(serde_json::from_value::<CreateNamespace>(missing).is_err());
    let patch: PatchNamespace =
        serde_json::from_value(json!({"reason":"Change","description":""})).unwrap();
    assert_eq!(
        serde_json::to_value(patch).unwrap(),
        json!({"reason":"Change","description":""})
    );
    let trust = json!({"sourceRevision":1,"identifier":"urn:air:example.com:a","versionPresent":false,"mediaType":"application/json","delivery":"data","exactEntryDigest":format!("sha256:{}","a".repeat(64)),"text":"Example","reason":"Check"});
    let value: TrustAssessmentRequest = serde_json::from_value(trust.clone()).unwrap();
    assert_eq!(serde_json::to_value(value).unwrap(), trust);
}

#[tokio::test]
async fn provider_is_evaluated_once_per_call_without_implicit_refresh_or_retry() {
    let tokens = Arc::new(Mutex::new(VecDeque::from(["first", "second"])));
    let observed = tokens.clone();
    let mut config = Config::new("https://example.com");
    config.token_provider = Some(Arc::new(move || {
        let token = observed
            .lock()
            .unwrap()
            .pop_front()
            .expect("unexpected credential acquisition")
            .to_owned();
        Box::pin(async { Ok(token) })
    }));
    let transport = Scripted::new(vec![reply(503, b"failure"), reply(200, b"recovered")]);
    let client = Client::with_transport(config, transport.clone()).unwrap();
    assert_eq!(
        client
            .raw("GET", "/api/v1/me", HeaderMap::new(), vec![])
            .await
            .unwrap()
            .status,
        503
    );
    assert_eq!(
        client
            .raw("GET", "/api/v1/me", HeaderMap::new(), vec![])
            .await
            .unwrap()
            .raw_body,
        b"recovered"
    );
    let requests = transport.requests.lock().unwrap();
    assert_eq!(requests.len(), 2);
    assert_eq!(requests[0].headers()["authorization"], "Bearer first");
    assert_eq!(requests[1].headers()["authorization"], "Bearer second");
    assert!(tokens.lock().unwrap().is_empty());
}

struct PendingTransport {
    dropped: Arc<AtomicUsize>,
    started: Arc<tokio::sync::Notify>,
}
struct DropSignal(Arc<AtomicUsize>);
impl Drop for DropSignal {
    fn drop(&mut self) {
        self.0.fetch_add(1, Ordering::SeqCst);
    }
}
impl Transport for PendingTransport {
    fn send<'a>(
        &'a self,
        _: reqwest::Request,
        _: Limits,
    ) -> BoxFuture<'a, Result<BufferedResponse, Error>> {
        Box::pin(async {
            let _signal = DropSignal(self.dropped.clone());
            self.started.notify_one();
            std::future::pending().await
        })
    }
}

#[tokio::test(start_paused = true)]
async fn credential_and_transport_share_one_deadline() {
    let credential_started = Arc::new(tokio::sync::Notify::new());
    let observed = credential_started.clone();
    let calls = Arc::new(AtomicUsize::new(0));
    let acquired = calls.clone();
    let transport_started = Arc::new(tokio::sync::Notify::new());
    let dropped = Arc::new(AtomicUsize::new(0));
    let mut config = Config::new("https://example.com");
    config.limits.timeout = Duration::from_secs(10);
    config.token_provider = Some(Arc::new(move || {
        acquired.fetch_add(1, Ordering::SeqCst);
        let observed = observed.clone();
        Box::pin(async move {
            observed.notify_one();
            tokio::time::sleep(Duration::from_secs(6)).await;
            Ok("shared-budget-token".into())
        })
    }));
    let client = Client::with_transport(
        config,
        Arc::new(PendingTransport {
            dropped: dropped.clone(),
            started: transport_started.clone(),
        }),
    )
    .unwrap();
    let start = tokio::time::Instant::now();
    let task = tokio::spawn(async move {
        client
            .raw("GET", "/api/v1/me", HeaderMap::new(), vec![])
            .await
    });
    credential_started.notified().await;
    tokio::time::advance(Duration::from_secs(6)).await;
    transport_started.notified().await;
    tokio::time::advance(Duration::from_secs(3)).await;
    assert!(!task.is_finished());
    tokio::time::advance(Duration::from_secs(1)).await;
    assert_eq!(task.await.unwrap().unwrap_err(), Error::Timeout);
    assert_eq!(start.elapsed(), Duration::from_secs(10));
    assert_eq!(dropped.load(Ordering::SeqCst), 1);
    assert_eq!(calls.load(Ordering::SeqCst), 1);
}

struct ConcurrentTransport {
    requests: Mutex<Vec<(reqwest::Request, Limits)>>,
    started: tokio::sync::Notify,
    finish: tokio::sync::Notify,
    dropped: Arc<AtomicUsize>,
}
impl Transport for ConcurrentTransport {
    fn send<'a>(
        &'a self,
        request: reqwest::Request,
        limits: Limits,
    ) -> BoxFuture<'a, Result<BufferedResponse, Error>> {
        let cancel = request.url().path() == "/cancel";
        let body = request.body().unwrap().as_bytes().unwrap().to_vec();
        self.requests.lock().unwrap().push((request, limits));
        Box::pin(async move {
            let _signal = DropSignal(self.dropped.clone());
            self.started.notify_one();
            if cancel {
                std::future::pending::<()>().await;
            }
            self.finish.notified().await;
            reply(200, &body)
        })
    }
}

#[tokio::test]
async fn concurrent_clones_keep_credentials_budgets_and_cancellation_separate() {
    let calls = Arc::new(AtomicUsize::new(0));
    let observed = calls.clone();
    let mut config = Config::new("https://example.com");
    config.token_provider = Some(Arc::new(move || {
        let sequence = observed.fetch_add(1, Ordering::SeqCst);
        Box::pin(async move { Ok(format!("token-{sequence}")) })
    }));
    let dropped = Arc::new(AtomicUsize::new(0));
    let transport = Arc::new(ConcurrentTransport {
        requests: Mutex::new(vec![]),
        started: tokio::sync::Notify::new(),
        finish: tokio::sync::Notify::new(),
        dropped: dropped.clone(),
    });
    let client = Client::with_transport(config, transport.clone()).unwrap();
    let narrow = client
        .with_limits(Limits {
            request_bytes: 3,
            response_bytes: 3,
            ..Limits::default()
        })
        .unwrap();
    let wide = client
        .with_limits(Limits {
            request_bytes: 6,
            response_bytes: 6,
            ..Limits::default()
        })
        .unwrap();
    let first = tokio::spawn(async move {
        narrow
            .raw("POST", "/cancel", HeaderMap::new(), b"one".to_vec())
            .await
    });
    transport.started.notified().await;
    let second = tokio::spawn(async move {
        wide.raw("POST", "/complete", HeaderMap::new(), b"second".to_vec())
            .await
    });
    transport.started.notified().await;
    {
        let requests = transport.requests.lock().unwrap();
        assert_eq!(requests.len(), 2);
        for (i, expected) in [
            (0, ("/cancel", "Bearer token-0", 3)),
            (1, ("/complete", "Bearer token-1", 6)),
        ] {
            let (request, limits) = &requests[i];
            assert_eq!(request.url().path(), expected.0);
            assert_eq!(request.headers()["authorization"], expected.1);
            assert!(request.headers()["authorization"].is_sensitive());
            assert_eq!(limits.request_bytes, expected.2);
            assert_eq!(limits.response_bytes, expected.2);
        }
    }
    assert!(!first.is_finished() && !second.is_finished());
    first.abort();
    assert!(first.await.unwrap_err().is_cancelled());
    assert_eq!(dropped.load(Ordering::SeqCst), 1);
    assert!(!second.is_finished());
    transport.finish.notify_one();
    assert_eq!(second.await.unwrap().unwrap().raw_body, b"second");
    assert_eq!(dropped.load(Ordering::SeqCst), 2);
    assert_eq!(calls.load(Ordering::SeqCst), 2);
}
#[tokio::test(start_paused = true)]
async fn transport_deadline_and_caller_cancellation_release_owned_work() {
    let dropped = Arc::new(AtomicUsize::new(0));
    let started = Arc::new(tokio::sync::Notify::new());
    let transport = Arc::new(PendingTransport {
        dropped: dropped.clone(),
        started: started.clone(),
    });
    let client = Client::with_transport(Config::new("https://example.com"), transport).unwrap();
    assert_eq!(
        client
            .raw("GET", "/api/v1/me", HeaderMap::new(), vec![])
            .await
            .unwrap_err(),
        Error::Timeout
    );
    assert_eq!(dropped.load(Ordering::SeqCst), 1);
    // Consume the first signal, then prove the next future entered its transport
    // before aborting it. No real timer, server or scheduler race is involved.
    started.notified().await;
    let task = tokio::spawn(async move {
        client
            .raw("GET", "/api/v1/me", HeaderMap::new(), vec![])
            .await
    });
    started.notified().await;
    task.abort();
    assert!(task.await.unwrap_err().is_cancelled());
    assert_eq!(dropped.load(Ordering::SeqCst), 2);
}

#[test]
fn indexed_live_and_skill_binary_codecs_preserve_exact_fields() {
    for kind in ["indexed", "live"] {
        let evidence = if kind == "indexed" {
            json!({"observationGroupId":format!("observation-group-v1:sha256:{}","a".repeat(64)),"normalizedEntryDigest":format!("sha256:{}","b".repeat(64)),"sourceIds":["00000000-0000-7000-8000-000000000001"],"sourceCount":1,"sourcesTruncated":false,"conflict":false,"sameSourceConflict":false})
        } else {
            json!({"sourceId":"00000000-0000-7000-8000-000000000001","source":"https://catalog.example/ard","entry":{"future":{"empty":[],"null":null,"enabled":false}}})
        };
        let mut wire = json!({"id":format!("{kind}:opaque"),"type":kind,"identifier":"urn:air:example.com:compat:plugin","versionPresent":false,"kind":"plugin","displayName":"Plugin","tags":[],"score":0});
        wire[kind] = evidence;
        let decoded: CatalogSearchResult = serde_json::from_value(wire.clone()).unwrap();
        assert_eq!(serde_json::to_value(decoded).unwrap(), wire);
    }
    let skill = json!({"role":"primary","mediaType":"application/agent-skills+zip","packageData":"AAEC/w==","handoff":{"git":{"repository":"https://example.com/skills.git","commit":"a".repeat(40),"directory":"skills/example"}}});
    let decoded: RepresentationInput = serde_json::from_value(skill.clone()).unwrap();
    assert_eq!(
        decoded.package_data.as_deref(),
        Some([0u8, 1, 2, 255].as_slice())
    );
    assert_eq!(serde_json::to_value(decoded).unwrap(), skill);
}

#[tokio::test]
async fn refusal_only_operations_reject_undeclared_success_statuses() {
    let transport = Scripted::new(vec![
        reply(200, b"private unexpected response"),
        reply(204, b""),
    ]);
    let client = client(transport.clone());
    let deletion = client
        .refuse_catalog_mcp_session_deletion()
        .await
        .unwrap_err();
    assert!(!format!("{deletion:?} {deletion}").contains("private"));
    match deletion {
        ApiOpError::Api(e) => {
            assert_eq!(e.status, 200);
            assert!(e.parse_error.is_some());
            assert_eq!(e.raw_body, b"private unexpected response");
        }
        _ => panic!("Expected undeclared-success evidence"),
    }
    match client.refuse_catalog_mcp_stream().await.unwrap_err() {
        ApiOpError::Api(e) => {
            assert_eq!(e.status, 204);
            assert!(e.parse_error.is_some());
        }
        _ => panic!("Expected undeclared-success evidence"),
    }
    let requests = transport.requests.lock().unwrap();
    assert_eq!(requests.len(), 2);
    assert_eq!(requests[0].method(), "DELETE");
    assert_eq!(requests[1].method(), "GET");
}
#[tokio::test]
async fn personal_label_query_preserves_literal_value_and_optional_presence() {
    let transport = Scripted::new(vec![
        reply(200, br#"{"items":[]}"#),
        reply(200, br#"{"items":[]}"#),
        reply(200, br#"{"items":[]}"#),
    ]);
    let client = client(transport.clone());
    for label in [None, Some(" Case é%_ "), Some("")] {
        let result = client
            .list_personal_items(None, label, None, None::<&str>)
            .await
            .unwrap();
        assert!(result.data.unwrap().items.is_empty());
        let requests = transport.requests.lock().unwrap();
        let pairs = requests
            .last()
            .unwrap()
            .url()
            .query_pairs()
            .collect::<Vec<_>>();
        assert_eq!(pairs.iter().any(|(key, _)| key == "label"), label.is_some());
        if let Some(expected) = label {
            assert_eq!(
                pairs.iter().find(|(key, _)| key == "label").unwrap().1,
                expected
            );
        }
    }
    assert_eq!(transport.requests.lock().unwrap().len(), 3);
}
