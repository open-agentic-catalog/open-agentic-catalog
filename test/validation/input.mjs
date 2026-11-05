// This is a restricted local-input profile, not another OpenAPI validator.
import { createHash } from "node:crypto";
import { open, realpath } from "node:fs/promises";
import { constants } from "node:fs";
import { parseAllDocuments, visit, isScalar } from "yaml";
import { Failure } from "./process.mjs";

export const INPUT_LIMIT = 2 << 20;
const DEPTH_LIMIT = 96;
const NODE_LIMIT = 100_000;
const DIALECTS = new Set([
  "https://spec.openapis.org/oas/3.1/dialect/base",
  "https://json-schema.org/draft/2020-12/schema",
]);
// digest binds an inspection to exact input bytes, not a parsed/reserialized schema.
export const digest = (bytes) => createHash("sha256").update(bytes).digest("hex");

// O_NONBLOCK prevents a selected FIFO from hanging before the regular-file check.
export async function readBounded(filename, limit = INPUT_LIMIT) {
  let file;
  try {
    file = await open(filename, constants.O_RDONLY | constants.O_NONBLOCK);
    const stat = await file.stat();
    if (!stat.isFile() || stat.size > limit) throw new Failure("input must be a bounded regular file");
    const buffer = Buffer.alloc(limit + 1);
    let offset = 0;
    while (offset < buffer.length) {
      const { bytesRead } = await file.read(buffer, offset, buffer.length - offset, null);
      if (!bytesRead) break;
      offset += bytesRead;
    }
    if (offset > limit) throw new Failure("input exceeds its byte limit");
    return buffer.subarray(0, offset);
  } catch (error) {
    if (error instanceof Failure) throw error;
    throw new Failure("cannot read the selected validation input");
  } finally {
    await file?.close();
  }
}

// parseInput accepts one bounded YAML 1.2/JSON tree without ambiguous keys, tags
// or aliases, preserving integer precision for local policy inspection.
export function parseInput(raw) {
  if (raw.length > INPUT_LIMIT) throw new Failure("input exceeds its byte limit");
  let documents;
  try {
    const text = new TextDecoder("utf-8", { fatal: true }).decode(raw);
    documents = parseAllDocuments(text, { uniqueKeys: true, intAsBigInt: true, version: "1.2" });
    if (documents.length !== 1 || documents[0].errors.length || documents[0].warnings.length)
      throw new Failure("input must be one unambiguous YAML/JSON document (check duplicate keys and tags)");
    let count = 0;
    visit(documents[0], {
      Node(_key, node, ancestors) {
        if (++count > NODE_LIMIT || ancestors.length > DEPTH_LIMIT)
          throw new Failure("input exceeds its structural limit");
        if (node.tag) throw new Failure("explicit YAML tags are outside the local validation profile");
      },
      Alias() { throw new Failure("YAML aliases are outside the local validation profile"); },
      Pair(_key, pair) {
        if (!isScalar(pair.key) || typeof pair.key.value !== "string")
          throw new Failure("mapping keys must be strings");
      },
      Scalar(_key, scalar) {
        if (scalar.tag) throw new Failure("explicit YAML tags are outside the local validation profile");
        if (typeof scalar.value === "number" && !Number.isFinite(scalar.value))
          throw new Failure("non-finite numbers are not JSON values");
      },
    });
    // BigInts stay exact in this inspection tree. Tools receive the original bytes.
    return documents[0].toJS({ maxAliasCount: 0, mapAsMap: false });
  } catch (error) {
    if (error instanceof Failure) throw error;
    throw new Failure("input could not be parsed safely");
  }
}

// localReference limits resolution to this document's root or JSON Pointer fragments.
function localReference(value) {
  if (typeof value !== "string" || !(value === "#" || value.startsWith("#/")))
    throw new Failure("only local JSON Pointer references are supported");
}

// Schema annotations and example values are data. Only schema-bearing keywords
// are traversed here, so a property literally named '$ref' is not fetched.
export function preflight(raw) {
  const document = parseInput(raw);
  if (!document || Array.isArray(document) || document.openapi !== "3.1.0")
    throw new Failure("the local validation profile requires OpenAPI 3.1.0");
  let nodes = 0;
  const schemaMaps = new Set(["properties", "patternProperties", "$defs", "definitions", "dependentSchemas"]);
  const schemaSingles = new Set(["items", "additionalProperties", "unevaluatedProperties",
    "unevaluatedItems", "contains", "propertyNames", "not", "if", "then", "else", "contentSchema"]);
  const schemaArrays = new Set(["allOf", "anyOf", "oneOf", "prefixItems"]);
  const documentMaps = new Set(["paths", "responses", "headers", "links", "callbacks",
    "requestBodies", "parameters", "securitySchemes", "pathItems", "content"]);
  // walk distinguishes schema keywords, named maps and literal example values.
  function walk(node, mode = "document", depth = 0) {
    if (++nodes > NODE_LIMIT || depth > DEPTH_LIMIT) throw new Failure("input exceeds its structural limit");
    if (node === null || typeof node !== "object") return;
    if (Array.isArray(node)) {
      for (const item of node) walk(item, mode, depth + 1);
      return;
    }
    if (mode === "map") {
      for (const child of Object.values(node)) walk(child, "document", depth + 1);
      return;
    }
    if ("$ref" in node) localReference(node.$ref);
    if ("$id" in node || "$dynamicRef" in node || "$recursiveRef" in node)
      throw new Failure("reference-base identifiers and dynamic references are not supported");
    for (const key of ["$schema", "jsonSchemaDialect"])
      if (key in node && !DIALECTS.has(node[key])) throw new Failure("unsupported schema dialect");
    if (mode !== "schema" && "operationRef" in node) localReference(node.operationRef);
    if (mode === "example") {
      if ("externalValue" in node) throw new Failure("external examples are not supported");
      return; // Example.value is literal data; Example.$ref was checked above.
    }
    if (mode === "schema") {
      for (const [key, value] of Object.entries(node)) {
        if (schemaMaps.has(key) && value && typeof value === "object")
          for (const child of Object.values(value)) walk(child, "schema", depth + 1);
        else if (schemaSingles.has(key) || schemaArrays.has(key)) walk(value, "schema", depth + 1);
      }
      if (node.discriminator?.mapping)
        for (const ref of Object.values(node.discriminator.mapping)) localReference(ref);
      return;
    }
    for (const [key, value] of Object.entries(node)) {
      if (key === "example" || key.startsWith("x-")) continue;
      if (key === "schema") walk(value, "schema", depth + 1);
      else if ((key === "schemas" || key === "examples") && value && typeof value === "object") {
        for (const child of Object.values(value)) walk(child, key === "schemas" ? "schema" : "example", depth + 1);
      } else walk(value, documentMaps.has(key) && !Array.isArray(value) ? "map" : "document", depth + 1);
    }
  }
  walk(document);
  return document;
}

// snapshot preflights exact bytes and remembers their resolved source and digest;
// validators still read that file, so callers must check unchanged afterwards.
export async function snapshot(filename) {
  const raw = await readBounded(filename);
  preflight(raw);
  return { filename: await realpath(filename), raw, hash: digest(raw) };
}

// unchanged rejects source drift during a read-only validator invocation.
export async function unchanged(input) {
  if (digest(await readBounded(input.filename)) !== input.hash)
    throw new Failure("validation input changed during the check; run again on stable inputs");
}
