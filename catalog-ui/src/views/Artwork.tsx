import type { components } from "../api/schema";
// Images are fetched through the authenticated Catalog boundary, never by remote img URLs.
import { useEffect, useRef, useState } from "react";
import { APIError, result, type Client } from "../api/client";
import { object } from "../api/values";
import { Icon, controlIcons } from "../shell/Icon";
import {
  artworkIRI,
  isArtwork,
  type ArtworkAsset,
  type ArtworkDescriptor,
} from "./artwork";

export type ForeignArtworkTarget = {
  sourceId: string;
  presentationGeneration: number;
  identifier: string;
  versionPresent: boolean;
  version?: string;
  kind: "agent" | "mcp_server" | "skill" | "plugin";
};
export type ArtworkTarget =
  | { origin: "local"; listingRevisionId: string }
  | { origin: "indexed"; target: ForeignArtworkTarget }
  | {
      origin: "live";
      receipt: string;
      descriptor: components["schemas"]["ArtworkPresentationDescriptor"];
    };
type Presentation = {
  descriptor: ArtworkDescriptor;
  target: ArtworkTarget;
  iconSource?: string;
  screenshotsSource?: string;
  mutableIcon?: boolean;
};
const uuid = (value: unknown): value is string =>
  typeof value === "string" && /^[a-f0-9-]{36}$/iu.test(value);
/** Narrow the selected result's actual source; group IDs never grant image authority. */
export function artworkPresentation(value: unknown): Presentation | undefined {
  if (!object(value)) return undefined;
  const projected = object(value.artwork) ? value.artwork : undefined;
  const local = object(value.local) ? value.local : undefined;
  const listing = object(value.listingRevision)
    ? value.listingRevision
    : local && object(local.listingRevision)
      ? local.listingRevision
      : undefined;
  const extensions =
    listing && object(listing.extensions) ? listing.extensions : undefined;
  const descriptor = projected?.descriptor ?? extensions?.[artworkIRI];
  if (!isArtwork(descriptor, projected?.mutableIcon === true)) return undefined;
  const attribution = {
    ...(typeof projected?.iconSource === "string"
      ? { iconSource: projected.iconSource }
      : {}),
    ...(typeof projected?.screenshotsSource === "string"
      ? { screenshotsSource: projected.screenshotsSource }
      : {}),
    mutableIcon: projected?.mutableIcon === true,
  };
  if (listing && uuid(listing.id))
    return {
      descriptor,
      target: { origin: "local", listingRevisionId: listing.id },
      ...attribution,
    };
  if (
    object(value.indexed) &&
    uuid(value.indexed.presentationSourceId) &&
    typeof value.indexed.presentationGeneration === "number" &&
    Number.isSafeInteger(value.indexed.presentationGeneration) &&
    value.indexed.presentationGeneration > 0 &&
    typeof value.identifier === "string" &&
    typeof value.versionPresent === "boolean" &&
    ["agent", "mcp_server", "skill", "plugin"].includes(String(value.kind))
  ) {
    return {
      descriptor,
      target: {
        origin: "indexed",
        target: {
          sourceId: value.indexed.presentationSourceId,
          presentationGeneration: value.indexed.presentationGeneration,
          identifier: value.identifier,
          versionPresent: value.versionPresent,
          ...(value.versionPresent && typeof value.version === "string"
            ? { version: value.version }
            : {}),
          kind: value.kind as ForeignArtworkTarget["kind"],
        },
      },
      ...attribution,
    };
  }
  if (
    value.type === "live" &&
    typeof projected?.receipt === "string" &&
    projected.receipt.length <= 4096 &&
    projected.receipt.length > 0
  )
    return {
      descriptor,
      target: {
        origin: "live",
        receipt: projected.receipt,
        descriptor:
          descriptor as components["schemas"]["ArtworkPresentationDescriptor"],
      },
      ...attribution,
    };
  return undefined;
}

