import { kindChoices } from "../resource-kinds";
import { commandSummary } from "./workspace";
import { SourceState, retrievalLabels, stateLabel } from "./operational-labels";

import { controlIcons } from "../shell/Icon";
import { Facts } from "./Facts";
import { SourceTopology } from "./SourceTopology";
import { SourceConfiguration } from "./SourceConfiguration";
import { ExactReference } from "./ExactReference";
import { useExpired } from "./use-expired";
import { CopyReference } from "./CopyReference";
import { characterText } from "./input-validation";
import { ValueList } from "./ValueList";
import * as checks from "./responses";
// Source administration binds connection and configuration changes to retained inspection/preview evidence.
import { useEffect, useRef, useState } from "react";
import { sourceStatus, sourceDirectoryURL } from "./source-directory-state";
import { DateValue } from "./date-value";

import type { components } from "../api/schema";
import { result, acknowledge } from "../api/client";
import { permits } from "../api/permissions";
import { ActionForm, EvidenceDetails } from "./authoring";
import type { WorkspaceProps } from "./workspace";
import {
  Field,
  Heading,
  Pager,
  ReadState,
  Reason,
  confirmDiscard,
  useRead,
} from "./workspace";

import { SourceSnapshots } from "./SourceSnapshots";
import { SourceTrust } from "./SourceTrust";
import { canReadTrust } from "./trust-result";
import { canRequestTrust } from "./trust-selection";
import { CurrentRead, useCurrentRead } from "./current-read";
import {
  sourceActionChoices,
  sourceActionLabels,
  sourceActionReason,
  type SourceAction,
} from "./source-actions";
import {
  sourceScope,
  topologyInput,
  trustPolicyName,
} from "./federation-inputs";
import type { Scope, Settings } from "./federation-inputs";

type Schema = components["schemas"];

/** Preserve exact scope values; only removing the last row removes the filter. */
export function ScopeInput({
  value,
  onChange,
  label,
  hint,
  field: configuredField,
}: {
  value: string[];
  onChange: (items: string[]) => void;
  label: string;
  hint: string;
  field?: string;
}) {
  const field =
    configuredField ??
    (label === "Excluded identifiers"
      ? "excludedIdentifiers"
      : label.replace("Scope ", ""));
  const bounds =
    field === "identifiers" || field === "excludedIdentifiers"
      ? [1000, 512]
      : field === "versions"
        ? [100, 128]
        : field === "publishers"
          ? [100, 255]
          : [100, 80];
  const itemLabels: Record<string, string> = {
    excludedIdentifiers: "Excluded identifier",
    identifiers: "Scope identifier",
    kinds: "Scope kind",
    publishers: "Scope publisher",
    tags: "Scope tag",
    versions: "Scope version",
  };
  return (
    <ValueList
      id={`scope-${field}`}
      label={label}
      itemLabel={itemLabels[field]!}
      hint={hint}
      value={value}
      onChange={onChange}
      clearable={field === "excludedIdentifiers"}
      maximum={bounds[0]!}
      length={bounds[1]!}
    />
  );
}
const format = (value: unknown) => JSON.stringify(value, null, 2);

/** Keep explicit inspection authority separate from source metadata reads. */
export default function Federation(props: WorkspaceProps) {
  return (
    <>
      <Heading
        title="Federation sources"
        icon="globe"
        description="Inspect and govern external catalog metadata. Connecting a source does not approve or install its contents."
      />
      {props.id === "new" ? (
        <Inspect {...props} />
      ) : !permits(props.access, "source.read", "sources.read") ? (
        <p role="alert">
          The source.read permission is required. When scopes are enforced,
          sources.read is also required.
        </p>
      ) : props.id ? (
        <SourceDetail key={props.id} {...props} />
      ) : (
        <SourceList {...props} />
      )}
    </>
  );
}

