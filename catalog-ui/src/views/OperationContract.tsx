// Readable contract declarations never execute requests or evaluate current grants.
import { useRef, useState } from "react";
import {
  localValue,
  object,
  references,
  text,
  type Document,
  type Operation,
} from "../api/reference";
import {
  authentication,
  decodedText,
  exampleCopyLimit,
  record,
  schemaFacts,
  summaryLimit,
} from "./contract-summary";
import { CodeBlock, JSONBlock } from "./CodeBlock";
import { CopyReference, CopyText } from "./CopyReference";

type Context = { document: Document; onReference: (reference: string) => void };

function ReferenceLinks({
  value,
  onReference,
  exclude = [],
}: {
  value: unknown;
  onReference: Context["onReference"];
  exclude?: string[];
}) {
  const refs = references(value).filter((ref) => !exclude.includes(ref));
  return (
    <>
      {refs.slice(0, summaryLimit).map((ref) => (
        <button
          type="button"
          className="api-ref-link"
          key={ref}
          onClick={() => onReference(ref)}
        >
          View {ref.slice(2)}
        </button>
      ))}
      {refs.length > summaryLimit && (
        <p>
          Additional local references remain in complete data and the definition
          selector.
        </p>
      )}
    </>
  );
}

/** Keep exact decoded declarations available without eagerly rendering every code viewer. */
function CompleteData({
  value,
  onReference,
  related,
  exclude,
  label = "Complete decoded data",
}: {
  value: unknown;
  onReference: Context["onReference"];
  related?: unknown;
  exclude?: string[];
  label?: string;
}) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <ReferenceLinks
        value={[value, related]}
        onReference={onReference}
        {...(exclude ? { exclude } : {})}
      />
      {value === undefined ? (
        <p>Definition unavailable in this contract.</p>
      ) : (
        <details
          className="api-complete-data"
          open={open}
          onToggle={(event) => setOpen(event.currentTarget.open)}
        >
          <summary>{label}</summary>
          {open && <JSONBlock value={value} />}
        </details>
      )}
    </>
  );
}

function Facts({ facts }: { facts: [string, string][] }) {
  return (
    <dl className="api-contract-facts">
      {facts.map(([key, value]) => (
        <div key={key}>
          <dt>{key}</dt>
          <dd>{value}</dd>
        </div>
      ))}
    </dl>
  );
}

function Schema({ value, document }: { document: Document; value: unknown }) {
  if (value === undefined) return <p>No schema declared.</p>;
  const summary = schemaFacts(document, value);
  const declaration = object(localValue(document, value));
  return (
    <div className="api-schema-summary">
      {summary.referenced && (
        <p>
          Referenced schema facts; sibling keywords remain in complete data.
        </p>
      )}
      {!!summary.facts.length && <Facts facts={summary.facts} />}
      {!summary.facts.length && !summary.unavailable && (
        <p>
          No direct type or scalar constraints declared. Inspect the complete
          schema.
        </p>
      )}
      {summary.unavailable && (
        <p>
          Some schema details are unavailable or unsupported. Inspect complete
          decoded data and local definitions.
        </p>
      )}
      {Object.hasOwn(declaration, "example") && (
        <Example value={declaration.example} label="schema example" />
      )}
      {declaration.examples !== undefined && (
        <>
          <p>
            Schema example annotations; these are not defaults or verified
            current records.
          </p>
          {Array.isArray(declaration.examples) ? (
            declaration.examples
              .slice(0, summaryLimit)
              .map((example: unknown, index: number) => (
                <Example
                  key={index}
                  value={example}
                  label={`schema example ${index + 1}`}
                />
              ))
          ) : (
            <p>Schema example metadata unavailable; inspect complete data.</p>
          )}
          {Array.isArray(declaration.examples) &&
            declaration.examples.length > summaryLimit && (
              <p>Additional schema examples remain in complete decoded data.</p>
            )}
        </>
      )}
    </div>
  );
}

function Description({ raw, value }: { raw: unknown; value: Document }) {
  const declaration = object(raw);
  const description =
    typeof declaration.description === "string"
      ? declaration.description
      : text(value.description);
  return description ? <p className="api-description">{description}</p> : null;
}

