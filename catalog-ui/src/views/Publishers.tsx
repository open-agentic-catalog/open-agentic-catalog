import { PublisherVerification } from "./PublisherVerification";
import { Facts } from "./Facts";
import { controlIcons } from "../shell/Icon";
import { ScrollableTable } from "./ScrollableTable";
import { CatalogDirectoryFilters } from "./catalog-directory-filters";
import { CommandReceipt } from "./authoring";
import { characterText } from "./input-validation";
import * as checks from "./responses";
// Publisher metadata editing and publication-domain approval use distinct permissions and commands.
import { CopyReference } from "./CopyReference";
import { useId, useRef, useState } from "react";
import { compareTableText, usePageSort } from "./table-sort";
import { result } from "../api/client";
import { acknowledged } from "./audit-handoff";
import type { components } from "../api/schema";
import { permits, publisherAccess } from "../api/permissions";
import { domainsFromInput } from "../api/commands";
import {
  CommandDialog,
  Field,
  Heading,
  Pager,
  ReadState,
  Reason,
  useRead,
  useUnsaved,
} from "./workspace";
import type { Command, WorkspaceProps as Props } from "./workspace";

type Publisher = components["schemas"]["Publisher"];
type DomainSort = ReturnType<typeof usePageSort<string, "domain" | "approval">>;
type PublisherCommand = Command & {
  action: "metadata" | "domain";
  disclosureGeneration?: number;
};
const undisclosedDomainResult =
  "Publisher domain command acknowledged. Private results are unavailable after access changed. Reload with current access to inspect publisher state.";

/** Select publisher workflows using distinct read, create and administration hints. */
export default function Publishers(props: Props) {
  const permission = publisherAccess(props.access);
  const identityKey = JSON.stringify([
    props.id,
    props.access.tenantId,
    props.access.realmId,
    props.access.membershipId,
    props.access.subject,
    props.access.actorType,
  ]);
  return (
    <>
      <Heading
        title="Publishers"
        icon="globe"
        description="Manage publisher metadata, declared domains and publication approvals."
      />
      {props.id === "new" ? (
        <>
          {!permission.create && (
            <p role="alert">
              Your current access does not permit publisher creation.
            </p>
          )}
          <PublisherEditor key={identityKey} {...props} />
        </>
      ) : permission.read ? (
        props.id ? (
          <PublisherDetail key={identityKey} {...props} />
        ) : (
          <PublisherList {...props} />
        )
      ) : (
        <p className="notice">
          Publisher browsing requires publisher.read and publishers.read when
          scopes are enforced.{" "}
          {permission.create && (
            <a href={`${props.runtime.basePath}/publishers/new`}>
              Create a publisher
            </a>
          )}
        </p>
      )}
    </>
  );
}