/** Page configured source summaries without triggering inspection or synchronization. */
function SourceList(props: WorkspaceProps) {
  const [filters, setFilters] = useState(() => ({
    displayName: "",
    status: sourceStatus(location.search),
  }));
  const [draft, setDraft] = useState(filters);
  const [cursor, setCursor] = useState("");
  useEffect(() => {
    const moved = () => {
      if (location.pathname !== `${props.runtime.basePath}/sources`) return;
      const next = { displayName: "", status: sourceStatus(location.search) };
      setFilters(next);
      setDraft(next);
      setCursor("");
    };
    window.addEventListener("popstate", moved);
    return () => window.removeEventListener("popstate", moved);
  }, [props.runtime.basePath]);
  const page = useRead(
    JSON.stringify([filters, cursor]),
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/federation/sources", {
          params: {
            query: {
              limit: 25,
              ...(filters.displayName
                ? { displayName: filters.displayName }
                : {}),
              ...(filters.status
                ? { status: filters.status as "active" | "paused" | "error" }
                : {}),
              ...(cursor ? { cursor: cursor } : {}),
            },
          },
          signal,
        }),
        checks.FederationSourcePage,
      ),
    props.onFailure,
  );
  return (
    <>
      <form
        className="workspace-toolbar"
        aria-label="Source filters"
        onSubmit={(event) => {
          event.preventDefault();
          setFilters(draft);
          setCursor("");
          history.pushState(
            null,
            "",
            sourceDirectoryURL(props.runtime.basePath, draft.status),
          );
        }}
      >
        <Field
          label="Source name (exact match)"
          hint="Enter the complete saved name, including capital letters and spaces. More than one source can have this name."
        >
          <input
            maxLength={320}
            pattern=".{0,160}"
            value={draft.displayName}
            onChange={(event) =>
              setDraft({ ...draft, displayName: event.target.value })
            }
          />
        </Field>
        <Field label="Source status">
          <select
            value={draft.status}
            onChange={(event) =>
              setDraft({ ...draft, status: event.target.value })
            }
          >
            {draft.status &&
              !["active", "paused", "error"].includes(draft.status) && (
                <option value={draft.status} disabled>
                  Unsupported status
                </option>
              )}
            <option value="">All sources (except removed)</option>
            <option value="active">Active</option>
            <option value="paused">Paused</option>
            <option value="error">Error</option>
          </select>
        </Field>
        <div className="workspace-actions">
          <button>Apply source filters</button>
          <button
            type="button"
            onClick={() => {
              const empty = { displayName: "", status: "" };
              setDraft(empty);
              setFilters(empty);
              setCursor("");
              history.pushState(
                null,
                "",
                sourceDirectoryURL(props.runtime.basePath, ""),
              );
            }}
          >
            Clear source filters
          </button>
        </div>
      </form>
      <p>
        Filters: name{" "}
        {filters.displayName ? JSON.stringify(filters.displayName) : "any"};
        status {filters.status || "all nonremoved"}. Filtering reads saved
        metadata only.
      </p>
      <ExactReference
        basePath={props.runtime.basePath}
        path="sources"
        label="Source"
      />
      <div className="workspace-toolbar">
        {permits(props.access, "source.inspect", "sources.manage") && (
          <a href={`${props.runtime.basePath}/sources/new`}>
            Inspect a new source
          </a>
        )}
        <a href={`${props.runtime.basePath}/observations`}>Observations</a>
        {permits(props.access, "federation.discover", "sources.read") && (
          <a href={`${props.runtime.basePath}/federation-search`}>
            Search diagnostics
          </a>
        )}
        <button
          disabled={page.busy}
          onClick={() =>
            page.error && cursor ? setCursor("") : page.refresh()
          }
        >
          {controlIcons["refresh"]} Refresh
        </button>
      </div>
      {!permits(props.access, "source.inspect", "sources.manage") && (
        <p>
          Inspecting a destination requires source.inspect and sources.manage
          scope when enforced.
        </p>
      )}
      {!permits(props.access, "federation.discover", "sources.read") && (
        <p>
          Search diagnostics requires federation.discover and sources.read scope
          when enforced.
        </p>
      )}
      <ReadState
        {...page}
        refresh={() => (page.error && cursor ? setCursor("") : page.refresh())}
      />
      {page.data?.items.map((source) => (
        <section className="workspace-panel" key={source.id}>
          <h2>
            <a href={`${props.runtime.basePath}/sources/${source.id}`}>
              {source.displayName}
            </a>
          </h2>
          <Facts
            rows={[
              ["Configured state", <SourceState state={source.status} />],
              ["Search mode", retrievalLabels[source.retrievalMode]],
              ["Profile", source.profileId],
            ]}
          />
          <CopyReference label="Source ID" value={source.id} />
          <Facts
            rows={[
              ["Endpoint", <code>{source.endpoint}</code>],
              [
                "Last sync attempt finished",
                source.lastSyncAt ? (
                  <DateValue value={source.lastSyncAt} />
                ) : (
                  "No recorded synchronization"
                ),
              ],
              [
                "Last complete snapshot",
                source.lastCompleteSnapshotId ? (
                  <a
                    href={`${props.runtime.basePath}/source-snapshots/${source.lastCompleteSnapshotId}`}
                  >
                    Inspect last complete snapshot
                  </a>
                ) : (
                  "No complete snapshot recorded"
                ),
              ],
            ]}
          />
          <a href={`${props.runtime.basePath}/sources/${source.id}`}>
            Synchronization history
          </a>
          <p className="muted">
            The last attempt may have failed. A saved snapshot does not confirm
            that this source is currently available or up to date.
          </p>
          {source.lastError && <p className="notice">{source.lastError}</p>}
        </section>
      ))}
      {page.data && !page.data.items.length && (
        <p>
          {filters.displayName || filters.status
            ? "No matching sources."
            : "No sources on this page."}
        </p>
      )}
      <Pager
        label="Source directory pages"
        busy={page.busy}
        cursor={cursor}
        nextCursor={page.data?.nextCursor}
        onPage={setCursor}
      />
    </>
  );
}

/** Prepare an explicit inspection of the administrator-entered source destination. */
function Inspect(props: WorkspaceProps) {
  const [url, setURL] = useState("");
  const [networkProfile, setNetwork] = useState("");
  const [credentialBinding, setBinding] = useState("");
  const [inspection, setInspection] =
    useState<Schema["FederationSourceInspection"]>();
  return (
    <>
      <ActionForm
        title="Inspect source"
        icon="search"
        description="Check the source endpoint before connecting it. The destination must be allowed by the deployment's network policy. Credential references name configured secrets; do not enter the secrets themselves."
        allowed={permits(props.access, "source.inspect", "sources.manage")}
        editable={permits(props.access, "source.inspect", "sources.manage")}
        unavailableReason="Inspecting a destination requires source.inspect and sources.manage scope when enforced."
        dirty={!!url || !!networkProfile || !!credentialBinding}
        onFailure={props.onFailure}
        prepare={() => {
          const body = {
            url: url.trim(),
            ...(networkProfile.trim()
              ? { networkProfile: networkProfile.trim() }
              : {}),
            ...(credentialBinding.trim()
              ? { credentialBinding: credentialBinding.trim() }
              : {}),
          };
          return {
            summary: commandSummary([
              ["Action", "Inspect source"] as const,
              ["Destination", body.url] as const,
              [
                "Network profile",
                body.networkProfile ?? "Deployment default",
              ] as const,
              [
                "Credential binding",
                body.credentialBinding
                  ? "Configured deployment reference (value withheld)"
                  : "None",
              ] as const,
            ]),
            run: async (key) => {
              const value = result(
                await props.client.api.POST(
                  "/api/v1/federation/source-inspections",
                  { params: { header: { "Idempotency-Key": key } }, body },
                ),
                checks.FederationSourceInspection,
              );
              setInspection(value);
              return "Inspection retained. Review its resolved endpoint, policy and expiry before connecting.";
            },
          };
        }}
      >
        <Field
          label="Source URL"
          hint="The remote Catalog or discovery endpoint to inspect before connecting. Only destinations allowed by this deployment can be contacted."
        >
          <input
            required
            type="url"
            value={url}
            onChange={(e) => setURL(e.target.value)}
          />
        </Field>
        <Field
          label="Network policy reference"
          hint="Optional name of an administrator-configured outbound network policy. Leave blank to use the deployment default; this is not an IP address."
        >
          <input
            maxLength={63}
            value={networkProfile}
            onChange={(e) => setNetwork(e.target.value)}
          />
        </Field>
        <Field
          label="Credential reference"
          hint="Optional configured credential reference for this source. Enter its name, never the token, password or secret itself."
        >
          <input
            autoComplete="off"
            maxLength={128}
            value={credentialBinding}
            onChange={(e) => setBinding(e.target.value)}
          />
        </Field>
      </ActionForm>
      {inspection && (
        <CopyReference label="Inspection ID" value={inspection.id} />
      )}
      {inspection && permits(props.access, "source.read", "sources.read") && (
        <a href={`${props.runtime.basePath}/inspections/${inspection.id}`}>
          Review inspection and connect
        </a>
      )}
    </>
  );
}

