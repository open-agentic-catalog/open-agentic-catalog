//! Isolated provider cancellation and returned-header admission controls.

use super::*;

#[tokio::test(start_paused = true)]
async fn pending_provider_cancellation_releases_credentials_and_allows_fresh_calls() {
    for expires in [false, true] {
        let (late_token, waiting) = tokio::sync::oneshot::channel::<String>();
        let waiting = Arc::new(Mutex::new(Some(waiting)));
        let started = Arc::new(tokio::sync::Notify::new());
        let dropped = Arc::new(AtomicUsize::new(0));
        let calls = Arc::new(AtomicUsize::new(0));
        let transport = Scripted::new(vec![reply(200, b"recovered")]);
        let mut config = Config::new("https://catalog.example.com");
        config.limits.timeout = Duration::from_secs(10);
        config.token_provider = Some(Arc::new({
            let waiting = waiting.clone();
            let started = started.clone();
            let dropped = dropped.clone();
            let calls = calls.clone();
            move || {
                calls.fetch_add(1, Ordering::SeqCst);
                let receiver = waiting.lock().unwrap().take();
                let started = started.clone();
                let dropped = dropped.clone();
                Box::pin(async move {
                    if let Some(receiver) = receiver {
                        let _release = DropSignal(dropped);
                        started.notify_one();
                        receiver.await.map_err(|_| Error::CredentialFailed)
                    } else {
                        Ok("fresh-token".to_owned())
                    }
                })
            }
        }));
        let client = Client::with_transport(config, transport.clone()).unwrap();
        let pending_client = client.clone();
        let began = tokio::time::Instant::now();
        let mut pending = tokio::spawn(async move {
            pending_client
                .raw("GET", "/pending", HeaderMap::new(), vec![])
                .await
        });
        // Observe a polled provider that owns its receiver before cancellation.
        let acquisition = tokio::time::timeout(Duration::from_secs(1), async {
            tokio::select! {
                _ = started.notified() => {}
                result = &mut pending => panic!("request completed before provider acquisition: {result:?}"),
            }
        })
        .await;
        if acquisition.is_err() {
            pending.abort();
            let _ = pending.await;
            panic!("request did not enter credential acquisition");
        }
        assert_eq!(calls.load(Ordering::SeqCst), 1);
        assert_eq!(dropped.load(Ordering::SeqCst), 0);
        assert!(transport.requests.lock().unwrap().is_empty());

        if expires {
            tokio::time::advance(Duration::from_secs(9)).await;
            assert!(!pending.is_finished());
            tokio::time::advance(Duration::from_secs(1)).await;
            let completion = tokio::time::timeout(Duration::from_secs(1), &mut pending).await;
            let result = match completion {
                Ok(result) => result,
                Err(_) => {
                    pending.abort();
                    let _ = pending.await;
                    panic!("SDK deadline did not release the pending credential provider");
                }
            };
            assert_eq!(result.unwrap().unwrap_err(), Error::Timeout);
            assert_eq!(began.elapsed(), Duration::from_secs(10));
        } else {
            pending.abort();
            assert!(pending.await.unwrap_err().is_cancelled());
        }
        assert_eq!(dropped.load(Ordering::SeqCst), 1);
        assert_eq!(calls.load(Ordering::SeqCst), 1);
        assert!(transport.requests.lock().unwrap().is_empty());
        assert!(
            late_token
                .send("cancelled-provider-token".to_owned())
                .is_err()
        );

        let response = tokio::time::timeout(
            Duration::from_secs(1),
            client.raw("GET", "/recover", HeaderMap::new(), vec![]),
        )
        .await
        .expect("fresh credential call did not complete")
        .unwrap();
        assert_eq!(response.raw_body, b"recovered");
        assert_eq!(calls.load(Ordering::SeqCst), 2);
        assert_eq!(dropped.load(Ordering::SeqCst), 1);
        let requests = transport.requests.lock().unwrap();
        assert_eq!(requests.len(), 1);
        assert_eq!(requests[0].url().path(), "/recover");
        assert_eq!(requests[0].headers()["authorization"], "Bearer fresh-token");
    }
}

#[tokio::test]
async fn repeated_response_headers_count_each_complete_field_at_the_byte_boundary() {
    // Independent literal wire lines include each repeated name and CRLF.
    let exact_budget = b"x-tag: a\r\nx-tag: bc\r\n".len();
    for budget in [exact_budget, exact_budget - 1] {
        let mut headers = HeaderMap::new();
        headers.append("x-tag", HeaderValue::from_static("a"));
        headers.append("x-tag", HeaderValue::from_static("bc"));
        let transport = Scripted::new(vec![Ok(BufferedResponse {
            status: 200,
            headers,
            body: b"bounded".to_vec(),
        })]);
        let client = client(transport.clone())
            .with_limits(Limits {
                header_bytes: budget,
                ..Limits::default()
            })
            .unwrap();
        let response = client.raw("GET", "/", HeaderMap::new(), vec![]).await;
        if budget == exact_budget {
            let response = response.unwrap();
            assert_eq!(response.raw_body, b"bounded");
            let values: Vec<_> = response
                .headers
                .get_all("x-tag")
                .iter()
                .map(HeaderValue::as_bytes)
                .collect();
            assert_eq!(values, [b"a".as_slice(), b"bc".as_slice()]);
        } else {
            assert_eq!(response.unwrap_err(), Error::HeadersTooLarge);
        }
        assert_eq!(transport.requests.lock().unwrap().len(), 1);
    }
}

#[tokio::test]
async fn every_content_encoding_must_be_identity_regardless_of_case_or_order() {
    for (encodings, accepted) in [
        (["IdEnTiTy", "IDENTITY"], true),
        (["IdEnTiTy", "gzip"], false),
        (["gzip", "identity"], false),
    ] {
        let mut headers = HeaderMap::new();
        for encoding in encodings {
            headers.append("content-encoding", HeaderValue::from_static(encoding));
        }
        let transport = Scripted::new(vec![Ok(BufferedResponse {
            status: 200,
            headers,
            body: b"exact-response".to_vec(),
        })]);
        let response = client(transport.clone())
            .raw("GET", "/", HeaderMap::new(), vec![])
            .await;
        if accepted {
            let response = response.unwrap();
            assert_eq!(response.raw_body, b"exact-response");
            assert_eq!(
                response.headers.get_all("content-encoding").iter().count(),
                2
            );
        } else {
            assert_eq!(response.unwrap_err(), Error::UnsupportedEncoding);
        }
        assert_eq!(transport.requests.lock().unwrap().len(), 1);
    }
}
