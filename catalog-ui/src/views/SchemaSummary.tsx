import { schemaSummary, type SchemaNode } from "./schema-summary";
import type { Document } from "../api/reference";

function Node({
  node,
  onReference,
}: {
  node: SchemaNode;
  onReference: (ref: string) => void;
}) {
  return (
    <div className="api-schema-node">
      <p>
        <strong>{node.label}</strong>
      </p>
      {node.description && (
        <p className="api-description">{node.description}</p>
      )}
      {!!node.facts.length && (
        <dl className="api-contract-facts">
          {node.facts.map(([name, value]) => (
            <div key={name}>
              <dt>{name}</dt>
              <dd>{value}</dd>
            </div>
          ))}
        </dl>
      )}
      {node.reference &&
        (node.reference.startsWith("#/") ? (
          <button type="button" onClick={() => onReference(node.reference)}>
            Inspect schema reference {node.reference}
          </button>
        ) : (
          <p>
            External or malformed reference; not fetched.{" "}
            <code>{node.reference}</code>
          </p>
        ))}
      {node.reference && (
        <p>
          References stay as links. Facts can describe the referenced target;
          sibling keywords remain in complete data.
        </p>
      )}
      {node.unavailable && (
        <p>
          Some schema metadata is unavailable or unsupported. Inspect complete
          decoded data.
        </p>
      )}
      {node.children.map((child, index) => (
        <Node key={index} node={child} onReference={onReference} />
      ))}
      {node.omitted && (
        <p className="notice">
          Schema summary limit reached. Additional depth, nodes or rows remain
          in complete decoded data and local definitions.
        </p>
      )}
    </div>
  );
}

/** Summarize selected schema declarations without hiding their retained JSON or exact YAML. */
export function SchemaSummary({
  document,
  value,
  onReference,
}: {
  document: Document;
  value: unknown;
  onReference: (ref: string) => void;
}) {
  return (
    <section aria-label="Readable schema summary">
      <h4>Declared schema facts</h4>
      <p>
        Bounded presentation only: composition is not flattened, annotations do
        not determine requiredness, and absent facts remain undeclared.
      </p>
      <Node node={schemaSummary(document, value)} onReference={onReference} />
    </section>
  );
}