/** Check administrative access before reading retained inspection evidence. */
export function Inspection(props: WorkspaceProps) {
  return (
    <>
      <Heading
        title="Source inspection"
        icon="globe"
        description="Review the exact resolved endpoint and retained inspection evidence."
      />
      {permits(props.access, "source.read", "sources.read") ? (
        <InspectionRead {...props} />
      ) : (
        <p role="alert">
          The source.read permission is required. When scopes are enforced,
          sources.read is also required.
        </p>
      )}
    </>
  );
}
/** Read one inspection and offer connection based on that retained evidence. */
function InspectionRead(props: WorkspaceProps) {
  const [replacement, setReplacement] = useState<{
    forId: string;
    value: Schema["FederationSourceInspection"];
  }>();
  const read = useRead(
    props.id,
    async (signal) =>
      result(
        await props.client.api.GET(
          "/api/v1/federation/source-inspections/{inspectionId}",
          { params: { path: { inspectionId: props.id } }, signal },
        ),
        checks.FederationSourceInspection,
      ),
    props.onFailure,
  );
  const inspection =
    read.data &&
    (replacement?.forId === props.id ? replacement.value : read.data);
  return (
    <>
      <ReadState {...read} />
      {inspection && (
        <>
          <section className="workspace-panel">
            <h2>Inspection summary</h2>
            <CopyReference label="Inspection ID" value={inspection.id} />
            <Facts
              rows={[
                ["Resolved endpoint", inspection.resolvedUrl],
                ["Profile", inspection.profileId],
                ["Entries found", inspection.entryCount],
                [
                  "Inspection expires",
                  <DateValue value={inspection.expiresAt} />,
                ],
              ]}
            />
            <p>
              Inspection checks this endpoint under the current policy. It does
              not connect the source, approve its content or authorize live
              query disclosure.
            </p>
            {inspection.warnings?.map((warning, index) => (
              <p className="notice" key={index}>
                {warning.code}: {warning.message}
              </p>
            ))}
          </section>
          <EvidenceDetails
            title="Resolved endpoint, capabilities and policy"
            value={inspection}
          />
          <Connect
            {...props}
            inspection={inspection}
            onInspection={(value) => setReplacement({ forId: props.id, value })}
          />
        </>
      )}
    </>
  );
}

/** Collect explicit source scope, participation and disclosure settings for a prepared command. */
function SettingsFields({
  value,
  onChange,
}: {
  value: Settings;
  onChange: (v: Settings) => void;
}) {
  const change = <K extends keyof Settings>(key: K, next: Settings[K]) =>
    onChange({ ...value, [key]: next });
  return (
    <>
      <Field
        label="Display source artwork"
        hint="Allow publisher artwork and native icons from operator-approved image origins. Images use credential-free requests; this does not approve or execute resources."
      >
        <input
          type="checkbox"
          checked={value.artworkEnabled || false}
          onChange={(event) => change("artworkEnabled", event.target.checked)}
        />
      </Field>
      <Field label="Display name">
        <input
          required
          maxLength={320}
          value={value.displayName}
          onChange={(e) => change("displayName", e.target.value)}
        />
      </Field>
      <Field
        label="ANS verification policy"
        hint="Enter a policy name supplied by your operator. Leave blank to clear the selection. Keys and verification activation are deployment settings."
      >
        <input
          maxLength={63}
          pattern={"[a-z0-9]([a-z0-9\\-]{0,61}[a-z0-9])?"}
          value={value.trustVerificationPolicy || ""}
          onChange={(e) => change("trustVerificationPolicy", e.target.value)}
        />
      </Field>
      <Field
        label="Search mode"
        hint="Indexed stores searchable metadata locally. Live sends permitted queries to the source. Hybrid supports both; live disclosure is controlled separately below."
      >
        <select
          value={value.retrievalMode}
          onChange={(e) =>
            change("retrievalMode", e.target.value as Settings["retrievalMode"])
          }
        >
          <option value="indexed">Indexed metadata</option>
          <option value="live">Live queries</option>
          <option value="hybrid">Indexed and live</option>
        </select>
      </Field>
      <Field
        label="Synchronization"
        hint="Manual synchronization starts when requested. Automatic synchronization schedules source updates. Both still require separate review before local publication."
      >
        <select
          value={value.updateMode}
          onChange={(e) =>
            change("updateMode", e.target.value as Settings["updateMode"])
          }
        >
          <option value="manual_review">Manual synchronization</option>
          <option value="automatic">Automatic source updates</option>
        </select>
      </Field>
      <fieldset className="source-settings-group">
        <legend>Metadata to include</legend>
        <p>
          Add at least one filter. Entries must match every filter group.
          Excluded identifiers are always omitted.
        </p>
        <details>
          <summary>How scope filters match</summary>
          <p>
            Identifiers, versions, kinds and publishers match any listed value.
            All listed tags must match. Publishers are domains from AIR
            identifiers, such as remote.example.
          </p>
        </details>
        {(["identifiers", "versions", "publishers", "tags"] as const).map(
          (key) => (
            <ScopeInput
              key={key}
              field={key}
              label={
                {
                  identifiers: "Identifiers",
                  versions: "Versions",
                  publishers: "Publisher domains",
                  tags: "Tags",
                }[key]
              }
              hint={`Add exact ${key}. Remove every row to omit this group; at least one inclusion group must remain. Identifiers, versions and publisher domains also have UTF-8 byte limits.`}
              value={value.scope[key] || []}
              onChange={(items) => {
                const next = { ...value.scope };
                if (items.length) Object.assign(next, { [key]: items });
                else delete next[key];
                change("scope", next);
              }}
            />
          ),
        )}
        <ScopeInput
          field="excludedIdentifiers"
          label="Excluded identifiers"
          hint="Omit these exact identifiers from this source, including every version. Each identifier permits 512 UTF-8 bytes. Exclusions take precedence over inclusion filters. Clear every row to remove exclusions. Other sources and already imported local resources are unchanged."
          value={value.scope.excludedIdentifiers || []}
          onChange={(items) => {
            const next = { ...value.scope };
            if (items.length) next.excludedIdentifiers = items;
            else delete next.excludedIdentifiers;
            change("scope", next);
          }}
        />
        <fieldset className="relationship-row">
          <legend>Resource kinds to include</legend>
          <p className="muted">
            Leave all unchecked to apply no kind filter. Select at least one
            kind or fill another scope filter above.
          </p>
          <div className="event-choices">
            {kindChoices.map(({ value: kind, label }) => (
              <label className="event-choice" key={kind}>
                <input
                  type="checkbox"
                  checked={value.scope.kinds?.includes(kind) || false}
                  onChange={(e) => {
                    const kinds = new Set(value.scope.kinds || []);
                    if (e.target.checked) kinds.add(kind);
                    else kinds.delete(kind);
                    const next = { ...value.scope };
                    if (kinds.size) next.kinds = [...kinds];
                    else delete next.kinds;
                    change("scope", next);
                  }}
                />
                {controlIcons[kind]} {label}
              </label>
            ))}
          </div>
        </fieldset>
      </fieldset>
      <fieldset className="source-settings-group">
        <legend>Live query disclosure</legend>
        <p>
          Live requests send query text and filters to approved upstreams.
          Anonymous results may publicly disclose source identity and origins.
        </p>
        {(
          [
            "enabled",
            "referralOnly",
            "queryPrivacyAcknowledged",
            "anonymousDisclosureAcknowledged",
          ] as const
        ).map((key) => (
          <Field
            key={key}
            {...(key === "enabled"
              ? {
                  hint: "Allow live requests when retrieval mode and discovery audience settings permit them.",
                }
              : {})}
            {...(key === "referralOnly"
              ? {
                  help: "Exclude this source from initial live selection. It can still participate as an observed referral when independently approved in the source topology; live queries must also be enabled.",
                }
              : {})}
            label={
              {
                enabled: "Enable live queries",
                referralOnly: "Only participate as an approved referral",
                queryPrivacyAcknowledged:
                  "I acknowledge queries are sent to this upstream",
                anonymousDisclosureAcknowledged:
                  "I acknowledge anonymous results may disclose upstream identity and origins",
              }[key]
            }
          >
            <input
              type="checkbox"
              checked={!!value.live?.[key]}
              onChange={(e) =>
                change("live", { ...value.live, [key]: e.target.checked })
              }
            />
          </Field>
        ))}
        <Field
          label="Referral traversal"
          hint="Live queries use the setting above. This choice controls whether a query may follow approved referrals."
          help="Approved referrals permits bounded referral traversal. Every contacted referral still needs independent source and topology approval; a returned referral alone grants no permission."
        >
          <select
            value={value.live?.federationMode || "none"}
            onChange={(e) =>
              change("live", {
                ...value.live,
                federationMode: e.target.value as "none" | "referrals",
              })
            }
          >
            <option value="none">Do not follow referrals</option>
            <option value="referrals">Follow approved referrals</option>
          </select>
        </Field>
      </fieldset>
    </>
  );
}

