"""Guarded output repairs for the pinned generator, never an OpenAPI parser."""

import json
from pathlib import Path
import re


def contract_operations(source):
    """Read operation headers from the already validated, maintained YAML layout."""
    path = method = None
    operations = {}
    for line in source.splitlines():
        path_match = re.fullmatch(r"  (/.*):", line)
        method_match = re.fullmatch(
            r"    (get|post|put|patch|delete|head|options|trace):", line
        )
        operation_match = re.fullmatch(r"      operationId: ([A-Za-z0-9_]+)", line)
        if path_match:
            path = path_match[1]
        elif method_match:
            method = method_match[1].upper()
        elif operation_match:
            name = operation_match[1]
            if not path or not method or name in operations:
                raise RuntimeError("Invalid authoritative Rust operation inventory")
            operations[name] = (method, path)
    all_ids = re.findall(r"^\s+operationId: ([A-Za-z0-9_]+)$", source, re.M)
    if not operations or len(all_ids) != len(operations):
        raise RuntimeError("OpenAPI operation layout changed; review Rust inventory")
    return operations


def _method_spans(text, names):
    # Streaming variants can have one-line or multiline generic parameters.
    declarations = list(re.finditer(r"^ {4}pub async fn (\w+)(?=[<(])", text, re.M))
    if len(declarations) != len(names) or {match[1] for match in declarations} != names:
        raise RuntimeError("Rust generated operation inventory changed")
    spans = []
    closing = "\n    }\n"
    for index, declaration in enumerate(declarations):
        limit = declarations[index + 1].start() if index + 1 < len(declarations) else len(text)
        end = text.find(closing, declaration.end(), limit)
        # Adjacent closing lines share the delimiter's boundary LF.
        if end < 0 or text.find(closing, end + len(closing) - 1, limit) >= 0:
            raise RuntimeError("Rust generator method boundary changed")
        spans.append((declaration, end + len(closing)))
    return spans


