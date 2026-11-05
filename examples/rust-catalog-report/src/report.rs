use open_agentic_catalog::{
    ApiOpError, CatalogRecord, CatalogSearch, CatalogSearchResultType, Client, Config, ResourceKind,
};
use serde_json::json;
use std::{
    collections::BTreeSet,
    ffi::OsString,
    fmt::Write,
    future::Future,
    io::Read,
    path::{Path, PathBuf},
    time::Duration,
};

const PAGE_LIMIT: usize = 10;
const PAGE_BUDGET: usize = 3;
const CELL_LIMIT: usize = 512;
pub const RUN_TIMEOUT: Duration = Duration::from_secs(60);
pub const HELP: &str = "Read authorized local releases and print a Markdown inventory.

Usage: catalog-rust-report --origin URL --token-file PATH [OPTIONS]

  --ca-file PATH             Private CA certificate bundle (PEM)
  --allow-loopback-http      Allow HTTP only at a literal loopback IP
  --query TEXT               Search text (at most 500 UTF-8 bytes)
  --kind KIND                agent | mcp_server | skill | plugin
  --help                     Show this help without credentials

Limits: three pages of ten results, thirty exact records, sixty seconds.
Exit: 0 complete, 2 incomplete, 1 failure (no report).";

pub struct Options {
    pub origin: String,
    pub token_file: PathBuf,
    pub ca_file: Option<PathBuf>,
    pub allow_loopback_http: bool,
    pub query: Option<String>,
    pub kind: Option<ResourceKind>,
}

impl Options {
    pub fn parse(arguments: impl IntoIterator<Item = OsString>) -> Result<Option<Self>, String> {
        let mut args = arguments.into_iter().peekable();
        let mut origin = None;
        let mut token_file = None;
        let mut ca_file = None;
        let mut allow_loopback_http = false;
        let mut query = None;
        let mut kind = None;
        let mut seen = BTreeSet::new();
        while let Some(argument) = args.next() {
            let flag = argument
                .to_str()
                .ok_or("Arguments must use UTF-8 flag names.")?;
            if flag == "--help" && seen.is_empty() && args.peek().is_none() {
                return Ok(None);
            }
            if !seen.insert(flag.to_owned()) {
                return Err("Duplicate option. Use --help for usage.".to_owned());
            }
            if flag == "--allow-loopback-http" {
                allow_loopback_http = true;
                continue;
            }
            if !matches!(
                flag,
                "--origin" | "--token-file" | "--ca-file" | "--query" | "--kind"
            ) {
                return Err("Unknown option. Use --help for usage.".to_owned());
            }
            let value = args.next().ok_or("Missing option value.")?;
            match flag {
                "--token-file" => token_file = Some(PathBuf::from(value)),
                "--ca-file" => ca_file = Some(PathBuf::from(value)),
                _ => {
                    let value = value.into_string().map_err(|_| "Invalid UTF-8 value.")?;
                    match flag {
                        "--origin" => origin = Some(value),
                        "--query" => {
                            if value.len() > 500 || value.contains('\0') {
                                return Err(
                                    "Query exceeds 500 UTF-8 bytes or contains NUL.".to_owned()
                                );
                            }
                            query = Some(value);
                        }
                        "--kind" => {
                            kind = Some(match value.as_str() {
                                "agent" => ResourceKind::Agent,
                                "mcp_server" => ResourceKind::McpServer,
                                "skill" => ResourceKind::Skill,
                                "plugin" => ResourceKind::Plugin,
                                _ => {
                                    return Err(
                                        "Invalid kind. Use --help for supported kinds.".to_owned()
                                    );
                                }
                            });
                        }
                        _ => unreachable!(),
                    }
                }
            }
        }
        Ok(Some(Self {
            origin: origin.ok_or("Missing --origin. Use --help for usage.")?,
            token_file: token_file.ok_or("Missing --token-file. Use --help for usage.")?,
            ca_file,
            allow_loopback_http,
            query,
            kind,
        }))
    }

    pub fn client(&self) -> Result<Client, String> {
        let token = read_file(&self.token_file, (32 << 10) + 2)
            .map_err(|_| "Token file must be a readable, bounded regular file.")?;
        let token = bare_token(&token)?;
        let mut config = Config::new(&self.origin);
        config.token = Some(token);
        config.allow_loopback_http = self.allow_loopback_http;
        if let Some(path) = &self.ca_file {
            config.root_certificates.push(
                read_file(path, 1 << 20)
                    .map_err(|_| "CA file must be a readable regular file of at most 1 MiB.")?,
            );
        }
        Client::new(config).map_err(|error| error.to_string())
    }