// At most four active image reads across cards and galleries. Cancelled waiters leave immediately.
let active = 0;
const waiting: Array<() => void> = [];
function releaseImage() {
  active--;
  waiting.shift()?.();
}
async function imageAdmission(signal: AbortSignal): Promise<() => void> {
  if (signal.aborted) throw new DOMException("Cancelled", "AbortError");
  if (active < 4) {
    active++;
    return releaseImage;
  }
  await new Promise<void>((resolve, reject) => {
    if (waiting.length >= 128) {
      reject(new Error("Image requests are busy."));
      return;
    }
    const resume = () => {
      signal.removeEventListener("abort", cancel);
      active++;
      resolve();
    };
    const cancel = () => {
      const index = waiting.indexOf(resume);
      if (index >= 0) waiting.splice(index, 1);
      reject(new DOMException("Cancelled", "AbortError"));
    };
    waiting.push(resume);
    signal.addEventListener("abort", cancel, { once: true });
  });
  if (signal.aborted) {
    releaseImage();
    throw new DOMException("Cancelled", "AbortError");
  }
  return releaseImage;
}
function ArtworkImage({
  client,
  presentation,
  asset,
  slot,
  index,
  lazy = false,
  kind,
  onFailure,
}: {
  client: Client;
  presentation: Presentation;
  asset: ArtworkAsset;
  slot: "icon" | "screenshot";
  index?: number;
  onFailure?: ((error: unknown) => void) | undefined;
  lazy?: boolean;
  kind?: ForeignArtworkTarget["kind"];
}) {
  const element = useRef<HTMLDivElement>(null);
  const failure = useRef(onFailure);
  failure.current = onFailure;
  const [visible, setVisible] = useState(!lazy);
  const [loaded, setLoaded] = useState<{
    href: string;
    key: string;
    client: Client;
  }>();
  const [failed, setFailed] = useState(false);
  const [retry, setRetry] = useState(0);
  const key = JSON.stringify({
    target: presentation.target,
    asset,
    slot,
    index,
  });
  useEffect(() => {
    if (!lazy || !element.current) return;
    if (typeof IntersectionObserver === "undefined") return;
    const observer = new IntersectionObserver((entries) => {
      if (entries.some((entry) => entry.isIntersecting)) {
        setVisible(true);
        observer.disconnect();
      }
    });
    observer.observe(element.current);
    return () => observer.disconnect();
  }, [lazy, key]);
  useEffect(() => {
    if (!visible) return;
    const controller = new AbortController();
    let current = true;
    let attachment = "";
    setLoaded(undefined);
    setFailed(false);
    const lifetime = client.signal;
    const invalidate = () => {
      current = false;
      controller.abort();
      if (attachment) {
        URL.revokeObjectURL(attachment);
        attachment = "";
      }
      setLoaded(undefined);
    };
    lifetime?.addEventListener("abort", invalidate, { once: true });
    void (async () => {
      let release: (() => void) | undefined;
      try {
        release = await imageAdmission(controller.signal);
        const reply = await client.api.POST("/api/v1/artwork/read", {
          body: {
            ...presentation.target,
            slot,
            ...(index === undefined ? {} : { index }),
          },
          parseAs: "arrayBuffer",
          signal: controller.signal,
        });
        const bytes = result(reply);
        const mediaType = reply.response.headers
          .get("Content-Type")
          ?.split(";")[0];
        if (
          !["image/png", "image/jpeg"].includes(mediaType || "") ||
          bytes.byteLength > 2 * 1024 * 1024
        )
          throw new Error("Invalid image response.");
        const hash = new Uint8Array(
          await crypto.subtle.digest("SHA-256", bytes),
        );
        const hex = Array.from(hash, (byte) =>
          byte.toString(16).padStart(2, "0"),
        ).join("");
        if (
          reply.response.headers.get("Content-Digest") !==
            `sha-256=:${btoa(String.fromCharCode(...hash))}:` ||
          (asset.digest && asset.digest !== `sha256:${hex}`)
        )
          throw new Error("Image changed.");
        result(reply);
        if (!current || controller.signal.aborted) return;
        attachment = URL.createObjectURL(
          new Blob([bytes], { type: mediaType! }),
        );
        setLoaded({ href: attachment, key, client });
      } catch (error) {
        if (current && !controller.signal.aborted) {
          setFailed(true);
          if (
            error instanceof APIError &&
            (error.status === 401 || error.status === 403)
          )
            failure.current?.(error);
        }
      } finally {
        release?.();
      }
    })();
    return () => {
      lifetime?.removeEventListener("abort", invalidate);
      current = false;
      controller.abort();
      if (attachment) URL.revokeObjectURL(attachment);
    };
    // The serialized key binds all image authority and content fields.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [client, key, visible, retry]);
  const href =
    loaded?.key === key && loaded.client === client ? loaded.href : "";
  return (
    <div ref={element} className={`artwork-image artwork-${slot}`}>
      {href ? (
        <img
          src={href}
          alt={asset.alt}
          width={asset.width || (slot === "icon" ? 48 : 640)}
          height={asset.height || (slot === "icon" ? 48 : 360)}
          onError={() => {
            setFailed(true);
            setLoaded(undefined);
          }}
        />
      ) : kind ? (
        <span className={`kind-icon ${kind}`} aria-hidden="true">
          <Icon name={kind} size={28} />
        </span>
      ) : (
        <p className="muted">
          {failed ? "Image unavailable" : "Loading image…"}
        </p>
      )}
      {failed && slot === "screenshot" && (
        <button type="button" onClick={() => setRetry((value) => value + 1)}>
          {controlIcons["refresh"]} Retry image
        </button>
      )}
    </div>
  );
}
export function ArtworkIcon({
  client,
  item,
  kind,
  onFailure,
}: {
  client: Client;
  item: unknown;
  kind: ForeignArtworkTarget["kind"];
  onFailure?: ((error: unknown) => void) | undefined;
}) {
  const presentation = artworkPresentation(item);
  if (!presentation?.descriptor.icon)
    return (
      <span className={`kind-icon ${kind}`} aria-hidden="true">
        <Icon name={kind} size={22} />
      </span>
    );
  return (
    <ArtworkImage
      client={client}
      presentation={presentation}
      asset={presentation.descriptor.icon}
      slot="icon"
      lazy
      onFailure={onFailure}
      kind={kind}
    />
  );
}
export function ArtworkGallery({
  client,
  item,
  onFailure,
}: {
  client: Client;
  item: unknown;
  onFailure?: ((error: unknown) => void) | undefined;
}) {
  const presentation = artworkPresentation(item);
  const [index, setIndex] = useState(0);
  const screenshots = presentation?.descriptor.screenshots || [];
  if (!presentation || (!screenshots.length && !presentation.descriptor.icon))
    return null;
  const selected = Math.min(index, screenshots.length - 1);
  const asset = screenshots[selected]!;
  return (
    <section className="artwork-gallery" aria-label="Resource screenshots">
      {presentation.descriptor.icon && (
        <figure>
          <ArtworkImage
            client={client}
            presentation={presentation}
            asset={presentation.descriptor.icon}
            slot="icon"
            onFailure={onFailure}
          />
          <figcaption>
            {presentation.descriptor.icon.rights ? (
              <p>
                Icon attribution:{" "}
                {presentation.descriptor.icon.rights.attribution}. Rights
                declared: {presentation.descriptor.icon.rights.basis}; legal
                clearance is not assessed.
              </p>
            ) : (
              <p>Source-provided native icon. Rights have not been assessed.</p>
            )}
            {presentation.mutableIcon && (
              <p className="muted">
                Native icon bytes may change independently of the entry.
              </p>
            )}
            {presentation.iconSource === "local_override" && (
              <p className="muted">Local consumer override</p>
            )}
          </figcaption>
        </figure>
      )}
      {screenshots.length > 0 && (
        <>
          <h3>Screenshots</h3>
          <figure>
            <ArtworkImage
              client={client}
              presentation={presentation}
              asset={asset}
              slot="screenshot"
              onFailure={onFailure}
              index={selected}
            />
            <figcaption>
              {asset.caption && <p>{asset.caption}</p>}
              <p>
                Image attribution: {asset.rights.attribution}. Rights declared:{" "}
                {asset.rights.basis}; legal clearance is not assessed.
              </p>
              <p className="muted">
                {presentation.screenshotsSource === "local_override"
                  ? "Local consumer override"
                  : presentation.screenshotsSource === "publisher"
                    ? "Source-provided artwork"
                    : "Listing artwork"}
              </p>
            </figcaption>
          </figure>
        </>
      )}
      {screenshots.length > 1 && (
        <div
          className="workspace-toolbar"
          role="group"
          aria-label="Choose screenshot"
        >
          <button
            type="button"
            className="icon-back"
            disabled={selected === 0}
            onClick={() => setIndex(selected - 1)}
          >
            {controlIcons["arrow"]} Previous screenshot
          </button>
          <span aria-live="polite">
            {selected + 1} of {screenshots.length}
          </span>
          <button
            type="button"
            disabled={selected === screenshots.length - 1}
            onClick={() => setIndex(selected + 1)}
          >
            {controlIcons["arrow"]} Next screenshot
          </button>
        </div>
      )}
    </section>
  );
}