const defaults: Settings = {
  displayName: "",
  trustVerificationPolicy: "",
  artworkEnabled: false,
  retrievalMode: "indexed",
  updateMode: "manual_review",
  scope: { kinds: ["agent", "mcp_server", "skill", "plugin"] },
  live: { enabled: false, federationMode: "none" },
};
/** Bind a new source connection to the selected inspection and administrator settings. */
function Connect(
  props: WorkspaceProps & {
    inspection: Schema["FederationSourceInspection"];
    onInspection: (value: Schema["FederationSourceInspection"]) => void;
  },
) {
  const [settings, setSettings] = useState<Settings>(defaults);
  const initialTopology = format({
    referrals: { enabled: false },
    nesting: { enabled: false },
    discoveryAssignments: [],
  });
  const [topology, setTopology] = useState(initialTopology);
  const [reason, setReason] = useState("");
  const [sourceID, setSourceID] = useState("");
  const expired = useExpired(props.inspection.expiresAt);
  return (
    <>
      <ActionForm
        title="Connect inspected source"
        icon="add"
        editable={permits(props.access, "source.manage", "sources.manage")}
        description={`Connect ${props.inspection.resolvedUrl}. Review the inspection expiry above. An expired inspection or changed policy requires inspection again.`}
        allowed={
          permits(props.access, "source.manage", "sources.manage") && !expired
        }
        unavailableReason={
          expired
            ? "This inspection has expired locally. Reinspect the same interface, then review its fresh evidence. Your settings remain in this form. The API checks its own clock and policy."
            : "Connecting requires source.manage and sources.manage scope when enforced."
        }
        dirty={
          !!reason ||
          format(settings) !== format(defaults) ||
          topology !== initialTopology
        }
        onFailure={props.onFailure}
        prepare={() => {
          if (!(Date.parse(props.inspection.expiresAt) > Date.now()))
            throw new Error(
              "Inspection expired. Reinspect the same interface before preparing a connection.",
            );
          const body: Schema["ConnectSource"] = {
            ...settings,
            displayName: characterText(
              settings.displayName.trim(),
              "Display name",
              160,
              1,
            ),
            trustVerificationPolicy: trustPolicyName(
              settings.trustVerificationPolicy,
            ),
            scope: sourceScope(settings.scope),
            ...topologyInput(topology),
            inspectionId: props.inspection.id,
            ...(props.inspection.networkProfile === undefined
              ? {}
              : { networkProfile: props.inspection.networkProfile }),
            ...(props.inspection.credentialBinding === undefined
              ? {}
              : { credentialBinding: props.inspection.credentialBinding }),
            reason: reason.trim(),
          };
          return {
            expiresAt: props.inspection.expiresAt,
            details: <SourceConfiguration proposed={body} />,
            summary: commandSummary([
              ["Action", "Connect source"] as const,
              ["Inspection ID", body.inspectionId] as const,
              ["Resolved endpoint", props.inspection.resolvedUrl] as const,
              ["Inspection expiry", props.inspection.expiresAt] as const,
              ["Name", body.displayName] as const,
              ["Scope", JSON.stringify(body.scope)] as const,
              ["Reason", body.reason] as const,
            ]),
            run: async (key) => {
              const source = result(
                await props.client.api.POST("/api/v1/federation/sources", {
                  params: { header: { "Idempotency-Key": key } },
                  body,
                }),
                checks.FederationSource,
              );
              setSourceID(source.id);
              return "Source connected. Synchronization is a separate action.";
            },
          };
        }}
      >
        <SettingsFields value={settings} onChange={setSettings} />
        <SourceTopology {...props} value={topology} onChange={setTopology} />
        <Reason value={reason} onChange={setReason} />
      </ActionForm>
      <ActionForm
        title="Reinspect the same interface"
        icon="refresh"
        description="After expiry, explicitly contact the same resolved endpoint with the captured deployment bindings. Review the fresh summary before connecting; your settings and reason remain in this form."
        editable={permits(props.access, "source.inspect", "sources.manage")}
        allowed={
          expired && permits(props.access, "source.inspect", "sources.manage")
        }
        unavailableReason={
          permits(props.access, "source.inspect", "sources.manage")
            ? "The current inspection has not expired locally. Reinspection does not run automatically."
            : "Reinspection requires source.inspect permission and sources.manage scope when enforced."
        }
        another={{ label: "Prepare another inspection", reset: () => {} }}
        onFailure={props.onFailure}
        prepare={() => {
          const body: Schema["InspectSource"] = {
            url: props.inspection.resolvedUrl,
            ...(props.inspection.networkProfile === undefined
              ? {}
              : { networkProfile: props.inspection.networkProfile }),
            ...(props.inspection.credentialBinding === undefined
              ? {}
              : { credentialBinding: props.inspection.credentialBinding }),
          };
          return {
            summary: commandSummary([
              ["Resolved endpoint to inspect", body.url] as const,
              [
                "Network profile",
                body.networkProfile ?? "Deployment default",
              ] as const,
              [
                "Credential binding",
                body.credentialBinding
                  ? "Configured deployment reference (value withheld)"
                  : "None",
              ] as const,
            ]),
            run: async (key) => {
              const value = result(
                await props.client.api.POST(
                  "/api/v1/federation/source-inspections",
                  { params: { header: { "Idempotency-Key": key } }, body },
                ),
                (value) =>
                  checks.FederationInspectionFor(
                    value,
                    props.access.tenantId,
                    body,
                  ),
              );
              props.onInspection(value);
              return "Fresh inspection retained. Review its resolved endpoint, policy and expiry before connecting.";
            },
          };
        }}
      />
      {sourceID && (
        <a href={`${props.runtime.basePath}/sources/${sourceID}`}>
          Open source
        </a>
      )}
    </>
  );
}