function Required({ value }: { value: unknown }) {
  return (
    <p>
      {value === true
        ? "Required"
        : value === false
          ? "Not required (declared false)"
          : "Requiredness not declared or unavailable."}
    </p>
  );
}

function Auth({ value, document, onReference }: Context & { value: unknown }) {
  const auth = authentication(document, value);
  const message = {
    none: "No API authentication requirement.",
    optional: "Anonymous or authenticated alternatives are declared.",
    required: "API authentication is required.",
    unavailable:
      "Authentication details unavailable. Do not assume anonymous access.",
  }[auth.kind];
  return (
    <>
      <p className="api-auth-summary">{message}</p>
      {auth.kind === "optional" && (
        <p>
          Anonymous admission still depends on the Catalog host, audience and
          domain rules.
        </p>
      )}
      {auth.alternatives.length > 1 && (
        <p>
          Choose one declared alternative. All schemes within that alternative
          are required together.
        </p>
      )}
      <ol className="api-auth-alternatives">
        {auth.alternatives.slice(0, summaryLimit).map((schemes, index) => (
          <li key={index}>
            {!schemes.length ? (
              <span>No API credential required by this alternative.</span>
            ) : (
              schemes.slice(0, summaryLimit).map((scheme) => (
                <div className="api-auth-scheme" key={scheme.name}>
                  <strong>{scheme.label}</strong> <code>{scheme.name}</code>
                  {scheme.description && (
                    <p className="api-description">{scheme.description}</p>
                  )}
                  {!!scheme.values.length && (
                    <p>
                      {scheme.oauth
                        ? "Declared OAuth scopes"
                        : "Declared requirement values"}
                      : <code>{scheme.values.join(", ")}</code>
                    </p>
                  )}
                  <button
                    type="button"
                    className="api-ref-link"
                    onClick={() => onReference(scheme.reference)}
                  >
                    View {scheme.reference.slice(2)}
                  </button>
                </div>
              ))
            )}
            {schemes.length > summaryLimit && (
              <p>Additional schemes remain in complete decoded data.</p>
            )}
          </li>
        ))}
      </ol>
      {auth.alternatives.length > summaryLimit && (
        <p>Additional alternatives remain in complete decoded data.</p>
      )}
      <p className="muted">
        These are contract declarations, not current permission checks. HTTP
        scheme requirement values are not automatically OAuth scopes or Catalog
        roles. Empty requirement arrays do not bypass OAuth ceilings.
      </p>
      <CompleteData value={value} onReference={onReference} />
    </>
  );
}

function Field({
  raw,
  headerName,
  document,
  onReference,
}: Context & { raw: unknown; headerName?: string }) {
  const resolved = localValue(document, raw);
  const fields = object(resolved);
  const name = headerName ?? text(fields.name);
  const location = headerName === undefined ? text(fields.in) : "header";
  const valid =
    record(resolved) &&
    !!name &&
    ["path", "query", "header", "cookie"].includes(location);
  return (
    <div className="api-contract-field">
      <p className="api-contract-label">
        <strong>
          {valid ? name : "Parameter or header metadata unavailable"}
        </strong>
      </p>
      {valid && (
        <>
          <p>
            <strong>Location:</strong> {location}
          </p>
          <Required value={fields.required} />
          <Description raw={raw} value={fields} />
          <Schema value={fields.schema} document={document} />
          {fields.content !== undefined && (
            <Media value={fields.content} document={document} />
          )}
          {Object.hasOwn(fields, "example") && (
            <Example value={fields.example} label={`${name} example`} />
          )}
          {fields.examples !== undefined && (
            <NamedExamples value={fields.examples} document={document} />
          )}
          {["style", "explode", "allowEmptyValue", "allowReserved"].some(
            (key) => Object.hasOwn(fields, key),
          ) && <p>Serialization details remain in complete decoded data.</p>}
        </>
      )}
      {!valid && (
        <p>
          Inspect the retained declaration and any local definition; no field
          name, location or type is inferred.
        </p>
      )}
      <CompleteData value={raw} related={resolved} onReference={onReference} />
    </div>
  );
}