/** Page publisher summaries without preloading their editable detail. */
function PublisherList(props: Props) {
  const sortHint = useId();
  const [cursor, setCursor] = useState("");
  const [filters, setFilters] = useState<{
    displayName?: string;
    declaredDomain?: string;
  }>({});
  const page = useRead(
    JSON.stringify([cursor, filters]),
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/publishers", {
          params: {
            query: {
              ...filters,
              limit: 25,
              ...(cursor ? { cursor: cursor } : {}),
            },
          },
          signal,
        }),
        checks.PublisherPage,
      ),
    props.onFailure,
  );
  const refresh = () => (page.error && cursor ? setCursor("") : page.refresh());
  const sorted = usePageSort(page.data?.items, {
    name: (a, b) => compareTableText(a.displayName, b.displayName),
    declared: (a, b) => a.domains.length - b.domains.length,
    approved: (a, b) =>
      a.approvedPublicationDomains.length - b.approvedPublicationDomains.length,
  });
  return (
    <>
      <CatalogDirectoryFilters
        kind="publisher"
        busy={page.busy}
        applied={filters}
        onApply={(next) => {
          setCursor("");
          setFilters(next);
          if (!cursor && JSON.stringify(next) === JSON.stringify(filters))
            page.refresh();
        }}
      />
      <div className="workspace-toolbar">
        <button disabled={page.busy} onClick={refresh}>
          {controlIcons["refresh"]} Refresh
        </button>
        {publisherAccess(props.access).create && (
          <a
            className="button primary"
            href={`${props.runtime.basePath}/publishers/new`}
          >
            {controlIcons["add"]} Create publisher
          </a>
        )}
      </div>
      <ReadState {...page} refresh={refresh} />
      {page.data && (
        <>
          <p id={sortHint} className="muted">
            Select a column heading to sort the results on this page.
          </p>
          <ScrollableTable label="Publishers">
            <table className="workspace-table" aria-describedby={sortHint}>
              <thead>
                <tr>
                  {sorted.heading("Name", "name")}
                  {sorted.heading("Declared domains", "declared")}
                  {sorted.heading("Approved domains", "approved")}
                </tr>
              </thead>
              <tbody>
                {sorted.rows.map((item) => (
                  <tr key={item.id}>
                    <td>
                      <a
                        href={`${props.runtime.basePath}/publishers/${item.id}`}
                      >
                        {item.displayName}
                      </a>
                    </td>
                    <td>
                      <div>
                        {item.domains.length}{" "}
                        {item.domains.length === 1 ? "domain" : "domains"}
                      </div>
                      <span className="muted">{item.domains.join(", ")}</span>
                    </td>
                    <td>
                      <div>
                        {item.approvedPublicationDomains.length}{" "}
                        {item.approvedPublicationDomains.length === 1
                          ? "domain"
                          : "domains"}
                      </div>
                      <span className="muted">
                        {item.approvedPublicationDomains.join(", ") || "None"}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!page.data.items.length && (
              <p className="workspace-empty">
                {filters.displayName || filters.declaredDomain
                  ? "No publishers match the applied filters."
                  : "No publishers on this page."}
              </p>
            )}
          </ScrollableTable>
        </>
      )}
      <Pager
        label="Publisher inventory pagination"
        cursor={cursor}
        nextCursor={page.data?.nextCursor}
        busy={page.busy}
        onPage={setCursor}
      />
    </>
  );
}

/** Load one publisher with the ETag required for metadata and domain changes. */
function PublisherDetail(props: Props) {
  const detail = useRead(
    props.id,
    async (signal) => {
      const reply = await props.client.api.GET(
        "/api/v1/publishers/{publisherId}",
        { params: { path: { publisherId: props.id } }, signal },
      );
      return {
        item: result(reply, checks.Publisher),
        etag: reply.response.headers.get("ETag"),
      };
    },
    props.onFailure,
  );
  // The revision-keyed editor resets drafts on readback; the table order belongs
  // to the selected Publisher and survives an updated revision.
  const approvalLabel = (domain: string) =>
    detail.data?.item.approvedPublicationDomains.includes(domain)
      ? "Approved"
      : "Not approved";
  const sortedDomains = usePageSort(detail.data?.item.domains, {
    domain: compareTableText,
    approval: (a, b) => compareTableText(approvalLabel(a), approvalLabel(b)),
  });
  return (
    <>
      <a className="back-link" href={`${props.runtime.basePath}/publishers`}>
        Back to publishers
      </a>
      <ReadState {...detail} />
      {detail.data && (
        <PublisherEditor
          key={`${detail.data.item.id}:${detail.data.etag}`}
          {...props}
          item={detail.data.item}
          sortedDomains={sortedDomains}
          etag={detail.data.etag}
          ready={!detail.busy && !detail.error}
          refresh={detail.refresh}
        />
      )}
    </>
  );
}

/** Prepare publisher metadata and explicit domain approval/revocation commands. */
function PublisherEditor({
  item,
  sortedDomains,
  etag,
  ready = true,
  refresh,
  ...props
}: Props & {
  item?: Publisher;
  sortedDomains?: DomainSort;
  etag?: string | null;
  ready?: boolean;
  refresh?: () => void;
}) {
  const sortHint = useId();
  const [name, setName] = useState(item?.displayName ?? "");
  const [domains, setDomains] = useState(item?.domains.join("\n") ?? "");
  const [reason, setReason] = useState("");
  const [domainReason, setDomainReason] = useState("");
  const [dnsReason, setDNSReason] = useState("");
  const [command, setCommand] = useState<PublisherCommand>();
  const [message, setMessage] = useState("");
  const [messageScope, setMessageScope] = useState<{
    action: "metadata" | "domain";
    disclosureGeneration?: number;
  }>();
  const [acknowledgements, setAcknowledgements] = useState(0);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const [receipt, setReceipt] = useState<{
    generation: number;
    value: components["schemas"]["PublicationDomainRevocation"];
  }>();
  const permission = publisherAccess(props.access);
  const approveDomain = permits(
    props.access,
    "publisher.approve_domain",
    "publishers.manage",
  );
  // Drafts and exact-key recovery survive a hint change; private domain results
  // belong to the approval access that admitted that command. Regaining access
  // does not resurrect a result captured before a withdrawal.
  const domainAccess = useRef({ allowed: approveDomain, generation: 0 });
  if (domainAccess.current.allowed !== approveDomain)
    domainAccess.current = {
      allowed: approveDomain,
      generation: domainAccess.current.generation + 1,
    };
  const visibleReceipt =
    approveDomain && receipt?.generation === domainAccess.current.generation
      ? receipt.value
      : undefined;
  const visibleMessage =
    messageScope?.action === "domain" &&
    (!approveDomain ||
      messageScope.disclosureGeneration !== domainAccess.current.generation)
      ? undisclosedDomainResult
      : message;
  const writable = item ? permission.update : permission.create;
  const changed =
    name !== (item?.displayName ?? "") ||
    domains !== (item?.domains.join("\n") ?? "");
  const metadataDirty = changed || !!reason;
  const dirty = metadataDirty || !!domainReason || !!dnsReason;
  const showEditor = writable || metadataDirty;
  useUnsaved(dirty && !saved);
  const domainCommand = (domain: string, revoke: boolean) => {
    if (!approveDomain || !item || !etag || !domainReason.trim()) return;
    const body = { reason: domainReason.trim() };
    const generation = domainAccess.current.generation;
    setCommand({
      action: "domain",
      intent: revoke ? "danger" : "primary",
      icon: revoke ? "remove" : "check",
      disclosureGeneration: generation,
      title: revoke
        ? "Revoke publication approval"
        : "Approve publication domain",
      summary: [
        { label: "Publisher ID", value: item.id },
        { label: "Base ETag", value: etag },
        { label: "Domain", value: domain },
        {
          label: "Approval change",
          value: revoke ? "Approved → Not approved" : "Not approved → Approved",
        },
        { label: "Reason", value: body.reason },
      ],
      description: revoke
        ? `Revoke ${domain}. Active discovery snapshots containing this domain will be withdrawn. Reason: ${body.reason}`
        : `Approve ${domain} for publication by ${item.displayName}. This does not verify domain ownership or legal rights. Reason: ${body.reason}`,
      run: async (key) => {
        const params = {
          path: { publisherId: item.id, domain },
          header: { "Idempotency-Key": key, "If-Match": etag },
        };
        if (revoke) {
          const reply = await props.client.api.POST(
            "/api/v1/publishers/{publisherId}/publication-domains/{domain}:revoke",
            { params, body },
          );
          const value = result(reply, checks.PublicationDomainRevocation);
          if (
            !domainAccess.current.allowed ||
            domainAccess.current.generation !== generation
          )
            return undisclosedDomainResult;
          setReceipt({ generation, value });
          return acknowledged(
            `Approval revoked. ${value.affectedReleaseIds.length} affected releases. Withdrawn hosts: ${value.withdrawnHosts.join(", ") || "none"}. Reload to view current approvals.`,
            reply.response,
            {
              action: "publisher.publication_domain_revoked",
              targetType: "publisher",
              targetId: item.id,
              revision: value.publisher.revision,
            },
          );
        }
        const reply = await props.client.api.POST(
          "/api/v1/publishers/{publisherId}/publication-domains/{domain}:approve",
          { params, body },
        );
        const saved = result(reply, checks.Publisher);
        if (
          !domainAccess.current.allowed ||
          domainAccess.current.generation !== generation
        )
          return undisclosedDomainResult;
        return acknowledged(
          "Domain approved for publication. Reload to view current approvals.",
          reply.response,
          {
            action: "publisher.publication_domain_approved",
            targetType: "publisher",
            targetId: item.id,
            revision: saved.revision,
          },
        );
      },
    });
  };
  return (
    <section className="workspace-panel">
      <h2>{item ? item.displayName : "Create publisher"}</h2>
      {item && (
        <Facts
          rows={[
            ["Publisher ID", <code>{item.id}</code>],
            ["Revision", item.revision],
            ...(!showEditor
              ? ([
                  ["Display name", item.displayName],
                  ["Declared domains", item.domains.join(", ")],
                ] as const)
              : []),
          ]}
        />
      )}
      <CommandReceipt key={acknowledgements} message={visibleMessage} />
      {error && (
        <p className="notice error" role="alert">
          {error}
        </p>
      )}
      {showEditor && (
        <form
          className="workspace-form"
          onSubmit={(e) => {
            e.preventDefault();
            setError("");
            if (!writable || !name.trim() || (item && !reason.trim())) return;
            try {
              const body = {
                displayName: characterText(name.trim(), "Display name", 160, 1),
                domains: domainsFromInput(domains),
              };
              setCommand({
                action: "metadata",
                title: item ? "Save publisher changes" : "Create publisher",
                icon: item ? "save" : "add",
                summary: [
                  {
                    label: "Publisher ID",
                    value:
                      item?.id ?? "New object; ID is returned after creation",
                  },
                  ...(item
                    ? [
                        {
                          label: "Base ETag",
                          value: etag ?? "Unavailable; reload before saving",
                        },
                      ]
                    : []),
                  {
                    label: "Display name",
                    value: item
                      ? `${item.displayName} → ${body.displayName}`
                      : body.displayName,
                  },
                  ...(item
                    ? [
                        {
                          label: "Previous domains",
                          value: item.domains.join("\n"),
                        },
                      ]
                    : []),
                  { label: "Declared domains", value: body.domains.join("\n") },
                  ...(item ? [{ label: "Reason", value: reason.trim() }] : []),
                ],
                description: item
                  ? `Update ${item.displayName}. Approved domains must be revoked before removal. Reason: ${reason.trim()}`
                  : `Create ${body.displayName} with ${body.domains.length} declared domains. Publication approvals are separate.`,
                run: async (key) => {
                  if (item) {
                    if (!etag)
                      throw new Error(
                        "Reload the publisher to obtain its current revision.",
                      );
                    const reply = await props.client.api.PATCH(
                      "/api/v1/publishers/{publisherId}",
                      {
                        params: {
                          path: { publisherId: item.id },
                          header: { "Idempotency-Key": key, "If-Match": etag },
                        },
                        body: { ...body, reason: reason.trim() },
                      },
                    );
                    const saved = result(reply, checks.Publisher);
                    return acknowledged(
                      "Publisher updated. Reload to view its current revision.",
                      reply.response,
                      {
                        action: "publisher.updated",
                        targetType: "publisher",
                        targetId: item.id,
                        revision: saved.revision,
                      },
                    );
                  }
                  const reply = await props.client.api.POST(
                    "/api/v1/publishers",
                    {
                      params: { header: { "Idempotency-Key": key } },
                      body,
                    },
                  );
                  const created = result(reply, checks.Publisher);
                  return acknowledged(
                    `Created publisher ${created.displayName}. ID: ${created.id}`,
                    reply.response,
                    {
                      action: "publisher.created",
                      targetType: "publisher",
                      targetId: created.id,
                      revision: created.revision,
                    },
                  );
                },
              });
            } catch (e) {
              setError(
                e instanceof Error ? e.message : "Check the publisher fields.",
              );
            }
          }}
        >
          <fieldset
            className="workspace-fields"
            disabled={!writable || !!command || saved}
          >
            <Field label="Display name">
              <input
                required
                maxLength={320}
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </Field>
            <Field
              label="Declared domains"
              hint="Domains this publisher claims, separated by commas or new lines, for example example.com. Declaring a domain does not approve it for publication."
            >
              <textarea
                required
                maxLength={16384}
                value={domains}
                onChange={(e) => setDomains(e.target.value)}
                rows={4}
              />
            </Field>
            {item && (
              <Reason
                label="Metadata change reason"
                value={reason}
                onChange={setReason}
              />
            )}
            {writable && (
              <button
                className="primary"
                type="submit"
                disabled={!!item && (!etag || !changed || !reason.trim())}
              >
                {controlIcons[item ? "save" : "add"]}{" "}
                {item ? "Review changes" : "Create publisher"}
              </button>
            )}
          </fieldset>
        </form>
      )}
      {!writable && (
        <p className="muted">
          Read-only. Publisher changes require publisher.create or
          publisher.manage for the selected action, and publishers.manage when
          scopes are enforced.
        </p>
      )}
      {item && sortedDomains && (
        <section className="workspace-panel">
          <h2>Publication domains</h2>
          <p className="muted">
            Approval permits the baseline publication workflow. Publisher
            authority and legal rights remain unassessed.
          </p>
          {metadataDirty && (
            <p className="notice">
              Finish or discard metadata changes before approving or revoking a
              publication domain.
            </p>
          )}
          {(approveDomain || !!domainReason) && (
            <fieldset
              className="workspace-fields"
              disabled={!approveDomain || !!command || saved}
            >
              <Reason
                label="Publication approval reason"
                value={domainReason}
                onChange={setDomainReason}
              />
            </fieldset>
          )}
          <p id={sortHint} className="muted">
            Select a column heading to sort the declared domains.
          </p>
          <ScrollableTable label="Publisher domains">
            <table className="workspace-table" aria-describedby={sortHint}>
              <thead>
                <tr>
                  {sortedDomains.heading("Domain", "domain")}
                  {sortedDomains.heading("Publication approval", "approval")}
                  {approveDomain && <th scope="col">Action</th>}
                </tr>
              </thead>
              <tbody>
                {sortedDomains.rows.map((domain) => {
                  const approved =
                    item.approvedPublicationDomains.includes(domain);
                  return (
                    <tr key={domain}>
                      <td>{domain}</td>
                      <td>
                        <span
                          className="workspace-status"
                          data-tone={approved ? "success" : "neutral"}
                        >
                          {approved ? "Approved" : "Not approved"}
                        </span>
                      </td>
                      {approveDomain && (
                        <td>
                          <button
                            className={approved ? "danger" : "primary"}
                            disabled={
                              !etag ||
                              !domainReason.trim() ||
                              metadataDirty ||
                              !!command ||
                              saved
                            }
                            onClick={() => domainCommand(domain, approved)}
                          >
                            {controlIcons[approved ? "remove" : "check"]}{" "}
                            {approved ? "Revoke approval" : "Approve domain"}
                          </button>
                        </td>
                      )}
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </ScrollableTable>
        </section>
      )}
      {item && (
        <PublisherVerification
          {...props}
          publisher={item}
          reason={dnsReason}
          setReason={setDNSReason}
          blocked={metadataDirty || !!command || saved || !ready}
        />
      )}
      {visibleReceipt && (
        <section
          className="workspace-panel"
          aria-label="Publication revocation receipt"
        >
          <h2>Publication revocation receipt</h2>
          <p>
            Affected releases: {visibleReceipt.affectedReleaseIds.length}. These
            references are the result of this command.
          </p>
          {visibleReceipt.affectedReleaseIds.map((id) => (
            <CopyReference key={id} label="release ID" value={id} />
          ))}
          <p>Withdrawn hosts: {visibleReceipt.withdrawnHosts.length}.</p>
          {visibleReceipt.withdrawnHosts.map((host) => (
            <CopyReference key={host} label="withdrawn host" value={host} />
          ))}
          <button type="button" onClick={() => setReceipt(undefined)}>
            Clear receipt
          </button>
        </section>
      )}
      <div className="workspace-toolbar">
        {refresh && (
          <button
            onClick={() => {
              if (
                !dirty ||
                (saved && !dnsReason) ||
                confirm("Discard unsaved changes and reload this publisher?")
              )
                refresh();
            }}
          >
            Reload publisher
          </button>
        )}
        <a href={`${props.runtime.basePath}/publishers`}>Back to publishers</a>
      </div>
      {command && (
        <CommandDialog
          command={command}
          allowed={
            ready &&
            !saved &&
            (command.action === "domain"
              ? approveDomain && !!item && !!etag && !metadataDirty
              : writable && (!item || !!etag))
          }
          onClose={() => setCommand(undefined)}
          onFailure={(cause) => {
            if (
              command.action !== "domain" ||
              command.disclosureGeneration === domainAccess.current.generation
            )
              props.onFailure(cause);
          }}
          onDone={(text) => {
            setSaved(true);
            setCommand(undefined);
            setMessage(text);
            setMessageScope({
              action: command.action,
              ...(command.disclosureGeneration !== undefined
                ? { disclosureGeneration: command.disclosureGeneration }
                : {}),
            });
            setAcknowledgements((v) => v + 1);
          }}
        />
      )}
    </section>
  );
}
