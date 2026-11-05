// Reader handoff separates governed references, retained documents and external delivery metadata.
import { Fragment } from "react";
import { safeLink } from "../api/client";
import type { Access, Client, RecordDetail, SearchResult } from "../api/client";
import { permits } from "../api/permissions";
import { permitsNamespace } from "../api/permissions";
import { documentationGuideLink } from "../api/session";
import type { useNamespaceAccessState } from "./namespace-access";
import { CopyReference } from "./CopyReference";
import { ArtifactDownload } from "./ArtifactDownload";
import { MCPClientExport } from "./MCPClientExport";
import { JSONBlock } from "./CodeBlock";
import {
  authenticationSummary,
  object,
  representationReferences,
  representationDisplay,
  representationGuide,
} from "./handoff";
import type { Representation } from "./handoff";
import "./workspace.css";

/** Display reported local assessments without promoting validation into trust or rights. */
export function LocalAssessment({
  evidence,
  compact = false,
  headingId,
}: {
  evidence: Pick<RecordDetail["evidence"], "state" | "trust" | "legalRights">;
  compact?: boolean;
  headingId?: string | undefined;
}) {
  const Heading = compact ? "h3" : "h2";
  const trust = {
    not_assessed: "Not assessed",
    unsupported: "Unsupported",
    unverified: "Unverified",
    verified: "Reported verified",
    failed: "Failed",
    expired: "Expired",
  };
  return (
    <section aria-label="Resource assessment">
      <Heading
        className="section-heading record-section-target"
        id={headingId}
        tabIndex={headingId ? -1 : undefined}
      >
        Validation and assessments
      </Heading>
      <dl className="workspace-kv">
        <dt>Validation</dt>
        <dd>{evidence.state === "complete" ? "Completed" : "Blocked"}</dd>
        <dt>Publisher trust</dt>
        <dd>{trust[evidence.trust.state]}</dd>
        <dt>Legal rights</dt>
        <dd>Not assessed</dd>
      </dl>
      <p className="muted">
        Validation completion is separate from publisher trust, legal rights and
        permission to execute. A published release or approved review is not
        proof of ownership or security.
      </p>
    </section>
  );
}

/** Bounded current attribution is not an immutable approval or a source-health signal. */
export function IndexedProvenance({
  item,
  access,
  basePath,
  headingId,
}: {
  item: SearchResult;
  access: Access | undefined;
  basePath: string;
  headingId?: string | undefined;
}) {
  if (!item.indexed) return null;
  const evidence = item.indexed;
  const manage = access && permits(access, "source.read", "sources.read");
  const query = new URLSearchParams({
    identifier: item.identifier,
    versionPresent: String(item.versionPresent),
    ...(item.versionPresent ? { version: item.version ?? "" } : {}),
  });
  return (
    <section aria-label="Indexed source provenance">
      <h3
        className="section-heading record-section-target"
        id={headingId}
        tabIndex={headingId ? -1 : undefined}
      >
        Source provenance
      </h3>
      <p>
        Reported sources: {evidence.sourceCount}. Source IDs shown:{" "}
        {evidence.sourceIds.length}.
        {evidence.sourcesTruncated &&
          " Additional source IDs were omitted from this bounded projection."}
      </p>
      <dl className="workspace-kv">
        <dt>Identity variants</dt>
        <dd>
          {evidence.conflict
            ? "Conflicting current variants"
            : "No conflict reported"}
        </dd>
        <dt>Same-source variants</dt>
        <dd>
          {evidence.sameSourceConflict
            ? "A source reports multiple current variants for this identity."
            : "None reported"}
        </dd>
      </dl>
      {evidence.sourceIds.length === 0 && <p>No source IDs supplied.</p>}
      {evidence.sourceIds.map((sourceId) => (
        <div key={sourceId}>
          <CopyReference label="Source ID" value={sourceId} />
          {manage && (
            <a href={`${basePath}/sources/${sourceId}`}>
              Open source {sourceId}
            </a>
          )}
        </div>
      ))}
      <CopyReference
        label="Observation group ID"
        value={evidence.observationGroupId}
      />
      {manage && (!item.versionPresent || !!item.version) && (
        <a href={`${basePath}/observation-group?${query}`}>
          Compare source contributions
        </a>
      )}
      <p className="muted">
        This is a current indexed projection. Source contributions can change;
        it is not immutable validation evidence or a source availability check.
      </p>
    </section>
  );
}

