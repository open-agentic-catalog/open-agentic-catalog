use crate::report::{self, Options, Report, Row};
use std::{collections::BTreeSet, ffi::OsString};

fn parse(values: &[&str]) -> Result<Option<Options>, String> {
    Options::parse(values.iter().map(OsString::from))
}

fn options() -> Options {
    parse(&[
        "--origin",
        "https://catalog.example",
        "--token-file",
        "token",
    ])
    .unwrap()
    .unwrap()
}

#[test]
fn explicit_inputs_and_all_four_kind_values() {
    for kind in ["agent", "mcp_server", "skill", "plugin"] {
        let values = parse(&[
            "--origin",
            "http://127.0.0.1:8080",
            "--token-file",
            "private token",
            "--ca-file",
            "private ca",
            "--allow-loopback-http",
            "--query",
            "testing",
            "--kind",
            kind,
        ])
        .unwrap()
        .unwrap();
        assert!(values.allow_loopback_http);
        assert_eq!(values.token_file.to_str(), Some("private token"));
        assert_eq!(values.ca_file.unwrap().to_str(), Some("private ca"));
        assert_eq!(values.query.as_deref(), Some("testing"));
        assert_eq!(values.kind.unwrap().as_str(), kind);
    }
    assert!(options().kind.is_none());
    assert!(options().query.is_none());
    assert!(!options().allow_loopback_http);
}

#[test]
fn help_does_not_require_credentials() {
    assert!(parse(&["--help"]).unwrap().is_none());
    assert!(report::HELP.contains("--token-file"));
}

#[test]
fn input_errors_are_safe_and_reject_duplicates_and_unknown_flags() {
    for values in [
        vec![],
        vec!["--origin"],
        vec!["--token-file", "sensitive"],
        vec!["--secret-sensitive", "value"],
        vec![
            "--origin",
            "https://catalog.example",
            "--origin",
            "sensitive",
        ],
        vec!["--kind", "sensitive"],
        vec!["--query", "nul\0sensitive"],
        vec!["--help", "--secret-sensitive"],
        vec!["--allow-loopback-http", "--allow-loopback-http"],
    ] {
        let message = parse(&values).err().expect("input must fail");
        assert!(!message.contains("sensitive"));
    }
    assert!(parse(&["--query", &"é".repeat(251)]).is_err());
    let maximum = "é".repeat(250);
    assert!(
        parse(&[
            "--origin",
            "https://catalog.example",
            "--token-file",
            "t",
            "--query",
            &maximum
        ])
        .is_ok()
    );
}

#[test]
fn bare_token_accepts_one_terminal_newline_and_rejects_caches_and_headers() {
    for bytes in [
        b"token".as_slice(),
        b"token\n",
        b"token\r\n",
        b"BearerOpaqueToken",
    ] {
        assert!(report::bare_token(bytes).is_ok());
    }
    for bytes in [
        b"".as_slice(),
        b"token\n\n",
        b"Bearer token",
        b" token",
        b"token\0",
        b"{\"token\":\"secret\"}",
        b"{}",
        b"\"token\"",
        b"[secret]",
        &[255],
    ] {
        let message = report::bare_token(bytes).unwrap_err();
        assert!(!message.contains("secret"));
    }
    assert!(report::bare_token(&vec![b'x'; 32 << 10]).is_ok());
    assert!(report::bare_token(&vec![b'x'; (32 << 10) + 1]).is_err());
}

#[test]
fn markdown_html_controls_and_direction_overrides_cannot_break_cells() {
    assert_eq!(
        report::cell("a|[b](<x>)`*_\\\n\t\u{1b}\u{202e}&"),
        "a\\|\\[b\\]\\(&lt;x&gt;\\)\\`\\*\\_\\\\[U+000A][U+0009][U+001B][U+202E]&amp;"
    );
    assert_eq!(report::cell("&lt;script&gt;"), "&amp;lt;script&amp;gt;");
    assert_eq!(report::cell("é😀"), "é😀");
}

#[test]
fn display_bound_counts_characters_and_never_invents_a_license() {
    assert_eq!(report::cell(&"😀".repeat(512)), "😀".repeat(512));
    assert_eq!(
        report::cell(&"😀".repeat(513)),
        "omitted: exceeds 512 characters"
    );
    let mut value = Report::default();
    value.rows.push(Row {
        name: "Test".to_owned(),
        kind: "skill".to_owned(),
        version: "source-x".to_owned(),
        release: "uuid".to_owned(),
        identifier: "urn:air:example:demo:test".to_owned(),
        license: None,
    });
    assert!(value.render(&options()).contains("| not declared |"));
    value.rows[0].license = Some("x".repeat(513));
    let text = value.render(&options());
    assert!(text.contains("| omitted: exceeds 512 characters |"));
    assert!(!text.contains("not declared"));
}

#[test]
fn complete_empty_and_partial_empty_have_different_meanings() {
    let mut value = Report {
        pages: 1,
        ..Report::default()
    };
    assert_eq!(value.exit_code(), 0);
    assert!(value.render(&options()).contains("No authorized matches."));
    for capped in [false, true] {
        value.partial = !capped;
        value.capped = capped;
        assert_eq!(value.exit_code(), 2);
        let text = value.render(&options());
        assert!(text.contains("Status: Incomplete"));
        assert!(text.contains("No rows were obtained within the budget."));
        assert!(!text.contains("No authorized matches."));
    }
}

#[test]
fn query_is_escaped_and_statuses_are_deduplicated() {
    let mut options = options();
    options.query = Some("[query](<script>)\n".to_owned());
    let value = Report {
        diagnostics: BTreeSet::from(["failed".to_owned(), "timeout".to_owned()]),
        ..Report::default()
    };
    let text = value.render(&options);
    assert!(text.contains("Query: \\[query\\]\\(&lt;script&gt;\\)[U+000A]"));
    assert!(text.contains("Search diagnostic statuses: failed, timeout."));
}

#[tokio::test(start_paused = true)]
async fn whole_run_deadline_cancels_pending_work_without_network_or_wall_wait() {
    use std::{cell::Cell, rc::Rc};
    struct Dropped(Rc<Cell<bool>>);
    impl Drop for Dropped {
        fn drop(&mut self) {
            self.0.set(true);
        }
    }
    let dropped = Rc::new(Cell::new(false));
    let start = tokio::time::Instant::now();
    let work = async {
        let _guard = Dropped(dropped.clone());
        std::future::pending::<Result<Report, String>>().await
    };
    let error = report::within_deadline(work).await.err().unwrap();
    assert!(error.contains("sixty-second"));
    assert_eq!(start.elapsed(), report::RUN_TIMEOUT);
    assert!(dropped.get());
    assert!(
        report::within_deadline(async { Ok(Report::default()) })
            .await
            .is_ok()
    );
}
