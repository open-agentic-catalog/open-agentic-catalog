import { StatusLabel, stateLabel, durationLabel } from "./operational-labels";

import { controlIcons } from "../shell/Icon";
import { Facts } from "./Facts";
import { DateValue } from "./date-value";

import { kindLabel } from "../resource-kinds";
import type { ProcessInfo } from "../api/build-info";
import * as checks from "./responses";
import { useEffect, useRef, useState } from "react";
// Service information separates API and UI-host identity from runtime capabilities.
import { result } from "../api/client";
import type { Access, Client, Runtime } from "../api/client";
import { permits } from "../api/permissions";
import { documentationGuideLink } from "../api/session";
import { CopyText } from "./CopyReference";
import { diagnosticDetails } from "./diagnostic-details";
import { EvidenceDetails } from "./authoring";
import SemanticAdmin from "./SemanticAdmin";
import { Heading, ReadState, useRead, confirmDiscard } from "./workspace";

/** Read connected service version and declared capabilities without using worker operations. */
export default function Service({
  client,
  runtime,
  access,
  onFailure,
}: {
  client: Client;
  runtime: Runtime;
  access: Access;
  onFailure: (e: unknown) => void;
}) {
  const version = useRead(
    "service-information",
    async (signal) => ({
      ...result(
        await client.api.GET("/api/v1/service-information", { signal }),
        checks.ServiceInformation,
      ),
      receivedAt: performance.now(),
    }),
    onFailure,
  );
  const uiBuild = useRead(
    "ui-process",
    async (signal) => ({
      ...(await client.processInfo(signal)),
      receivedAt: performance.now(),
    }),
    onFailure,
  );
  const capabilities = useRead(
    "capabilities",
    async (signal) =>
      result(
        await client.api.GET("/api/v1/capabilities", { signal }),
        checks.Capabilities,
      ),
    onFailure,
  );
  const [displayTime, setDisplayTime] = useState(() => performance.now());
  useEffect(() => {
    const timer = window.setInterval(
      () => setDisplayTime(performance.now()),
      30_000,
    );
    return () => window.clearInterval(timer);
  }, []);
  const indexing = useRef<HTMLElement>(null);
  const anchorHandled = useRef(false);
  useEffect(() => {
    // Native fragments can resolve before this lazy view and its reads finish.
    const arrive = () => {
      if (
        anchorHandled.current ||
        location.hash !== "#search-indexing" ||
        version.busy ||
        capabilities.busy
      )
        return;
      anchorHandled.current = true;
      // The target section is rendered unconditionally before this effect runs.
      indexing.current!.focus({ preventScroll: true });
      indexing.current!.scrollIntoView({ block: "start" });
    };
    const changed = () => {
      anchorHandled.current = false;
      arrive();
    };
    arrive();
    window.addEventListener("hashchange", changed);
    return () => window.removeEventListener("hashchange", changed);
  }, [version.busy, capabilities.busy]);
  const data = capabilities.data;
  const [rebuildOffered, setRebuildOffered] = useState(false);
  const [rebuildEpoch, setRebuildEpoch] = useState(0);
  if (
    !rebuildOffered &&
    data?.search.semantic === "enabled" &&
    permits(access, "operations.reindex", "tenant.admin")
  )
    setRebuildOffered(true);
  const observationsGuide = documentationGuideLink(
    runtime.documentationUrl,
    "observability",
  );
  const indexingGuide = documentationGuideLink(
    runtime.documentationUrl,
    "hybrid-search",
  );
  const diagnostics = diagnosticDetails(version.data, uiBuild.data, {
    api: version.problem,
    ui: uiBuild.problem,
  });
  return (
    <>
      <Heading
        title="Service information"
        icon="book"
        description="View the API and UI versions, running processes and available features."
      />
      <button
        disabled={version.busy || capabilities.busy || uiBuild.busy}
        onClick={() => {
          if (!confirmDiscard()) return;
          setRebuildEpoch((epoch) => epoch + 1);
          version.refresh();
          uiBuild.refresh();
          capabilities.refresh();
        }}
      >
        {controlIcons["refresh"]} Refresh
      </button>
      <p className="muted">
        Uptime was measured with the last response. Refresh to update it.
      </p>
      <details className="workspace-advanced">
        <summary>About these process details</summary>
        <p>
          These details describe the API and UI processes that answered this
          page. Refresh may reach another replica. The UI host runs separately
          from the API.
        </p>
        <p>
          Last received is the time since this browser received each response.
          These details do not show worker health, every replica or upgrade
          history. A process start time is not an upgrade date.
        </p>
      </details>
      {observationsGuide && (
        <p>
          <a
            href={observationsGuide.href}
            target="_blank"
            rel="noopener noreferrer"
          >
            {observationsGuide.label}
            <span className="sr-only"> (opens in a new tab)</span>
          </a>
        </p>
      )}
      <ReadState {...version} />
      {version.data && (
        <section className="workspace-panel">
          <h2>Catalog API</h2>
          <ProcessTiming
            info={version.data}
            displayTime={displayTime}
            label="API"
          />
          <details>
            <summary>API build details</summary>
            <Facts
              rows={[
                ["Commit", <code>{version.data.commit}</code>],
                ["Source state", version.data.sourceState],
                ["ARD profile", version.data.ardProfile],
                ["AI Catalog profile", version.data.aiCatalogProfile],
              ]}
            />
          </details>
        </section>
      )}
      <ReadState {...uiBuild} />
      {uiBuild.data && (
        <section className="workspace-panel">
          <h2>UI host</h2>
          <ProcessTiming
            info={uiBuild.data}
            displayTime={displayTime}
            label="UI host"
          />
          <details>
            <summary>UI build details</summary>
            <Facts
              rows={[
                ["Commit", <code>{uiBuild.data.commit}</code>],
                ["Source state", uiBuild.data.sourceState],
              ]}
            />
          </details>
        </section>
      )}
      <ReadState {...capabilities} />
      <EvidenceDetails title="Support details preview" value={diagnostics} />
      <CopyText
        label="Copy support details"
        text={JSON.stringify(diagnostics, null, 2)}
      />
      <p>
        <a href={`${runtime.basePath}/troubleshooting`}>
          Troubleshooting and diagnostic help
        </a>
      </p>
      <section className="workspace-panel">
        <h2>API reference</h2>
        <p>
          The OpenAPI document served by this Catalog, including request types,
          scopes and responses.
        </p>
        <a className="button" href={`${runtime.basePath}/api-reference`}>
          Browse API reference
        </a>
      </section>
      <section
        className="workspace-panel"
        id="search-indexing"
        ref={indexing}
        tabIndex={-1}
        aria-label="Search indexing"
      >
        <h2>Search indexing</h2>
        {indexingGuide && (
          <p>
            <a
              href={indexingGuide.href}
              target="_blank"
              rel="noopener noreferrer"
            >
              {indexingGuide.label}
              <span className="sr-only"> (opens in a new tab)</span>
            </a>
          </p>
        )}
        {data ? (
          <>
            <p>
              Semantic search:{" "}
              <StatusLabel state={data.search.semantic} tone="neutral" />.
              Rebuild search data without changing Catalog records or published
              listings.
            </p>
            {data.search.semantic === "disabled" ? (
              <p>
                Semantic indexing is turned off. Keyword search is available.
                Ask your operator to enable semantic indexing.
              </p>
            ) : data.search.semantic === "unavailable" ? (
              <p>
                Semantic indexing is unavailable in this deployment. Keyword
                search is available. Ask your operator to check the supported
                deployment capabilities.
              </p>
            ) : (
              !permits(access, "operations.reindex", "tenant.admin") && (
                <p>
                  Rebuilding requires operations.reindex permission and
                  tenant.admin when scopes are enforced. The capability being
                  enabled does not grant that permission.
                </p>
              )
            )}
            {data.search.indexing &&
              permits(access, "operations.reindex", "tenant.admin") && (
                <section aria-label="Catalog index status">
                  <h3>Catalog index status</h3>
                  <p>
                    Index status:{" "}
                    <StatusLabel
                      state={data.search.indexing.state}
                      tone={
                        data.search.indexing.state === "failed"
                          ? "danger"
                          : data.search.indexing.state === "building"
                            ? "info"
                            : data.search.indexing.state === "ready"
                              ? "success"
                              : "neutral"
                      }
                    />
                    . {data.search.indexing.indexed} items indexed;{" "}
                    {data.search.indexing.failed} items remain keyword-only
                    after an input error.
                  </p>
                  <p className="muted">
                    These counts cover this tenant's Catalog search. Refresh to
                    update progress. Public discovery snapshots have separate
                    indexes.
                  </p>
                  {data.search.indexing.jobId &&
                    permits(access, "job.read", "catalog.read") && (
                      <a
                        className="button"
                        href={`${runtime.basePath}/jobs/${data.search.indexing.jobId}`}
                      >
                        View current indexing Job
                      </a>
                    )}
                </section>
              )}
          </>
        ) : (
          <p>
            Indexing controls require a successful capability read. Use Refresh
            to retry.
          </p>
        )}
        {rebuildOffered && (
          <SemanticAdmin
            key={rebuildEpoch}
            access={access}
            client={client}
            runtime={runtime}
            ready={
              data?.search.semantic === "enabled" &&
              !capabilities.busy &&
              !capabilities.error
            }
            onFailure={onFailure}
          />
        )}
      </section>
      {data && (
        <>
          <section className="workspace-panel">
            <h2>Capabilities</h2>
            <p className="muted">
              These features are configured for this deployment. Your
              permissions determine which actions you can use.
            </p>
            <Facts
              rows={[
                [
                  "Resource kinds",
                  data.resourceKinds.map(kindLabel).join(", "),
                ],
                ["Search profile", data.search.profile],
                [
                  "Search using a fallback",
                  data.search.degraded ? "Yes" : "No",
                ],
                [
                  "Semantic search",
                  <StatusLabel state={data.search.semantic} tone="neutral" />,
                ],
                [
                  "Last reported embedding provider status",
                  data.search.providerState ? (
                    <StatusLabel
                      state={data.search.providerState}
                      tone={
                        data.search.providerState === "healthy"
                          ? "success"
                          : data.search.providerState === "degraded"
                            ? "warning"
                            : "neutral"
                      }
                    />
                  ) : (
                    "Not reported"
                  ),
                ],
                ...Object.entries(data.optionalCapabilities).map(
                  ([key, value]) => entry(key, value),
                ),
              ]}
            />
            <p className="muted">
              Turned off means a feature is disabled; unavailable means it is
              not offered. Search may fall back to keywords. Provider status is
              the API process's last report, not a new health check. Unknown or
              not reported does not establish provider health.
            </p>
          </section>
          <section className="workspace-panel">
            <h2>Standards profiles</h2>
            <Facts
              rows={[
                ...Object.entries(data.profiles).map(([key, value]) =>
                  entry(key, value.join(", ")),
                ),
                [
                  "Profile bundle digest",
                  <code>{data.profileBundle.digest}</code>,
                ],
                ["Profile artifacts", data.profileBundle.artifacts],
              ]}
            />
          </section>
          <section className="workspace-panel">
            <h2>Governance</h2>
            <Facts
              rows={Object.entries(data.governance).map(([key, value]) =>
                entry(key, value),
              )}
            />
          </section>
          {!!data.degradations.length && (
            <section className="workspace-panel">
              <h2>Degradations</h2>
              {data.degradations.map((item, index) => (
                <p className="notice" key={index}>
                  {item.component}: {item.reason}
                </p>
              ))}
            </section>
          )}
          <details className="workspace-panel">
            <summary>Configured limits</summary>
            <Facts
              rows={Object.entries(data.limits).map(([key, value]) =>
                entry(key, value, true),
              )}
            />
          </details>
          <details className="workspace-panel">
            <summary>Supported operation families</summary>
            <ul>
              {data.operations.map((value) => (
                <li key={value}>{value}</li>
              ))}
            </ul>
          </details>
        </>
      )}
    </>
  );
}

