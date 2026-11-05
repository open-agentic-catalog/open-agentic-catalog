import { commandSummary } from "./workspace";
import { controlIcons } from "../shell/Icon";
// Retained artwork evidence and private edits have separate source permissions.
import { useState } from "react";
import { APIError, result, type Access, type Client } from "../api/client";
import { permits } from "../api/permissions";
import { object } from "../api/values";
import { CommandReceipt, ActionForm, EvidenceDetails } from "./authoring";
import { Field, ReadState, useRead, confirmDiscard, Pager } from "./workspace";
import { ArtworkEditor } from "./ArtworkEditor";
import {
  artworkInput,
  isArtworkAsset,
  type ArtworkDescriptor,
} from "./artwork";
import type { ForeignArtworkTarget } from "./Artwork";

type Mode = "inherit" | "replace" | "suppress";
type Override = {
  id: string;
  target: unknown;
  revision: number;
  icon: { mode: Mode; asset?: ArtworkDescriptor["icon"] };
  screenshots: { mode: Mode; assets?: ArtworkDescriptor["screenshots"] };
  reason: string;
  createdAt: string;
};
function isOverride(value: unknown): value is Override {
  const modes = ["inherit", "replace", "suppress"];
  return (
    object(value) &&
    typeof value.id === "string" &&
    typeof value.revision === "number" &&
    Number.isSafeInteger(value.revision) &&
    value.revision > 0 &&
    object(value.icon) &&
    modes.includes(String(value.icon.mode)) &&
    (value.icon.asset === undefined || isArtworkAsset(value.icon.asset)) &&
    object(value.screenshots) &&
    modes.includes(String(value.screenshots.mode)) &&
    (value.screenshots.assets === undefined ||
      (Array.isArray(value.screenshots.assets) &&
        value.screenshots.assets.length <= 8 &&
        value.screenshots.assets.every((asset) => isArtworkAsset(asset)))) &&
    typeof value.reason === "string" &&
    typeof value.createdAt === "string"
  );
}
function isTargetOverride(
  value: unknown,
  target: ForeignArtworkTarget,
): value is Override {
  if (!isOverride(value) || !object(value.target)) return false;
  const retainedTarget = value.target;
  // The API omits an empty version string. Version presence remains an
  // independent identity field and must never be inferred from that omission.
  const { version, ...identity } = target;
  return (
    (retainedTarget.version === undefined ||
      typeof retainedTarget.version === "string") &&
    Object.entries(identity).every(
      ([key, expected]) => retainedTarget[key] === expected,
    ) &&
    (retainedTarget.version ?? "") === (version ?? "")
  );
}
export function foreignArtworkTarget(
  value: unknown,
): ForeignArtworkTarget | undefined {
  if (
    !object(value) ||
    typeof value.identifier !== "string" ||
    !value.identifier ||
    typeof value.versionPresent !== "boolean" ||
    !["agent", "mcp_server", "skill", "plugin"].includes(String(value.kind))
  )
    return undefined;
  const evidence = object(value.indexed)
    ? value.indexed
    : object(value.live)
      ? value.live
      : undefined;
  const sourceId = evidence?.presentationSourceId ?? evidence?.sourceId;
  if (
    typeof sourceId !== "string" ||
    !/^[a-f0-9-]{36}$/iu.test(sourceId) ||
    typeof evidence?.presentationGeneration !== "number" ||
    !Number.isSafeInteger(evidence.presentationGeneration) ||
    evidence.presentationGeneration < 1 ||
    (value.versionPresent &&
      value.version !== undefined &&
      typeof value.version !== "string")
  )
    return undefined;
  return {
    sourceId,
    presentationGeneration: evidence.presentationGeneration,
    identifier: value.identifier,
    versionPresent: value.versionPresent,
    ...(value.versionPresent
      ? { version: (value.version ?? "") as string }
      : {}),
    kind: value.kind as ForeignArtworkTarget["kind"],
  };
}
export function SourceArtworkControls({
  client,
  access,
  item,
  onFailure,
}: {
  client: Client;
  access: Access | undefined;
  item: unknown;
  onFailure: (cause: unknown) => void;
}) {
  // Private evidence and pending reads belong to one identity and exact target.
  return (
    <ArtworkControls
      key={JSON.stringify([access, foreignArtworkTarget(item)])}
      client={client}
      access={access}
      item={item}
      onFailure={onFailure}
    />
  );
}
function ArtworkControls({
  client,
  access,
  item,
  onFailure,
}: {
  client: Client;
  access: Access | undefined;
  item: unknown;
  onFailure: (cause: unknown) => void;
}) {
  const [open, setOpen] = useState(false);
  const target = foreignArtworkTarget(item);
  const [selectedSource, setSelectedSource] = useState("");
  const contributions =
    object(item) &&
    object(item.indexed) &&
    Array.isArray(item.indexed.sourceIds)
      ? item.indexed.sourceIds
          .filter(
            (id): id is string =>
              typeof id === "string" &&
              /^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/iu.test(
                id,
              ),
          )
          .slice(0, 16)
      : [];
  const receipt =
    object(item) &&
    item.type === "live" &&
    object(item.artwork) &&
    typeof item.artwork.receipt === "string"
      ? item.artwork.receipt
      : undefined;
  if (!target || !access) return null;
  const readable = permits(access, "source.read", "sources.read");
  const writable =
    permits(access, "source.manage", "sources.manage") &&
    (!receipt || access.permissions.includes("federation.discover"));
  if (!readable && !writable) return null;
  return (
    <section className="workspace-panel">
      <h3>Local artwork override</h3>
      <p>
        Inspect retained artwork choices for this source and exact entry.
        Publisher metadata and peer snapshots are preserved.
      </p>
      <button
        type="button"
        aria-expanded={open}
        onClick={() => {
          if (!open || confirmDiscard()) setOpen(!open);
        }}
      >
        {controlIcons[writable ? "edit" : "book"]}{" "}
        {open
          ? writable
            ? "Close artwork editor"
            : "Close artwork evidence"
          : writable
            ? "Manage local artwork"
            : "Inspect local artwork"}
      </button>
      {open && !readable && (
        <p className="notice">
          Inspecting the current override requires source.read and sources.read
          when scopes are enforced. No evidence was requested.
        </p>
      )}
      {open && readable && writable && contributions.length > 1 && (
        <Field label="Artwork contribution source">
          <select
            value={selectedSource || target.sourceId}
            onChange={(event) => {
              if (confirmDiscard()) setSelectedSource(event.target.value);
            }}
          >
            {contributions.map((id) => (
              <option key={id} value={id}>
                {id}
                {id === target.sourceId
                  ? " (current presentation)"
                  : " (alternate contribution)"}
              </option>
            ))}
          </select>
        </Field>
      )}
      {open &&
        readable &&
        (writable && selectedSource && selectedSource !== target.sourceId ? (
          <AlternateSourceWorkspace
            key={selectedSource}
            client={client}
            target={{ ...target, sourceId: selectedSource }}
            onFailure={onFailure}
          />
        ) : (
          <OverrideWorkspace
            key={JSON.stringify({
              ...target,
              sourceId: selectedSource || target.sourceId,
            })}
            client={client}
            target={{ ...target, sourceId: selectedSource || target.sourceId }}
            writable={writable}
            {...(receipt ? { receipt } : {})}
            onFailure={onFailure}
          />
        ))}
    </section>
  );
}
function AlternateSourceWorkspace({
  client,
  target,
  onFailure,
}: {
  client: Client;
  target: ForeignArtworkTarget;
  onFailure: (cause: unknown) => void;
}) {
  const read = useRead(
    target.sourceId,
    async (signal) =>
      result(
        await client.api.GET("/api/v1/federation/sources/{sourceId}", {
          params: { path: { sourceId: target.sourceId } },
          signal,
        }),
        (value) =>
          object(value) &&
          value.id === target.sourceId &&
          typeof value.presentationGeneration === "number" &&
          Number.isSafeInteger(value.presentationGeneration) &&
          value.presentationGeneration > 0,
      ),
    onFailure,
  );
  return (
    <>
      <p>
        Editing an alternate contribution. This does not change which source
        supplies the current presentation.
      </p>
      <ReadState {...read} />
      {read.data && (
        <OverrideWorkspace
          writable
          client={client}
          target={{
            ...target,
            presentationGeneration: read.data.presentationGeneration!,
          }}
          onFailure={onFailure}
        />
      )}
    </>
  );
}
function OverrideWorkspace({
  writable,
  client,
  target,
  receipt,
  onFailure,
}: {
  writable: boolean;
  client: Client;
  target: ForeignArtworkTarget;
  receipt?: string;
  onFailure: (cause: unknown) => void;
}) {
  const [message, setMessage] = useState({ text: "", sequence: 0 });
  const [generation, setGeneration] = useState(0);
  const read = useRead(
    JSON.stringify(target),
    async (signal) => {
      const { sourceId, ...query } = target;
      const reply = await client.api.GET(
        "/api/v1/federation/sources/{sourceId}/artwork",
        {
          params: {
            path: { sourceId },
            query,
          },
          signal,
        },
      );
      try {
        const value = result(reply, (value): value is Override =>
          isTargetOverride(value, target),
        );
        return { value, etag: reply.response.headers.get("ETag") || undefined };
      } catch (error) {
        if (error instanceof APIError && error.status === 404)
          return { value: undefined, etag: undefined };
        throw error;
      }
    },
    onFailure,
  );
  return (
    <>
      <CommandReceipt key={`ack:${message.sequence}`} message={message.text} />
      <button
        disabled={read.busy}
        onClick={() => {
          if (!confirmDiscard()) return;
          read.refresh();
          setGeneration((v) => v + 1);
        }}
      >
        {controlIcons["refresh"]} Refresh artwork override
      </button>
      <ReadState {...read} />
      {read.data &&
        (read.data.value ? (
          <EvidenceDetails
            title="Current artwork override"
            value={read.data.value}
          />
        ) : (
          <p>No local artwork override for this exact entry.</p>
        ))}
      {read.data && writable && (
        <OverrideForm
          key={read.data.value?.revision || 0}
          client={client}
          target={target}
          {...(receipt ? { receipt } : {})}
          initial={read.data.value}
          etag={read.data.etag}
          onFailure={onFailure}
          onSaved={() => {
            setMessage((previous) => ({
              text: "Local artwork override saved. Refresh the override head and history here; refresh Catalog results separately to view its current projection.",
              sequence: previous.sequence + 1,
            }));
          }}
        />
      )}
      <OverrideHistory
        generation={generation}
        client={client}
        target={target}
        onFailure={onFailure}
      />
    </>
  );
}
function OverrideForm({
  client,
  target,
  receipt,
  initial,
  etag,
  onFailure,
  onSaved,
}: {
  client: Client;
  target: ForeignArtworkTarget;
  receipt?: string;
  initial: Override | undefined;
  etag: string | undefined;
  onFailure: (cause: unknown) => void;
  onSaved: () => void;
}) {
  const [iconMode, setIconMode] = useState<Mode>(
    initial?.icon.mode || "inherit",
  );
  const [screenshotsMode, setScreenshotsMode] = useState<Mode>(
    initial?.screenshots.mode || "inherit",
  );
  const [artwork, setArtwork] = useState<ArtworkDescriptor>({
    schemaVersion: 1,
    ...(initial?.icon.asset ? { icon: initial.icon.asset } : {}),
    ...(initial?.screenshots.assets
      ? { screenshots: initial.screenshots.assets }
      : {}),
  });
  const [reason, setReason] = useState("");
  const [dirty, setDirty] = useState(false);
  return (
    <div onChange={() => setDirty(true)}>
      <ActionForm
        title="Save local artwork override"
        icon="save"
        onAcknowledged={onSaved}
        description="Use source artwork when permitted, replace it, or hide it. To restore inheritance, reset both choices and save. Earlier overrides stay in history."
        allowed={!initial || !!etag}
        dirty={dirty}
        onFailure={onFailure}
        prepare={() => {
          const descriptor = artworkInput({
            schemaVersion: 1,
            ...(iconMode === "replace" && artwork.icon
              ? { icon: artwork.icon }
              : {}),
            ...(screenshotsMode === "replace" && artwork.screenshots
              ? { screenshots: artwork.screenshots }
              : {}),
          });
          if (iconMode === "replace" && !descriptor.icon)
            throw new Error("Add the replacement icon first.");
          if (screenshotsMode === "replace" && !descriptor.screenshots?.length)
            throw new Error("Add replacement screenshots first.");
          const body = {
            target,
            ...(receipt ? { receipt } : {}),
            icon: {
              mode: iconMode,
              ...(iconMode === "replace" ? { asset: descriptor.icon! } : {}),
            },
            screenshots: {
              mode: screenshotsMode,
              ...(screenshotsMode === "replace"
                ? { assets: descriptor.screenshots! }
                : {}),
            },
            reason: reason.trim(),
          };
          if (!body.reason || [...body.reason].length > 2000)
            throw new Error("Give a reason of up to 2000 characters.");
          return {
            summary: commandSummary([
              ["Action", "Save local artwork override"] as const,
              ["Source ID", target.sourceId] as const,
              ["Entry identifier", target.identifier] as const,
              [
                "Version presence",
                target.versionPresent ? "Present" : "Absent",
              ] as const,
              [
                "Version",
                target.versionPresent
                  ? target.version || "(empty)"
                  : "Not supplied",
              ] as const,
              ["Resource kind", target.kind] as const,
              [
                "Presentation generation",
                String(target.presentationGeneration),
              ] as const,
              [
                "Override revision",
                etag || "New override; no current revision",
              ] as const,
              ["Icon choice", iconMode] as const,
              ["Screenshot choice", screenshotsMode] as const,
              ["Replacement descriptors", JSON.stringify(descriptor)] as const,
              ["Reason", body.reason] as const,
            ]),
            run: async (key) => {
              const reply = await client.api.POST(
                "/api/v1/federation/sources/{sourceId}/artwork",
                {
                  params: {
                    path: { sourceId: target.sourceId },
                    header: {
                      "Idempotency-Key": key,
                      ...(etag ? { "If-Match": etag } : {}),
                    },
                  },
                  body,
                },
              );
              result(reply, (value): value is Override =>
                isTargetOverride(value, target),
              );
              setDirty(false);
              return "Local artwork override saved. Refresh Catalog results to view it.";
            },
          };
        }}
      >
        {(
          [
            ["Icon behavior", iconMode, setIconMode],
            ["Screenshot behavior", screenshotsMode, setScreenshotsMode],
          ] as const
        ).map(([label, value, update]) => (
          <Field key={label} label={label}>
            <select
              value={value}
              onChange={(event) => update(event.target.value as Mode)}
            >
              <option value="inherit">Inherit from source</option>
              <option value="replace">Use local replacement</option>
              <option value="suppress">Hide artwork</option>
            </select>
          </Field>
        ))}
        {(iconMode === "replace" || screenshotsMode === "replace") && (
          <ArtworkEditor
            value={artwork}
            onChange={(value) => {
              setArtwork(value);
              setDirty(true);
            }}
          />
        )}
        <button
          type="button"
          onClick={() => {
            setIconMode("inherit");
            setScreenshotsMode("inherit");
            setDirty(true);
          }}
        >
          {controlIcons["history"]} Reset both choices to inherit
        </button>
        <Field label="Artwork override reason">
          <textarea
            value={reason}
            onChange={(event) => setReason(event.target.value)}
          />
        </Field>
      </ActionForm>
    </div>
  );
}
function OverrideHistory({
  generation,
  client,
  target,
  onFailure,
}: {
  generation: number;
  client: Client;
  target: ForeignArtworkTarget;
  onFailure: (cause: unknown) => void;
}) {
  const [open, setOpen] = useState(false);
  const [cursor, setCursor] = useState("");
  const read = useRead(
    open ? JSON.stringify({ target, cursor, generation }) : "closed",
    async (signal) => {
      if (!open) return undefined;
      const { sourceId, ...query } = target;
      return result(
        await client.api.GET(
          "/api/v1/federation/sources/{sourceId}/artwork/history",
          {
            params: {
              path: { sourceId },
              query: { ...query, limit: 25, ...(cursor ? { cursor } : {}) },
            },
            signal,
          },
        ),
        (value) =>
          object(value) &&
          Array.isArray(value.items) &&
          value.items.every((item) => isTargetOverride(item, target)) &&
          (value.nextCursor === undefined ||
            typeof value.nextCursor === "string"),
      );
    },
    onFailure,
  );
  return (
    <details onToggle={(event) => setOpen(event.currentTarget.open)}>
      <summary>{controlIcons["history"]} Artwork override history</summary>
      {open && (
        <>
          <ReadState {...read} />
          {read.data?.items.map((item) => (
            <EvidenceDetails
              key={item.id}
              title={`Artwork revision ${item.revision}`}
              value={item}
            />
          ))}
          {read.data?.items.length === 0 && <p>No local artwork history.</p>}
          <Pager
            cursor={cursor}
            {...(read.data?.nextCursor
              ? { nextCursor: read.data.nextCursor }
              : {})}
            busy={read.busy}
            onPage={setCursor}
          />
        </>
      )}
    </details>
  );
}