def client_source(source, bindings):
    """Retain generated operations/codecs; replace unsafe transport boilerplate."""
    methods = [s for s in bindings["symbols"] if s.get("operation")]
    streaming = {
        s["name"] for s in methods if s["operation"]["consumption"] != "buffered"
    }
    expected = {s["name"] for s in methods} - streaming
    marker = "fn __pct_encode_path_segment(s: &str) -> String {"
    if source.count(marker) != 1:
        raise RuntimeError("Rust generator transport marker changed")
    source = """//! Generated operations from the complete Control OpenAPI.
//! Regenerate with make sdk-generate SDK_LANGUAGE=rust; do not edit.
#![allow(clippy::format_in_format_args, clippy::let_unit_value, clippy::too_many_arguments)]
use super::types::*;
use crate::{Client, Error as HttpError, ApiError, ApiOpError, Response};
""" + source[source.index(marker) :]
    source = source.replace("impl HttpClient {", "impl Client {")
    source = source.replace("ApiOpError::Api(", "ApiOpError::api(")

    # The tool emits additional, unbounded streaming variants for wildcard
    # downloads. Keep their ordinary bounded byte methods, not those variants.
    spans = _method_spans(source, {method["name"] for method in methods})
    pieces, end, removed = [], 0, 0
    for declaration, stop in spans:
        if declaration[1] in streaming:
            pieces.append(source[end:declaration.start()])
            end = stop
            removed += 1
    if removed != len(streaming):
        raise RuntimeError("Rust generator binary variants changed")
    pieces.append(source[end:])
    source = "".join(pieces)
    source = re.sub(
        r"^    ///(?:Select `\*/\*` as binary_stream|Success chunks are returned live).*\n",
        "",
        source,
        flags=re.M,
    )
    source, auth = re.subn(
        r"        if let Some\(api_key\) = &self.api_key \{\n            req = req.bearer_auth\(api_key\);\n        }\n",
        "",
        source,
    )
    source, headers = re.subn(
        r"^        for \(name, value\) in &self.custom_headers \{\n(?: {12}[^\n]*\n)*        }\n",
        "",
        source,
        flags=re.M,
    )
    if auth != len(expected) or headers != len(expected):
        raise RuntimeError("Rust generator identity boilerplate changed")
    source, sends = re.subn(
        r"let response = req.send\(\).await\?;",
        "let response = self.send(req).await?;",
        source,
    )
    if sends != len(expected):
        raise RuntimeError("Rust generator send boundary changed")

    # Repair each method within its own validated bounds. Neither a missing
    # method end nor fake result openers may borrow from the next operation.
    spans = _method_spans(source, expected)
    pieces, end = [], 0
    for declaration, stop in spans:
        body = source.find("{", declaration.end(), stop)
        result = source.find(") -> Result<", declaration.end(), body) if body >= 0 else -1
        # Keep multiline signatures, with unambiguous whitespace ownership,
        # and never search again within a failed signature or cross its body.
        signature = re.fullmatch(
            r"\) -> Result<\s*([^,\s{}][^,{}]*),\s*ApiOpError<([^>{}]+)>\s*(?:,\s*)?> \{",
            source[result:body + 1],
        ) if body >= 0 and result >= 0 else None
        if signature is None:
            raise RuntimeError("Rust generator result signature changed")
        method = (source[declaration.start():result]
                  + ") -> Result<Response<" + signature[1] + ">, ApiOpError<" + signature[2] + ">> {"
                  + source[body + 1:stop])
        # Match one LF-delimited line at a time; \s must not rescan across
        # long blank-line runs while looking for a surviving req assignment.
        if not any(re.match(r"\s+req =", line) for line in method.split("\n")):
            method = method.replace("let mut req =", "let req =")
        pieces.extend((source[end:declaration.start()], method))
        end = stop
    pieces.append(source[end:])
    source = "".join(pieces)
    source = re.sub(
        r"__read_bounded_response_body\(\s*response,\s*self.max_response_body_bytes,\s*\)\s*\.await\?",
        "response.body.clone()",
        source,
    )
    source = source.replace(
        "Ok(body) => Ok(body)",
        "Ok(body) => Ok(Response::new(body, status_code, headers, raw_body))",
    )
    source = source.replace(
        "Ok(())", "Ok(Response::new((), status_code, headers, raw_body))"
    )
    source = source.replace(
        "Ok(body_text)", "Ok(Response::new(body_text, status_code, headers, raw_body))"
    )
    source = source.replace(
        "Ok(bytes::Bytes::from(body_bytes))",
        "Ok(Response::new(bytes::Bytes::from(body_bytes.clone()), status_code, headers, body_bytes))",
    )
    # Decode the original bytes. The tool's lossy text is useful only as
    # explicit error evidence; parsing it would silently accept invalid UTF-8.
    source, decoded = re.subn(
        r"serde_json::from_str(?P<type>::<[^>\n]+>)?\(&body_text\)",
        lambda match: "serde_json::from_slice" + (match["type"] or "") + "(&raw_body)",
        source,
    )
    if not decoded or "serde_json::from_str" in source:
        raise RuntimeError("Rust generator JSON decoding boundary changed")
    # 304 is a declared empty result, not malformed success JSON or a problem.
    for method in methods:
        if method["name"] in expected and not method["operation"]["response_statuses"]:
            pattern = (
                r"(    pub async fn "
                + re.escape(method["name"])
                + r"\(.*?)if status.is_success\(\) \{\s*let _ = body_text;\s*let _ = raw_body;\s*let _ = headers;\s*Ok\(Response::new\(\(\), status_code, headers, raw_body\)\)\s*} else \{"
            )
            source, count = re.subn(
                pattern,
                r'''\1if status.is_success() {
                    Err(ApiOpError::api(ApiError {
                        status: status_code,
                        headers,
                        body: body_text,
                        raw_body,
                        typed: None,
                        parse_error: Some(format!(
                            "unexpected successful status {}; generated return type selects `{}`",
                            status_code, "any declared 2xx response",
                        )),
                    }))
                } else {''',
                source,
                flags=re.S,
            )
            if count != 1:
                raise RuntimeError("Rust generator refusal-only status changed")
        if (
            method["name"] not in expected
            or "304" not in method["operation"]["response_excluded_statuses"]
        ):
            continue
        pattern = (
            r"(    pub async fn "
            + re.escape(method["name"])
            + r"\(.*?let headers = response.headers\(\).clone\(\);)"
        )
        source, count = re.subn(
            pattern,
            r"\1\n        if status_code == 304 { return Ok(Response::empty(status_code, headers, response.body)); }",
            source,
            flags=re.S,
        )
        if count != 1:
            raise RuntimeError("Rust generator 304 result changed")
    if any(
        marker in source
        for marker in (
            "req.send()",
            "self.api_key",
            "self.custom_headers",
            "__read_bounded",
            "bytes_stream()",
            "reqwest_middleware",
            "reqwest_retry",
        )
    ):
        raise RuntimeError("Unmanaged Rust transport survived generation")
    found = set(re.findall(r"pub async fn (\w+)\(", source))
    if found != expected:
        raise RuntimeError("Rust generated operation inventory changed")
    return source