    fn search(&self, cursor: Option<&str>) -> Result<CatalogSearch, String> {
        let mut value = json!({"origins": ["local"], "limit": PAGE_LIMIT});
        if let Some(query) = &self.query {
            value["text"] = json!(query);
        }
        if let Some(kind) = &self.kind {
            value["kinds"] = json!([kind.as_str()]);
        }
        if let Some(cursor) = cursor {
            value["cursor"] = json!(cursor);
        }
        serde_json::from_value(value).map_err(|_| "Could not build Search input.".to_owned())
    }
}

fn read_file(path: &Path, limit: usize) -> std::io::Result<Vec<u8>> {
    if !std::fs::metadata(path)?.is_file() {
        return Err(std::io::Error::other("not a regular file"));
    }
    let file = std::fs::File::open(path)?;
    if !file.metadata()?.is_file() {
        return Err(std::io::Error::other("not a regular file"));
    }
    let mut bytes = Vec::new();
    file.take((limit + 1) as u64).read_to_end(&mut bytes)?;
    if bytes.len() > limit {
        return Err(std::io::Error::other("file exceeds bound"));
    }
    Ok(bytes)
}

pub(crate) fn bare_token(bytes: &[u8]) -> Result<String, String> {
    let token = bytes
        .strip_suffix(b"\r\n")
        .or_else(|| bytes.strip_suffix(b"\n"))
        .unwrap_or(bytes);
    if token.is_empty() || token.len() > 32 << 10 || token.iter().any(|b| *b <= 32 || *b >= 127) {
        return Err(
            "Token file must contain one bare ASCII access token, not a JSON credential cache."
                .to_owned(),
        );
    }
    let value = std::str::from_utf8(token).map_err(|_| "Invalid access token.")?;
    if value.starts_with(['{', '[', '"']) {
        return Err("Token file must contain a bare access token.".to_owned());
    }
    Ok(value.to_owned())
}

pub struct Row {
    pub name: String,
    pub kind: String,
    pub version: String,
    pub release: String,
    pub identifier: String,
    pub license: Option<String>,
}

impl Row {
    fn from_record(record: CatalogRecord) -> Self {
        Self {
            name: bounded(record.listing_revision.display_name),
            kind: record.resource.kind.to_string(),
            version: bounded(record.version.version),
            release: record.release.id.to_string(),
            identifier: bounded(record.resource.canonical_identifier),
            license: record.listing_revision.license.map(bounded),
        }
    }
}

#[derive(Default)]
pub struct Report {
    pub rows: Vec<Row>,
    pub pages: usize,
    pub partial: bool,
    pub capped: bool,
    pub diagnostics: BTreeSet<String>,
}

impl Report {
    pub fn exit_code(&self) -> u8 {
        if self.partial || self.capped { 2 } else { 0 }
    }

    pub fn render(&self, options: &Options) -> String {
        let mut output = String::from(
            "# Catalog report\n\nScope: caller-authorized local releases. Paging is not a point-in-time snapshot.\n\n",
        );
        writeln!(
            output,
            "Query: {}\n",
            cell(options.query.as_deref().unwrap_or("all"))
        )
        .unwrap();
        writeln!(
            output,
            "Kind: {}\n",
            options.kind.as_ref().map_or("all", ResourceKind::as_str)
        )
        .unwrap();
        writeln!(
            output,
            "Status: {} ({} {}, {} {}).\n",
            if self.exit_code() == 0 {
                "Complete"
            } else {
                "Incomplete"
            },
            self.pages,
            if self.pages == 1 { "page" } else { "pages" },
            self.rows.len(),
            if self.rows.len() == 1 {
                "release"
            } else {
                "releases"
            }
        )
        .unwrap();
        if self.capped {
            output.push_str("More matches remain; the three-page budget stopped this report.\n\n");
        }
        if self.partial {
            output.push_str("Search reported partial results; this report may omit matches.\n\n");
        }
        if !self.diagnostics.is_empty() {
            writeln!(
                output,
                "Search diagnostic statuses: {}.\n",
                self.diagnostics
                    .iter()
                    .cloned()
                    .collect::<Vec<_>>()
                    .join(", ")
            )
            .unwrap();
        }
        if self.rows.is_empty() {
            output.push_str(if self.exit_code() == 0 {
                "No authorized matches.\n"
            } else {
                "No rows were obtained within the budget.\n"
            });
            return output;
        }
        output.push_str("| Name | Kind | Version | Release UUID | Canonical identifier | Declared license |\n| --- | --- | --- | --- | --- | --- |\n");
        for row in &self.rows {
            writeln!(
                output,
                "| {} | {} | {} | {} | {} | {} |",
                cell(&row.name),
                cell(&row.kind),
                cell(&row.version),
                cell(&row.release),
                cell(&row.identifier),
                row.license
                    .as_deref()
                    .map_or_else(|| "not declared".to_owned(), cell)
            )
            .unwrap();
        }
        output
    }
}