/** A namespace hint explains this route; the artifact read may use another authorized reference. */
function DownloadAuthority({
  state,
  namespaceId,
}: {
  state?: ReturnType<typeof useNamespaceAccessState> | undefined;
  namespaceId?: string | undefined;
}) {
  const permitted =
    state &&
    namespaceId &&
    permitsNamespace(
      state.access,
      "resource.read_content",
      "catalog.read",
      namespaceId,
    );
  return (
    <div className="notice">
      <p>
        Retained downloads require a currently authorized content reference and
        catalog.read scope when enforced. The server checks access when you
        prepare the download.
      </p>
      {state?.busy ? (
        <p role="status">
          Checking this namespace's content permission hint...
        </p>
      ) : state?.error ? (
        <p>
          Content permission hints could not be confirmed.{" "}
          <button type="button" onClick={state.refresh}>
            Retry content permission hints
          </button>
        </p>
      ) : (
        <p>
          {state
            ? permitted
              ? "Your current permission hint includes content access in this namespace."
              : "Your current permission hint does not include content access in this namespace."
            : "Content permission hints have not been confirmed."}{" "}
          A different authorized reference can allow the same document. A digest
          or readable metadata alone grants no download access.
        </p>
      )}
    </div>
  );
}

function HandoffGuide({
  kind,
  documentationUrl,
}: {
  kind: SearchResult["kind"];
  documentationUrl?: string | undefined;
}) {
  const guides = {
    agent: "agent-example",
    mcp_server: "mcp-example",
    skill: "skill-example",
    plugin: "plugin-example",
  } as const;
  const guide = documentationGuideLink(documentationUrl ?? "", guides[kind]);
  return guide ? (
    <p className="muted">
      For format, package and client setup, read{" "}
      <a href={guide.href} target="_blank" rel="noopener noreferrer">
        {guide.label} (opens in a new tab)
      </a>
      .
    </p>
  ) : null;
}

/** Show one representation's delivery facts without fetching its URL or rebuilding an archive. */
export function RepresentationHandoff({
  item,
  client,
  onFailure,
  compact = false,
  permissionState,
  namespaceId,
}: {
  item: Representation;
  client: Client;
  onFailure: (error: unknown) => void;
  compact?: boolean;
  permissionState?: ReturnType<typeof useNamespaceAccessState> | undefined;
  namespaceId?: string | undefined;
}) {
  const authentication = authenticationSummary(item);
  const display = representationDisplay(item);
  const guide = representationGuide(item);
  const Heading = compact ? "h4" : "h3";
  return (
    <section className="workspace-panel handoff">
      <Heading>
        {item.role}: {guide.title}
      </Heading>
      <p className="muted">{guide.guidance}</p>
      {guide.facts.length > 0 && (
        <dl className="workspace-kv">
          {guide.facts.map((fact) => (
            <Fragment key={fact.label}>
              <dt>{fact.label}</dt>
              <dd>{fact.value}</dd>
            </Fragment>
          ))}
        </dl>
      )}
      {display.delivery.length > 0 && (
        <p className="tags">
          {display.delivery.map((label) => (
            <span className="badge" key={label}>
              {label}
            </span>
          ))}
        </p>
      )}
      {display.attribution.length > 0 && (
        <dl className="workspace-kv">
          {display.attribution.map((fact) => (
            <Fragment key={fact.label}>
              <dt>{fact.label}</dt>
              <dd>{fact.value}</dd>
            </Fragment>
          ))}
        </dl>
      )}
      <div role="group" aria-label="Declared delivery references">
        {representationReferences(item).map((reference) => (
          <CopyReference
            key={reference.label}
            {...reference}
            open={
              reference.label === "Git repository" ||
              reference.label === "Metadata document URL"
            }
          />
        ))}
      </div>
      <p className="muted">
        {authentication ||
          "Authentication and client requirements are not summarized for this representation. Inspect the retained metadata and provider documentation before use."}
      </p>
      {item.artifactId && !item.plugin && !item.package ? (
        <>
          <DownloadAuthority
            state={permissionState}
            namespaceId={namespaceId}
          />
          <ArtifactDownload
            key={item.artifactId}
            client={client}
            id={item.artifactId}
            onFailure={onFailure}
          />
        </>
      ) : (
        <p className="muted">
          {item.plugin || item.package
            ? "The archive is not stored for download. Retained inspection documents are evidence, not the complete package. Use the declared external archive or immutable Git/OCI reference; no archive is reconstructed here."
            : item.data !== undefined
              ? "Inline metadata is available below. Formatted JSON is not an original-byte download."
              : "No retained document is available. Use the declared external handoff, if supplied."}
        </p>
      )}
      {item.data !== undefined && (
        <details>
          <summary>View decoded metadata</summary>
          <p className="muted">
            This decoded JSON display is not original bytes. Numbers may have
            rounded during decoding and whitespace is reconstructed. Use a
            verified retained document download, when available, for exact
            bytes.
          </p>
          <JSONBlock value={item.data} />
        </details>
      )}
      <details>
        <summary>Representation evidence</summary>
        <dl className="workspace-kv">
          <dt>Media type</dt>
          <dd>{item.mediaType}</dd>
          <dt>Profile</dt>
          <dd>{item.profileId || "Not supplied"}</dd>
          <dt>Digest scope</dt>
          <dd>
            {item.plugin || item.package
              ? "Inspected archive"
              : "Representation document"}
          </dd>
          <dt>Digest</dt>
          <dd>
            <code>{item.contentDigest || "Not supplied"}</code>
          </dd>
          <dt>Size</dt>
          <dd>{item.size} bytes</dd>
        </dl>
      </details>
    </section>
  );
}