function Example({ value, label }: { value: unknown; label: string }) {
  const content = decodedText(value);
  if (content === undefined) return <p>Example content unavailable.</p>;
  const large = content.length > exampleCopyLimit;
  return (
    <div className="api-example">
      <p className="api-contract-label">
        <strong>{label}</strong>
      </p>
      <p>
        {typeof value === "string"
          ? "Supplied string; copied verbatim."
          : "Decoded JSON example; copied as JSON, not original YAML bytes."}
      </p>
      {!large && <CodeBlock text={content} original />}
      <CopyText label={`Copy ${label}`} text={content} disabled={large} />
      {large && (
        <p>
          Example too large for clipboard controls. Inspect complete data or
          download the YAML.
        </p>
      )}
    </div>
  );
}

function NamedExamples({
  value,
  document,
}: {
  document: Document;
  value: unknown;
}) {
  if (!record(value))
    return <p>Named example metadata unavailable; inspect complete data.</p>;
  const entries = Object.entries(value);
  return (
    <>
      {entries.slice(0, summaryLimit).map(([name, raw]) => {
        const resolved = localValue(document, raw);
        const example = object(resolved);
        return (
          <div className="api-named-example" key={name}>
            <p>
              <strong>Named example:</strong> {name}
            </p>
            <Description raw={raw} value={example} />
            {record(resolved) &&
            Object.hasOwn(example, "value") &&
            !Object.hasOwn(example, "externalValue") ? (
              <Example value={example.value} label={`${name} example`} />
            ) : (
              <p>
                {typeof example.externalValue === "string" &&
                !Object.hasOwn(example, "value")
                  ? "External example location declared; its content is not loaded or fetched."
                  : "Example content unavailable or ambiguous; inspect complete data."}
              </p>
            )}
          </div>
        );
      })}
      {entries.length > summaryLimit && (
        <p>Additional examples remain in complete decoded data.</p>
      )}
    </>
  );
}

function Media({
  value,
  document,
}: Pick<Context, "document"> & { value: unknown }) {
  if (!record(value))
    return <p>Media-type metadata unavailable; inspect complete data.</p>;
  const entries = Object.entries(value);
  return (
    <>
      {!entries.length && <p>No media types declared.</p>}
      {entries.slice(0, summaryLimit).map(([mediaType, media]) => (
        <div className="api-media" key={mediaType}>
          <p className="api-contract-label">
            <strong>{mediaType}</strong>
          </p>
          {record(media) ? (
            <>
              <Schema value={media.schema} document={document} />
              {Object.hasOwn(media, "example") && (
                <Example value={media.example} label={`${mediaType} example`} />
              )}
              {media.examples !== undefined && (
                <NamedExamples value={media.examples} document={document} />
              )}
              {Object.hasOwn(media, "example") &&
                Object.hasOwn(media, "examples") && (
                  <p>
                    Both example forms are declared; inspect complete data
                    before choosing a payload.
                  </p>
                )}
            </>
          ) : (
            <p>Media-type details unavailable; inspect complete data.</p>
          )}
        </div>
      ))}
      {entries.length > summaryLimit && (
        <p>Additional media types remain in complete decoded data.</p>
      )}
    </>
  );
}

function Body({ value, document, onReference }: Context & { value: unknown }) {
  if (value === undefined) return <p>No request body.</p>;
  const resolved = localValue(document, value);
  return (
    <>
      {record(resolved) ? (
        <>
          <Required value={resolved.required} />
          <Description raw={value} value={resolved} />
          {resolved.content === undefined ? (
            <p>No media types declared.</p>
          ) : (
            <Media value={resolved.content} document={document} />
          )}
        </>
      ) : (
        <p>
          Request-body definition unavailable or unsupported in this contract.
        </p>
      )}
      <CompleteData
        value={value}
        related={resolved}
        onReference={onReference}
      />
    </>
  );
}

