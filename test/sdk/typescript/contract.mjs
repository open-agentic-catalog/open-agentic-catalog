// Compare the complete generated type graph with the independent OpenAPI source.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import ts from "../../../sdk/typescript/node_modules/typescript/lib/typescript.js";
import { parse } from "../../../sdk/typescript/node_modules/yaml/dist/index.js";
const contract = parse(
  await readFile(
    new URL("../../../api/openapi/control-v1.yaml", import.meta.url),
    "utf8",
  ),
);
// JSON Schema patterns must also work in the consumer's ECMAScript engine.
const resultID = new RegExp(
  contract.components.schemas.CatalogSearchResult.properties.id.pattern,
);
for (const kind of ["local", "indexed", "live"])
  assert.ok(resultID.test(`${kind}:selected`), `${kind} result ID refused`);
for (const invalid of [
  "",
  "foreign:selected",
  "local:",
  ...[" ", "\t", "\n", "\r", "\v", "\f"].map(
    (space) => `local:before${space}after`,
  ),
])
  assert.equal(resultID.test(invalid), false, "invalid result ID admitted");
const source = ts.createSourceFile(
  "schema.d.ts",
  await readFile(
    new URL("../../../sdk/typescript/src/schema.d.ts", import.meta.url),
    "utf8",
  ),
  ts.ScriptTarget.Latest,
  true,
);
const interfaces = new Map(
  source.statements
    .filter(ts.isInterfaceDeclaration)
    .map((n) => [n.name.text, n]),
);
function resolveType(node) {
  if (ts.isTypeReferenceNode(node)) return interfaces.get(node.typeName.text);
  if (ts.isIndexedAccessTypeNode(node))
    return members(resolveType(node.objectType)).get(
      node.indexType.literal.text,
    ).type;
  return node;
}
const members = (node) =>
  new Map(resolveType(node).members.map((n) => [String(n.name.text), n]));
const paths = members(interfaces.get("paths"));
const operations = members(interfaces.get("operations"));
const resolve = (node) =>
  node.$ref
    ? resolve(
        node.$ref
          .slice(2)
          .split("/")
          .reduce((value, key) => value[key], contract),
      )
    : node;
let count = 0;
for (const [path, methods] of Object.entries(contract.paths)) {
  const generated = members(paths.get(path).type);
  for (const [method, operation] of Object.entries(methods)) {
    if (!operation.operationId) continue;
    count++;
    const reference = generated.get(method).type;
    assert.equal(reference.indexType.literal.text, operation.operationId);
    const fields = members(operations.get(operation.operationId).type);
    const responses = members(fields.get("responses").type);
    assert.deepEqual(
      [...responses.keys()].sort(),
      Object.keys(operation.responses).sort(),
    );
    for (const [status, response] of Object.entries(operation.responses)) {
      const content = members(responses.get(status).type).get("content");
      const expected = Object.keys(resolve(response).content ?? {}).sort();
      assert.deepEqual(
        content && ts.isTypeLiteralNode(content.type)
          ? [...members(content.type).keys()].sort()
          : [],
        expected,
        `${path} ${status} media`,
      );
    }
    if (operation.requestBody) {
      const content = members(fields.get("requestBody").type).get("content");
      assert.deepEqual(
        [...members(content.type).keys()].sort(),
        Object.keys(resolve(operation.requestBody).content).sort(),
      );
    }
  }
}
assert.equal(operations.size, count);
assert.deepEqual([...paths.keys()].sort(), Object.keys(contract.paths).sort());
const schemas = members(
  members(interfaces.get("components")).get("schemas").type,
);
assert.deepEqual(
  [...schemas.keys()].sort(),
  Object.keys(contract.components.schemas).sort(),
);
console.log(
  `Complete TypeScript contract inventory: ${count} operations, ${schemas.size} named schemas, all paths and request/response alternatives`,
);

function ownProperties(node) {
  node = resolveType(node);
  if (ts.isParenthesizedTypeNode(node)) return ownProperties(node.type);
  if (ts.isUnionTypeNode(node) || ts.isIntersectionTypeNode(node))
    return new Set(node.types.flatMap((part) => [...ownProperties(part)]));
  if (ts.isTypeLiteralNode(node))
    return new Set(
      node.members.filter(ts.isPropertySignature).map((part) => part.name.text),
    );
  return new Set();
}
let objectCount = 0;
for (const [name, schema] of Object.entries(contract.components.schemas)) {
  if (!schema.properties) continue;
  objectCount++;
  const actual = ownProperties(schemas.get(name).type);
  for (const field of Object.keys(schema.properties))
    assert.ok(actual.has(field), `${name}.${field} missing after generation`);
}
console.log(`Preserved declared fields in ${objectCount} named object schemas`);