/** Render a service fact as escaped text in the shared definition-list layout. */
function entry(name: string, value: string | number, limit = false) {
  const labels: Record<string, string> = {
    credentialResolution: "Credential resolution",
    serviceActivity: "Service activity API",
    managedMedia: "Managed media",
    softwareAssessment: "Software assessment",
    webhooks: "Webhooks",
    ard: "ARD",
    aiCatalog: "AI Catalog",
    mcp: "MCP",
    publicationAuthorization: "Publication authorization",
    publisherAuthority: "Publisher authority",
    legalRights: "Legal rights",
    requestBodyBytes: "Request body",
    publicationConcurrentConstructions: "Concurrent snapshot constructions",
    validationMaxFindings: "Validation findings per result",
    validationMaxDiagnosticBytes: "Validation diagnostic size",
    catalogDocumentBytes: "Catalog document size",
    pluginDocumentBytes: "Plugin document size",
    pluginComponents: "Components per Plugin",
    skillPackageBytes: "Skill archive size",
    skillPackageExpandedBytes: "Expanded Skill package size",
    skillPackageFileBytes: "Skill file size",
    skillPackageFiles: "Files per Skill package",
    skillPackageDepth: "Skill package path depth",
    skillPackageExpansionRatio: "Skill archive expansion ratio",
    catalogPageSize: "Catalog page size",
    ardPageSize: "ARD page size",
    liveFederationPageSize: "Live federation page size",
    publicationRecords: "Records per publication",
    publicationProjectionBytes: "Publication projection size",
    publicationARDEntries: "ARD entries per publication",
    publicationAICatalogEntries: "AI Catalog entries per publication",
    publicationExclusions: "Exclusions per publication",
    publicationArtifactReferences: "Artifact references per publication",
    publicationDocumentBytes: "Publication document size",
    publicationSnapshotBytes: "Publication snapshot size",
    publicationWorkUnits: "Work units per publication",
  };
  const label =
    labels[name] ??
    name
      .replace(/([a-z])([A-Z])/g, "$1 $2")
      .replace(/^./, (char) => char.toUpperCase());
  const unit = !limit
    ? ""
    : name.endsWith("Bytes")
      ? " bytes"
      : name.endsWith("PageSize")
        ? " items per page"
        : name === "skillPackageExpansionRatio"
          ? ":1"
          : "";
  return [
    label,
    <>
      {typeof value === "string" ? stateLabel(value) : value}
      {unit}
    </>,
  ] as const;
}

/** Display a complete server observation; do not infer elapsed time from UTC dates. */
function ProcessTiming({
  info,
  displayTime,
  label,
}: {
  info: ProcessInfo & { receivedAt: number; version: string };
  displayTime: number;
  label: string;
}) {
  const ageMinutes = Math.max(
    0,
    Math.floor((displayTime - info.receivedAt) / 60_000),
  );
  const uptime = durationLabel(info.uptimeSeconds);
  return (
    <Facts
      rows={[
        ["Version", info.version],
        ["Process started", <DateValue value={info.startedAt} />],
        ["Measured uptime", uptime],
        [
          "Instance ID",
          <>
            <code>{info.instanceId}</code>
            <CopyText
              label={`Copy ${label} instance ID`}
              text={info.instanceId}
            />
          </>,
        ],
        [
          "Last received",
          ageMinutes < 1
            ? "Less than a minute ago"
            : `${ageMinutes} minute${ageMinutes === 1 ? "" : "s"} ago`,
        ],
        ["Measured at", <DateValue value={info.observedAt} />],
      ]}
    />
  );
}