function Responses({
  value,
  document,
  onReference,
}: Context & { value: unknown }) {
  if (!record(value))
    return (
      <>
        <p>Response declarations unavailable in this contract.</p>
        {value !== undefined && (
          <CompleteData value={value} onReference={onReference} />
        )}
      </>
    );
  const entries = Object.entries(value);
  return (
    <>
      {!entries.length && <p>No responses declared.</p>}
      {entries.slice(0, summaryLimit).map(([status, raw]) => {
        const resolved = localValue(document, raw);
        const response = object(resolved);
        return (
          <div className="api-response" key={status}>
            <h4>{status}</h4>
            {record(resolved) && typeof response.description === "string" ? (
              <>
                <Description raw={raw} value={response} />
                {response.content === undefined ? (
                  <p>No response body declared.</p>
                ) : (
                  <Media value={response.content} document={document} />
                )}
                {response.headers !== undefined && (
                  <>
                    <h5>Response headers</h5>
                    {record(response.headers) ? (
                      Object.entries(response.headers)
                        .slice(0, summaryLimit)
                        .map(([name, header]) => (
                          <Field
                            key={name}
                            raw={header}
                            headerName={name}
                            document={document}
                            onReference={onReference}
                          />
                        ))
                    ) : (
                      <p>
                        Response-header metadata unavailable; inspect complete
                        data.
                      </p>
                    )}
                    {Object.keys(object(response.headers)).length >
                      summaryLimit && (
                      <p>Additional headers remain in complete decoded data.</p>
                    )}
                  </>
                )}
              </>
            ) : (
              <p>
                Response definition unavailable or unsupported in this contract.
              </p>
            )}
            <CompleteData
              value={raw}
              related={resolved}
              onReference={onReference}
              exclude={Object.values(object(response.headers))
                .slice(0, summaryLimit)
                .flatMap((header) => references(header))}
            />
          </div>
        );
      })}
      {entries.length > summaryLimit && (
        <>
          <p>Additional responses remain in complete decoded data.</p>
          <CompleteData
            value={value}
            label="Complete response declarations"
            onReference={onReference}
          />
        </>
      )}
    </>
  );
}

/** Present only declared facts, with exact-source download and current grants owned elsewhere. */
export function OperationContract({
  operation,
  document,
  onReference,
}: Context & { operation: Operation }) {
  const parameters = useRef<{
    document?: Document;
    source?: unknown[];
    next: number;
    rows: { key: string; raw: unknown }[];
  }>({ next: 0, rows: [] });
  if (
    parameters.current.document !== document ||
    parameters.current.source !== operation.parameters
  ) {
    const current = parameters.current;
    current.document = document;
    current.source = operation.parameters;
    current.rows = operation.parameters.slice(0, summaryLimit).map((raw) => {
      const fields = object(localValue(document, raw));
      return {
        // Match the projector's exact identity after inheritance/overrides.
        key:
          text(fields.in) && text(fields.name)
            ? JSON.stringify([fields.in, fields.name])
            : `unresolved:${current.next++}`,
        raw,
      };
    });
  }
  // Anonymous declarations have no stable semantic identity. Their fresh tokens
  // reset only these rows when a replacement projection arrives.
  return (
    <div className="api-operation-contract">
      <CopyReference
        label="method and path"
        value={`${operation.method} ${operation.path}`}
      />
      {operation.declaredId ? (
        <CopyReference label="operation ID" value={operation.declaredId} />
      ) : (
        <p>Operation ID unavailable or not declared.</p>
      )}
      <p className="muted">
        Copies retain literal path placeholders. Use your direct Catalog API
        origin, independently acquired credentials and current command values.
        The UI host forwarding route is not the backend API origin.
      </p>
      <section aria-label="Operation authentication">
        <h3>Authentication</h3>
        <Auth
          value={operation.security}
          document={document}
          onReference={onReference}
        />
      </section>
      <section aria-label="Operation parameters">
        <h3>Parameters</h3>
        {!operation.parameters.length && <p>No parameters.</p>}
        {parameters.current.rows.map(({ raw, key }) => (
          <Field
            key={key}
            raw={raw}
            document={document}
            onReference={onReference}
          />
        ))}
        {operation.parameters.length > summaryLimit && (
          <>
            <p>
              Additional parameter declarations remain in complete decoded data.
            </p>
            <CompleteData
              value={operation.parameters}
              label="Complete parameter declarations"
              onReference={onReference}
            />
          </>
        )}
      </section>
      <section aria-label="Operation request body">
        <h3>Request body</h3>
        <Body
          value={operation.body}
          document={document}
          onReference={onReference}
        />
      </section>
      <section aria-label="Operation responses">
        <h3>Responses</h3>
        <p className="muted">
          Response codes describe the HTTP contract. An accepted Job is not
          completed work. Control problem details, discovery envelopes and MCP
          responses retain their declared media types and schemas.
        </p>
        <Responses
          value={operation.responses}
          document={document}
          onReference={onReference}
        />
      </section>
    </div>
  );
}