/** Load a source and its ETag before mounting management and snapshot-history controls. */
const keptSourceInput = new Error(
  "Source input was kept instead of applying reload.",
);
function SourceDetail(props: WorkspaceProps) {
  const editorVersion = useRef(0);
  const keptInput = useRef(false);
  const markEditorInteraction = () => {
    editorVersion.current++;
  };
  const read = useCurrentRead(
    props.id,
    async (signal) => {
      const startedVersion = editorVersion.current;
      keptInput.current = false;
      const reply = await props.client.api.GET(
        "/api/v1/federation/sources/{sourceId}",
        { params: { path: { sourceId: props.id } }, signal },
      );
      const source = result(reply, (value) =>
        checks.FederationSourceFor(value, props.access.tenantId, props.id),
      );
      if (
        !signal.aborted &&
        startedVersion !== editorVersion.current &&
        !confirmDiscard()
      ) {
        keptInput.current = true;
        throw keptSourceInput;
      }
      return { source, etag: reply.response.headers.get("ETag") };
    },
    (error) => {
      if (error !== keptSourceInput) props.onFailure(error);
    },
  );
  return (
    <>
      <a href={`${props.runtime.basePath}/sources`}>Back to sources</a>
      <CurrentRead
        read={{
          ...read,
          error: keptInput.current
            ? "Your unsaved input was kept. Reload again when you are ready to replace it with current source data."
            : read.error,
        }}
        label="source"
        reload="Reload source"
        refresh={() => {
          if (confirmDiscard()) read.refresh();
        }}
      />
      {read.data && (
        <div
          onChangeCapture={markEditorInteraction}
          onClickCapture={markEditorInteraction}
        >
          <SourceActions
            {...props}
            {...read.data}
            refreshSource={read.refresh}
            onAcknowledged={read.acknowledge}
            readEpoch={read.epoch}
            stale={read.stale}
            busy={read.busy || !!read.error}
          />
        </div>
      )}
    </>
  );
}
/** Prepare source lifecycle, synchronization and settings workflows from the loaded source. */
function SourceActions(
  props: WorkspaceProps & {
    source: Schema["FederationSource"];
    etag: string | null;
    refreshSource: () => void;
    onAcknowledged: (message: string) => void;
    readEpoch: number;
    stale: boolean;
    busy: boolean;
  },
) {
  const [tab, setTab] = useState("overview");
  const choices = sourceActionChoices(props.access, props.source.status);
  const [selectedAction, setAction] = useState<SourceAction>(
    () =>
      choices[0] ?? sourceActionChoices(props.access, "active")[0] ?? "test",
  );
  const selectionEpoch = useRef(props.readEpoch);
  if (selectionEpoch.current !== props.readEpoch) {
    selectionEpoch.current = props.readEpoch;
    setAction(choices[0] ?? selectedAction);
  }
  // Only an accepted fresh read resets selection; hint changes retain exact recovery.
  const action = selectedAction;
  const [draft, setDraft] = useState({ epoch: props.readEpoch, reason: "" });
  const reason = draft.epoch === props.readEpoch ? draft.reason : "";
  const setReason = (reason: string) =>
    setDraft({ epoch: props.readEpoch, reason });
  const [response, setResponse] = useState<unknown>();
  const [testResult, setTestResult] =
    useState<Schema["FederationTestResult"]>();
  const [jobID, setJobID] = useState("");
  const usable = props.source.status !== "removed";
  return (
    <>
      <section className="workspace-panel">
        <h2>{props.source.displayName}</h2>
        <CopyReference label="Source ID" value={props.source.id} />
        <Facts
          rows={[
            ["Configured state", <SourceState state={props.source.status} />],
            ["Search mode", retrievalLabels[props.source.retrievalMode]],
            ["Endpoint", props.source.endpoint],
            ["Profile", props.source.profileId],
            [
              "Update policy",
              props.source.updateMode === "automatic"
                ? "Automatic synchronization"
                : "Manual synchronization",
            ],
            [
              "ANS verification policy",
              props.source.trustVerificationPolicy || "No policy selected",
            ],
            [
              "Metadata to include",
              (
                [
                  "kinds",
                  "identifiers",
                  "excludedIdentifiers",
                  "versions",
                  "publishers",
                  "tags",
                ] as const
              )
                .map(
                  (name) =>
                    `${name === "excludedIdentifiers" ? "excluded identifiers" : name}: ${(props.source.scope as Scope)[name]?.length ?? 0}`,
                )
                .join("; "),
            ],
            [
              "Live query policy",
              props.source.live.enabled
                ? "Enabled; approved topology and runtime limits still apply"
                : "Disabled",
            ],
            [
              "Query disclosure acknowledged",
              props.source.live.queryPrivacyAcknowledged ? "Yes" : "No",
            ],
            [
              "Last sync attempt finished",
              props.source.lastSyncAt ? (
                <DateValue value={props.source.lastSyncAt} />
              ) : (
                "No finished synchronization attempt recorded"
              ),
            ],
            [
              "Last complete snapshot",
              props.source.lastCompleteSnapshotId ? (
                <a
                  href={`${props.runtime.basePath}/source-snapshots/${props.source.lastCompleteSnapshotId}`}
                >
                  Inspect last complete snapshot
                </a>
              ) : (
                "No complete snapshot recorded"
              ),
            ],
          ]}
        />
        <p>
          Filters select which source metadata contributes. Synchronization
          updates indexed copies; live requests can send queries to approved
          sources. Active is a configured state, not proof that the source is
          available or up to date.
        </p>
        {props.source.lastError && (
          <p className="notice">{props.source.lastError}</p>
        )}
        <EvidenceDetails
          title="Source configuration and provenance"
          value={props.source}
        />
        <a href={`${props.runtime.basePath}/observations?source=${props.id}`}>
          Source observations
        </a>
        <div className="workspace-toolbar">
          {[
            "overview",
            "snapshots",
            ...(permits(props.access, "source.manage", "sources.manage")
              ? ["change"]
              : []),
            ...(canReadTrust(props.access) || canRequestTrust(props.access)
              ? ["trust"]
              : []),
          ].map((value) => (
            <button
              key={value}
              aria-pressed={tab === value}
              onClick={() => {
                if (confirmDiscard()) setTab(value);
              }}
            >
              {value === "change"
                ? "Preview configuration change or removal"
                : value === "trust"
                  ? "ANS registration"
                  : value === "overview"
                    ? "Overview"
                    : "Sync history"}
            </button>
          ))}
        </div>
      </section>
      {tab === "snapshots" && <SourceSnapshots {...props} />}
      {!permits(props.access, "source.manage", "sources.manage") && (
        <p>
          Configuration changes require source.manage and sources.manage scope
          when enforced.
        </p>
      )}
      {!canReadTrust(props.access) && !canRequestTrust(props.access) && (
        <p>
          ANS evidence and assessment requests require separate evidence and
          assessment permissions. Source reading alone does not permit them.
        </p>
      )}
      {tab === "change" && usable && (
        <ChangeSource key={props.readEpoch} {...props} />
      )}
      {tab === "trust" && (
        <SourceTrust
          key={props.source.revision}
          {...props}
          onConfigure={() => {
            if (confirmDiscard()) setTab("change");
          }}
        />
      )}
      {tab === "overview" && (
        <>
          <ActionForm
            key={action + props.readEpoch}
            icon={
              action === "test"
                ? "search"
                : action === "sync"
                  ? "refresh"
                  : action === "pause"
                    ? "pause"
                    : "play"
            }
            intent={action === "pause" ? "warning" : "primary"}
            title={
              {
                test: "Test source",
                sync: "Synchronize source",
                pause: "Pause source",
                resume: "Resume source",
              }[action]
            }
            description={
              {
                test: "Test this saved source endpoint under its configured network policy.",
                sync: "Request synchronization of this source. A returned job may already be in progress; acceptance is not completion.",
                pause: "Pause this source using its loaded revision.",
                resume: "Resume this source using its loaded revision.",
              }[action] + " Reload the source before another command."
            }
            allowed={
              !props.busy &&
              !props.stale &&
              usable &&
              !!props.etag &&
              permits(
                props.access,
                action === "sync"
                  ? "source.sync"
                  : action === "test"
                    ? "source.inspect"
                    : "source.manage",
                "sources.manage",
              ) &&
              (action !== "resume" || props.source.status === "paused") &&
              (action !== "pause" || props.source.status !== "paused") &&
              (action !== "sync" || props.source.status !== "paused")
            }
            editable={sourceActionChoices(props.access, "active").length > 0}
            unavailableReason={
              sourceActionReason(props.access, props.source.status, action) ??
              (props.stale
                ? "Reload the source after the acknowledged command before another action."
                : "The source revision must be loaded before this action.")
            }
            onAcknowledged={props.onAcknowledged}
            dirty={!!reason}
            onFailure={props.onFailure}
            prepare={() => {
              const params = {
                path: { sourceId: props.id },
                header: { "Idempotency-Key": "", "If-Match": props.etag! },
              };
              const body = { reason: reason.trim() };
              return {
                summary: commandSummary([
                  ["Action", action] as const,
                  ["Source ID", props.id] as const,
                  ["Revision", props.etag!] as const,
                  ...(action === "pause" || action === "resume"
                    ? [["Reason", body.reason] as const]
                    : []),
                ]),
                run: async (key) => {
                  params.header["Idempotency-Key"] = key;
                  if (action === "test") {
                    const tested = result(
                      await props.client.api.POST(
                        "/api/v1/federation/sources/{sourceId}:test",
                        { params, body: {} },
                      ),
                      (value) =>
                        checks.FederationTestFor(
                          value,
                          props.id,
                          props.source.revision,
                        ),
                    );
                    setResponse(tested);
                    setTestResult(tested);
                  } else if (action === "sync") {
                    const reply = result(
                      await props.client.api.POST(
                        "/api/v1/federation/sources/{sourceId}:sync",
                        { params, body: {} },
                      ),
                      (value) =>
                        checks.SourceSyncAccepted(value) &&
                        typeof value === "object" &&
                        value !== null &&
                        "job" in value &&
                        checks.JobFor(
                          value.job,
                          props.access.tenantId,
                          "source.sync",
                        ),
                    );
                    setResponse(reply);
                    setJobID(reply.job.id);
                    return reply.existing
                      ? "Existing synchronization Job reused. Inspect the Job for progress; acknowledgement is not completion."
                      : "Synchronization Job accepted. Inspect the Job for progress and the resulting snapshot; acceptance is not completion.";
                  } else if (action === "pause")
                    setResponse(
                      result(
                        await props.client.api.POST(
                          "/api/v1/federation/sources/{sourceId}:pause",
                          { params, body },
                        ),
                        checks.FederationSource,
                      ),
                    );
                  else
                    setResponse(
                      result(
                        await props.client.api.POST(
                          "/api/v1/federation/sources/{sourceId}:resume",
                          { params, body },
                        ),
                        checks.FederationSource,
                      ),
                    );
                  return action === "test"
                    ? "Source test returned. Inspect the result; testing does not synchronize observations."
                    : action === "pause"
                      ? "Source paused. Reload to inspect its current state."
                      : "Source resumed. Reload to inspect its current state.";
                },
              };
            }}
          >
            <Field label="Source action">
              <select
                value={action}
                onChange={(e) => {
                  if (
                    choices.includes(e.target.value as SourceAction) &&
                    confirmDiscard()
                  ) {
                    setAction(e.target.value as typeof action);
                    setReason("");
                    setResponse(undefined);
                    setTestResult(undefined);
                    setJobID("");
                  }
                }}
              >
                {(Object.keys(sourceActionLabels) as SourceAction[]).map(
                  (value) => (
                    <option
                      key={value}
                      value={value}
                      disabled={!choices.includes(value)}
                    >
                      {sourceActionLabels[value]}
                    </option>
                  ),
                )}
              </select>
            </Field>
            <p>
              {sourceActionReason(props.access, props.source.status, action) ??
                "Available from the loaded source state. The API checks current eligibility."}
            </p>
            <details className="workspace-advanced">
              <summary>Other source actions</summary>
              <ul>
                {(Object.keys(sourceActionLabels) as SourceAction[])
                  .filter((value) => value !== action)
                  .map((value) => (
                    <li key={value}>
                      {sourceActionLabels[value]}:{" "}
                      {sourceActionReason(
                        props.access,
                        props.source.status,
                        value,
                      ) ??
                        "Available from the loaded source state; the API checks current eligibility."}
                    </li>
                  ))}
              </ul>
            </details>
            {["pause", "resume"].includes(action) && (
              <Reason value={reason} onChange={setReason} />
            )}
          </ActionForm>
          {testResult && (
            <section
              className="workspace-panel"
              aria-label="Recorded source test outcome"
            >
              <h2>Source test: {stateLabel(testResult.status)}</h2>
              <p>
                This records one admitted test, not synchronization, future
                availability or content approval.
              </p>
              <Facts
                rows={[
                  [
                    "Source ID / returned revision",
                    <>
                      {testResult.sourceId} / {testResult.sourceRevision}
                    </>,
                  ],
                  ["Tested at", <DateValue value={testResult.testedAt} />],
                  [
                    "HTTP outcome",
                    testResult.httpStatus === 0
                      ? "No HTTP response"
                      : `HTTP ${testResult.httpStatus}`,
                  ],
                  [
                    "Safe diagnostic",
                    testResult.safeDiagnostic || "No diagnostic recorded",
                  ],
                  ["Observed entry count", testResult.entryCount],
                  [
                    "Advertised capabilities",
                    Object.entries(testResult.capabilities).length
                      ? Object.entries(testResult.capabilities).map(
                          ([name, enabled]) => (
                            <p key={name}>
                              {name}: {enabled ? "Yes" : "No"}
                            </p>
                          ),
                        )
                      : "None recorded",
                  ],
                ]}
              />
            </section>
          )}
          {response !== undefined && (
            <EvidenceDetails title="Command result" value={response} />
          )}
          {jobID && (
            <p>
              Synchronization Job accepted: <code>{jobID}</code>. Acceptance is
              not completion.{" "}
              {permits(props.access, "job.read", "catalog.read") ? (
                <a href={`${props.runtime.basePath}/jobs/${jobID}`}>
                  Inspect synchronization Job
                </a>
              ) : (
                "Inspecting progress requires job.read and catalog.read scope when enforced."
              )}
            </p>
          )}
        </>
      )}
    </>
  );
}