/** Present immutable local identities separately so no synthetic install/configuration command is implied. */
export function LocalHandoff({
  record,
  client,
  onFailure,
  compact = false,
  headingId,
  permissionState,
  documentationUrl,
}: {
  record: RecordDetail;
  client: Client;
  onFailure: (error: unknown) => void;
  compact?: boolean;
  headingId?: string | undefined;
  permissionState?: ReturnType<typeof useNamespaceAccessState> | undefined;
  documentationUrl?: string | undefined;
}) {
  const Heading = compact ? "h3" : "h2";
  const guidance = {
    agent:
      "Use the Agent metadata with a compatible client. Check the declared interfaces and authentication requirements before connecting.",
    mcp_server:
      "Use the MCP document with a compatible client. Check its transport, package or remote endpoint and required inputs.",
    skill:
      "Use the declared immutable Git or OCI reference when supplied. A retained Skill document alone may not contain the complete Skill tree.",
    plugin:
      "Use the declared external archive and retained inspection evidence. A manifest or component document is not the complete Plugin archive.",
  };
  return (
    <section className="handoff" aria-label="Resource handoff">
      <Heading
        className="section-heading record-section-target"
        id={headingId}
        tabIndex={headingId ? -1 : undefined}
      >
        Use this resource
      </Heading>
      <p className="notice">
        Catalog discovery does not grant permission to install, deploy or run
        this resource.
      </p>
      <div className="record-links">
        {(
          [
            ["Documentation", record.listingRevision.documentationUrl],
            ["Support", record.listingRevision.supportUrl],
            ["Terms", record.listingRevision.termsUrl],
          ] as const
        ).map(
          ([label, url]) =>
            safeLink(url) && (
              <CopyReference
                key={label}
                label={label}
                value={url!}
                open
                openLabel={label}
              />
            ),
        )}
      </div>
      <p>Copy a reference for your client or retrieve a stored document.</p>
      <p className="muted">{guidance[record.resource.kind]}</p>
      <HandoffGuide
        kind={record.resource.kind}
        documentationUrl={documentationUrl}
      />
      <div role="group" aria-label="Exact resource reference">
        <CopyReference
          label="Resource identifier"
          value={record.resource.canonicalIdentifier}
        />
        <CopyReference
          label="Resource version"
          value={record.version.version}
        />
      </div>
      <MCPClientExport
        compact={compact}
        record={record}
        client={client}
        onFailure={onFailure}
        permissionState={permissionState}
      />
      {record.versionRevision.representations.map((item) => (
        <RepresentationHandoff
          key={item.id}
          item={item}
          client={client}
          onFailure={onFailure}
          compact={compact}
          permissionState={permissionState}
          namespaceId={record.resource.namespaceId}
        />
      ))}
      {record.versionRevision.representations.length === 0 && (
        <p>No representation delivery details are available in this record.</p>
      )}
    </section>
  );
}

/** Copy external identifiers with provenance; sparse indexed results contain no delivery contract. */
export function ExternalHandoff({
  item,
  headingId,
  documentationUrl,
}: {
  item: SearchResult;
  headingId?: string | undefined;
  documentationUrl?: string | undefined;
}) {
  const entry = object(item.live?.entry);
  const locator =
    typeof entry.url === "string" && entry.url.length <= 8192 ? entry.url : "";
  return (
    <section className="handoff" aria-label="External resource handoff">
      <h3
        className="section-heading record-section-target"
        id={headingId}
        tabIndex={headingId ? -1 : undefined}
      >
        Use this resource
      </h3>
      <p className="notice">
        Trust and legal rights are not assessed by this Catalog for this
        external entry. Upstream declarations are not local assessments.
      </p>
      <CopyReference label="External identifier" value={item.identifier} />
      {item.versionPresent ? (
        <CopyReference label="Declared version" value={item.version ?? ""} />
      ) : (
        <p>No version supplied; this is not an immutable version pin.</p>
      )}
      {item.live && (
        <CopyReference label="Live source" value={item.live.source} />
      )}
      {locator && (
        <CopyReference label="External metadata URL" value={locator} />
      )}
      <p>
        {item.indexed
          ? "This indexed projection does not include representation delivery details."
          : "Live references are transient declarations from the source."}{" "}
        No local release, retained download or approval is implied.
      </p>
      <p className="muted">
        Authentication requirements are not established by this search
        projection. Check the source metadata before configuring a client.
      </p>
      <HandoffGuide kind={item.kind} documentationUrl={documentationUrl} />
    </section>
  );
}