def types_source(source, spec):
    """Reject unknown control fields and preserve explicit null in opaque JSON."""
    # The pinned generator leaves these Rust types and ARD filter IRIs as
    # plain prose. Format documentation only; never rewrite codecs or data.
    source = re.sub(
        r"^(\s*///.*)$",
        lambda match: rustdoc_line(match[0]),
        source,
        flags=re.M,
    )
    for name, schema in spec["components"]["schemas"].items():
        if schema.get("additionalProperties") is not False or any(
            key in schema for key in ("oneOf", "allOf", "anyOf")
        ):
            continue
        marker = "pub struct " + name + " {"
        if (
            source.count(marker) == 1
            and "#[serde(deny_unknown_fields)]\n" + marker not in source
        ):
            source = source.replace(marker, "#[serde(deny_unknown_fields)]\n" + marker)
    # A shared union whose branches already consume every common field needs
    # no projected retry input. The pinned generator emits a no-op mutable
    # object binding here; retain the retry and error semantics without that
    # unused binding. Never remove a projection which actually removes fields.
    source = re.sub(
        r"let mut variant_input = value;\n[ \t]*if let Some\(object\) = variant_input\.as_object_mut\(\) \{[ \t\r\n]*\}",
        "let variant_input = value;",
        source,
    )
    return source.replace(
        "Dependency-free builder for [`#struct_name`].",
        "Dependency-free builder for this request.",
    )


def rustdoc_line(line):
    """Format plain types and URLs while preserving existing inline code."""
    pieces = re.split(r"(`+[^`]*`+)", line)
    for index in range(0, len(pieces), 2):
        text = re.sub(r"Option<Vec<u8>>|Vec<u8>", r"`\g<0>`", pieces[index])
        pieces[index] = re.sub(
            r"(?<![<\w])https?://[^\s<>`]+[\w/#]",
            r"<\g<0>>",
            text,
        )
    return "".join(pieces)


def repair(directory, spec_path):
    directory = Path(directory)
    bindings = json.loads((directory / "bindings.json").read_text())
    spec = json.loads((directory / "effective.json").read_text())
    (directory / "client.rs").write_text(
        client_source((directory / "client.rs").read_text(), bindings)
    )
    types = types_source((directory / "types.rs").read_text(), spec)
    types = types.replace(str(spec_path), "api/openapi/control-v1.yaml")
    (directory / "types.rs").write_text(types)
    (directory / "mod.rs").write_text(
        "//! Generated Control API bindings. Do not edit.\n"
        "#![allow(dead_code, clippy::redundant_field_names, clippy::match_single_binding, "
        "clippy::clone_on_copy, clippy::double_must_use, clippy::collapsible_if)]\n"
        "pub mod types;\npub mod client;\n"
    )
    operations = [
        {"method": s["name"], **s["operation"]}
        for s in bindings["symbols"]
        if s.get("operation") and s["operation"]["consumption"] == "buffered"
    ]
    for name in ("bindings.json", "effective.json", "REQUIRED_DEPS.toml"):
        (directory / name).unlink()
    (directory / "operations.json").write_text(
        json.dumps(operations, indent=2, sort_keys=True) + "\n"
    )