fn bounded(value: String) -> String {
    if value.chars().take(CELL_LIMIT + 1).count() > CELL_LIMIT {
        "omitted: exceeds 512 characters".to_owned()
    } else {
        value
    }
}

pub(crate) fn cell(value: &str) -> String {
    if value.chars().take(CELL_LIMIT + 1).count() > CELL_LIMIT {
        return "omitted: exceeds 512 characters".to_owned();
    }
    let mut output = String::new();
    for c in value.chars() {
        match c {
            '&' => output.push_str("&amp;"),
            '<' => output.push_str("&lt;"),
            '>' => output.push_str("&gt;"),
            c if c.is_control()
                || matches!(c, '\u{202a}'..='\u{202e}' | '\u{2066}'..='\u{2069}') =>
            {
                write!(output, "[U+{:04X}]", c as u32).unwrap();
            }
            '\\' | '`' | '*' | '_' | '[' | ']' | '(' | ')' | '!' | '#' | '|' | '~' => {
                output.push('\\');
                output.push(c);
            }
            _ => output.push(c),
        }
    }
    output
}

pub async fn within_deadline(
    work: impl Future<Output = Result<Report, String>>,
) -> Result<Report, String> {
    tokio::time::timeout(RUN_TIMEOUT, work)
        .await
        .map_err(|_| "The sixty-second report deadline expired.".to_owned())?
}

pub async fn collect(client: &Client, options: &Options) -> Result<Report, String> {
    let mut report = Report::default();
    let mut cursor = None;
    let mut releases = BTreeSet::new();
    for _ in 0..PAGE_BUDGET {
        let page = client
            .search_catalog(options.search(cursor.as_deref())?)
            .await
            .map_err(|error| call_error("Search", error))?
            .data
            .ok_or("Search returned no page.")?;
        if page.items.len() > PAGE_LIMIT {
            return Err("Search exceeded the requested page limit.".to_owned());
        }
        report.pages += 1;
        report.partial |= page.partial;
        for diagnostic in page.diagnostics.unwrap_or_default() {
            report.diagnostics.insert(diagnostic.status.to_string());
        }
        for item in page.items {
            if item.r#type != CatalogSearchResultType::Local {
                return Err("Search returned a non-local result.".to_owned());
            }
            let local = item.local.ok_or("Local result has no release reference.")?;
            let release = local.release.id;
            if !releases.insert(release) {
                return Err("Search repeated a release UUID.".to_owned());
            }
            let record = client
                .get_catalog_record(release.to_string())
                .await
                .map_err(|error| call_error("Record read", error))?
                .data
                .ok_or("Record read returned no record.")?;
            if record.release.id != release {
                return Err("Record read returned a different release UUID.".to_owned());
            }
            report.rows.push(Row::from_record(record));
        }
        cursor = page.next_cursor;
        if cursor
            .as_ref()
            .is_some_and(|v| v.is_empty() || v.len() > 4096)
        {
            return Err("Search returned an invalid continuation.".to_owned());
        }
        if cursor.is_none() {
            return Ok(report);
        }
    }
    report.capped = cursor.is_some();
    Ok(report)
}

fn call_error<E>(operation: &str, error: ApiOpError<E>) -> String {
    match error {
        ApiOpError::Api(response) if (200..300).contains(&response.status) => format!(
            "{operation} failed: Catalog returned an unexpected or malformed success response (HTTP {}).",
            response.status
        ),
        // SDK Display exposes safe transport codes or HTTP status, never a body.
        other => format!("{operation} failed: {other}"),
    }
}