/** Bind a source change to its inspected preview and captured source revision. */
function ChangeSource(
  props: WorkspaceProps & {
    source: Schema["FederationSource"];
    etag: string | null;
    onAcknowledged: (message: string) => void;
    busy: boolean;
    stale: boolean;
  },
) {
  const source = props.source;
  const [intent, setIntent] = useState<"update" | "remove">("update");
  const [settings, setSettings] = useState<Settings>({
    displayName: source.displayName,
    artworkEnabled: source.artworkEnabled || false,
    trustVerificationPolicy: source.trustVerificationPolicy || "",
    retrievalMode: source.retrievalMode,
    updateMode: source.updateMode,
    scope: sourceScope(source.scope),
    live: source.live,
  });
  const [topology, setTopology] = useState(
    format({
      referrals: source.referrals,
      nesting: source.nesting,
      discoveryAssignments: source.discoveryAssignments || [],
    }),
  );
  const [inspectionID, setInspectionID] = useState("");
  const [networkProfile, setNetwork] = useState(source.networkProfile || "");
  const [credentialBinding, setBinding] = useState(
    source.credentialBinding || "",
  );
  const [reason, setReason] = useState("");
  const [preview, setPreview] = useState<{
    data: Schema["FederationChangePreview"];
    request: Schema["PreviewSourceChange"];
  }>();
  const [generation, setGeneration] = useState(0);
  const previewExpired = useExpired(preview?.data.expiresAt);
  const allowed =
    !props.stale &&
    !props.busy &&
    !!props.etag &&
    permits(props.access, "source.manage", "sources.manage");
  return (
    <>
      <ActionForm
        key={generation}
        title="Preview source change"
        icon="search"
        description="Review affected observations and query disclosure before applying. Any new edit requires a fresh preview; the server also verifies the source revision, observation set and expiry."
        allowed={allowed}
        editable={permits(props.access, "source.manage", "sources.manage")}
        dirty={
          !preview &&
          (intent === "remove" ||
            !!inspectionID ||
            settings.artworkEnabled !==
              ((source as typeof source & { artworkEnabled?: boolean })
                .artworkEnabled || false) ||
            settings.displayName !== source.displayName ||
            settings.trustVerificationPolicy !==
              (source.trustVerificationPolicy || "") ||
            settings.retrievalMode !== source.retrievalMode ||
            settings.updateMode !== source.updateMode ||
            format(settings.scope) !== format(source.scope) ||
            format(settings.live) !== format(source.live) ||
            topology !==
              format({
                referrals: source.referrals,
                nesting: source.nesting,
                discoveryAssignments: source.discoveryAssignments || [],
              }))
        }
        onFailure={props.onFailure}
        prepare={() => {
          const request: Schema["PreviewSourceChange"] =
            intent === "remove"
              ? { intent }
              : {
                  intent,
                  ...settings,
                  displayName: characterText(
                    settings.displayName.trim(),
                    "Display name",
                    160,
                    1,
                  ),
                  trustVerificationPolicy: trustPolicyName(
                    settings.trustVerificationPolicy,
                  ),
                  scope: sourceScope(settings.scope),
                  ...topologyInput(topology),
                  ...(inspectionID
                    ? {
                        inspectionId: inspectionID.trim(),
                        networkProfile: networkProfile.trim(),
                        credentialBinding: credentialBinding.trim(),
                      }
                    : {}),
                };
          return {
            details: (
              <SourceConfiguration
                saved={source}
                proposed={request}
                remove={request.intent === "remove"}
              />
            ),
            summary: commandSummary([
              ["Action", `Preview ${request.intent}`] as const,
              ["Source ID", props.id] as const,
              ["Revision", props.etag!] as const,
              [
                "Settings",
                request.intent === "remove"
                  ? "Removal"
                  : JSON.stringify({
                      displayName: request.displayName,
                      scope: request.scope,
                      inspectionId: request.inspectionId,
                    }),
              ] as const,
            ]),
            run: async (key) => {
              const data = result(
                await props.client.api.POST(
                  "/api/v1/federation/sources/{sourceId}/change-previews",
                  {
                    params: {
                      path: { sourceId: props.id },
                      header: { "Idempotency-Key": key },
                    },
                    body: request,
                  },
                ),
                (value) =>
                  checks.FederationPreviewFor(
                    value,
                    props.access.tenantId,
                    props.id,
                    source.revision,
                    request.intent,
                  ),
              );
              setPreview({ data, request });
              return "Preview ready. Inspect its impact before applying.";
            },
          };
        }}
      >
        <Field label="Change type">
          <select
            value={intent}
            onChange={(e) => setIntent(e.target.value as typeof intent)}
          >
            <option value="update">Update configuration</option>
            <option value="remove">Remove source</option>
          </select>
        </Field>
        {intent === "update" && (
          <>
            <SettingsFields value={settings} onChange={setSettings} />
            <SourceTopology
              {...props}
              value={topology}
              onChange={setTopology}
            />
            <details>
              <summary>Replace network or credential references</summary>
              <p>
                Inspect the same interface to refresh policy evidence or change
                explicit network/credential bindings. Paste its inspection ID
                here; blank keeps existing bindings. Endpoint, type, profile or
                interface changes require a new Source identity.
              </p>
              {permits(props.access, "source.inspect", "sources.manage") && (
                <a href={`${props.runtime.basePath}/sources/new`}>
                  Reinspect existing interface
                </a>
              )}
              <Field
                label="Replacement inspection ID"
                hint="Run a fresh source inspection first, then copy its ID here. An expired or unrelated inspection cannot authorize a source change."
              >
                <input
                  value={inspectionID}
                  onChange={(e) => setInspectionID(e.target.value)}
                />
              </Field>
              {inspectionID && (
                <>
                  <Field label="New network profile reference">
                    <input
                      value={networkProfile}
                      onChange={(e) => setNetwork(e.target.value)}
                    />
                  </Field>
                  <Field label="New credential binding reference">
                    <input
                      value={credentialBinding}
                      onChange={(e) => setBinding(e.target.value)}
                    />
                  </Field>
                </>
              )}
            </details>
          </>
        )}
      </ActionForm>
      {preview && (
        <>
          <section className="workspace-panel">
            <h2>Change preview summary</h2>
            <CopyReference label="Preview ID" value={preview.data.id} />
            <Facts
              rows={[
                ["Current saved entries", preview.data.currentMatched],
                [
                  "Entries included after this change",
                  preview.data.proposedMatched,
                ],
                [
                  "Entries excluded after this change",
                  preview.data.excludedObservations,
                ],
                [
                  "Artwork overrides affected",
                  preview.data.artworkOverridesAffected,
                ],
                [
                  "Queries sent upstream",
                  preview.data.sendsQueriesUpstream ? "Yes" : "No",
                ],
                [
                  "Preview expires",
                  <DateValue value={preview.data.expiresAt} />,
                ],
              ]}
            />
            <p>
              Applying uses this exact preview and source revision. Recheck the
              affected scope and query disclosure; a later edit requires a new
              preview. Counts cover retained current observations, not the full
              upstream inventory or future live results. Clearing exclusions may
              require synchronization to recover records that are no longer
              current. Retained history is not deleted.
            </p>
          </section>
          <SourceConfiguration
            saved={source}
            proposed={preview.request}
            remove={preview.request.intent === "remove"}
          />
          {previewExpired && (
            <p role="status">
              This preview has expired locally. Revise configuration and request
              a fresh preview; your choices remain available. The API still
              checks its own clock and current source policy.
            </p>
          )}
          <EvidenceDetails title="Exact change impact" value={preview.data} />
          <button
            onClick={() => {
              if (confirmDiscard()) {
                setPreview(undefined);
                setGeneration((v) => v + 1);
              }
            }}
          >
            {controlIcons["edit"]} Revise configuration and discard preview
          </button>
          <ActionForm
            key={preview.data.id}
            icon={preview.request.intent === "remove" ? "remove" : "save"}
            intent={preview.request.intent === "remove" ? "danger" : "primary"}
            editable={permits(props.access, "source.manage", "sources.manage")}
            onAcknowledged={props.onAcknowledged}
            title={
              preview.request.intent === "remove"
                ? "Remove source"
                : "Apply source change"
            }
            description={`Apply preview ${preview.data.digest}. ${preview.data.excludedObservations} retained current observations are outside the proposed scope. The source ${preview.data.sendsQueriesUpstream ? "sends" : "does not send"} queries upstream. Historical evidence is retained after removal.`}
            allowed={
              allowed &&
              preview.data.sourceRevision === source.revision &&
              !previewExpired
            }
            dirty={!!reason}
            onFailure={props.onFailure}
            prepare={() => {
              if (!(Date.parse(preview.data.expiresAt) > Date.now()))
                throw new Error(
                  "Preview expired. Request a fresh preview before preparing a new command.",
                );
              const { intent, ...settings } = preview.request;
              const body = {
                ...settings,
                changePreviewDigest: preview.data.digest,
                reason: reason.trim(),
              };
              return {
                expiresAt: preview.data.expiresAt,
                details: (
                  <SourceConfiguration
                    saved={source}
                    proposed={preview.request}
                    remove={intent === "remove"}
                  />
                ),
                summary: commandSummary([
                  [
                    "Action",
                    intent === "remove"
                      ? "Remove source"
                      : "Apply source change",
                  ] as const,
                  ["Source ID", props.id] as const,
                  ["Revision", props.etag!] as const,
                  ["Preview digest", body.changePreviewDigest] as const,
                  ["Preview ID", preview.data.id] as const,
                  ["Preview intent", intent] as const,
                  ["Preview expiry", preview.data.expiresAt] as const,
                  [
                    "Current / proposed / excluded observations",
                    `${preview.data.currentMatched} / ${preview.data.proposedMatched} / ${preview.data.excludedObservations}`,
                  ] as const,
                  [
                    "Artwork overrides affected",
                    String(preview.data.artworkOverridesAffected),
                  ] as const,
                  [
                    "Queries sent upstream",
                    preview.data.sendsQueriesUpstream ? "Yes" : "No",
                  ] as const,
                  ["Reason", body.reason] as const,
                ]),
                run: async (key) => {
                  const params = {
                    path: { sourceId: props.id },
                    header: { "Idempotency-Key": key, "If-Match": props.etag! },
                  };
                  if (intent === "remove") {
                    const reply = await props.client.api.POST(
                      "/api/v1/federation/sources/{sourceId}:remove",
                      {
                        params,
                        body: {
                          changePreviewDigest: body.changePreviewDigest,
                          reason: body.reason,
                        },
                      },
                    );
                    acknowledge(reply);
                  } else
                    result(
                      await props.client.api.PATCH(
                        "/api/v1/federation/sources/{sourceId}",
                        { params, body },
                      ),
                      checks.FederationSource,
                    );
                  return intent === "remove"
                    ? "Source removed."
                    : "Source change applied. Reload the source.";
                },
              };
            }}
          >
            <Reason value={reason} onChange={setReason} />
          </ActionForm>
          {preview.request.intent === "remove" && (
            <a href={`${props.runtime.basePath}/sources`}>Back to sources</a>
          )}
        </>
      )}
    </>
  );
}
