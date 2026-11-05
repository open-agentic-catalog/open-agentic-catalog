// Download exact retained files on demand, using the private export endpoint.
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { Client } from "../api/client";
import { result } from "../api/client";
import type { components } from "../api/schema";
import { permitsNamespace } from "../api/permissions";
import { controlIcons } from "../shell/Icon";
import { Field } from "./workspace";
import { maximumDraftFileBytes, readDraftFile } from "./draft-file";

type Props = {
  client: Client;
  access: components["schemas"]["CurrentAccess"];
  namespaceId: string;
  revisionId: string;
  listingRevisionId?: string | undefined;
  onFailure: (cause: unknown) => void;
  heading?: "h2" | "h3";
};
export function DraftDownload(props: Props) {
  return permitsNamespace(
    props.access,
    "resource.read",
    "catalog.read",
    props.namespaceId,
  ) &&
    permitsNamespace(
      props.access,
      "resource.read_content",
      "catalog.read",
      props.namespaceId,
    ) ? (
    <RetainedDraftDownload
      key={`${props.revisionId}:${props.listingRevisionId}:${JSON.stringify(props.access)}`}
      {...props}
    />
  ) : null;
}
function RetainedDraftDownload({
  client,
  revisionId,
  listingRevisionId,
  onFailure,
  heading: Heading = "h2",
}: Props) {
  const [includeListing, setIncludeListing] = useState(false);
  const [listing, setListing] = useState(listingRevisionId ?? "");
  const [href, setHref] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const request = useRef<AbortController | undefined>(undefined);
  const url = useRef("");
  const notice = useRef<HTMLParagraphElement>(null);
  const link = useRef<HTMLAnchorElement>(null);
  const clear = () => {
    request.current?.abort();
    if (url.current) URL.revokeObjectURL(url.current);
    url.current = "";
    setHref("");
    setError("");
    setBusy(false);
  };
  useLayoutEffect(() => {
    setHref("");
    setBusy(false);
    setError("");
    return () => {
      request.current?.abort();
      if (url.current) URL.revokeObjectURL(url.current);
      url.current = "";
    };
  }, [client]);
  useEffect(() => {
    if (error) notice.current?.focus();
  }, [error]);
  useEffect(() => {
    if (href) link.current?.focus();
  }, [href]);
  return (
    <section className="workspace-panel" aria-label="Download saved draft JSON">
      <Heading>Download draft JSON</Heading>
      <p>
        Download this exact content revision as an editable file. Publisher
        identities, approvals and releases are excluded.
      </p>
      <Field label="Include Listing metadata">
        <input
          type="checkbox"
          checked={includeListing}
          onChange={(event) => {
            clear();
            setIncludeListing(event.target.checked);
          }}
        />
      </Field>
      {includeListing && (
        <Field
          label="Exact ListingRevision ID"
          hint="The Listing revision must reference this content revision."
        >
          <input
            value={listing}
            readOnly={!!listingRevisionId}
            onChange={(event) => {
              clear();
              setListing(event.target.value);
            }}
          />
        </Field>
      )}
      <div className="workspace-actions document-downloads">
        <button
          type="button"
          disabled={busy}
          onClick={() => {
            clear();
            if (
              includeListing &&
              !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(
                listing,
              )
            ) {
              setError(
                "Enter the exact ListingRevision ID, or exclude Listing metadata.",
              );
              return;
            }
            const controller = new AbortController();
            request.current = controller;
            setBusy(true);
            void (async () => {
              try {
                const reply = await client.api.GET(
                  "/api/v1/version-revisions/{revisionId}/export",
                  {
                    params: {
                      path: { revisionId },
                      ...(includeListing
                        ? { query: { listingRevisionId: listing } }
                        : {}),
                    },
                    parseAs: "arrayBuffer",
                    signal: controller.signal,
                  },
                );
                const bytes = result(reply);
                if (
                  bytes.byteLength > maximumDraftFileBytes ||
                  !/^application\/json(?:;|$)/i.test(
                    reply.response.headers.get("Content-Type") ?? "",
                  )
                )
                  throw new Error(
                    "The draft download has an unsupported size or media type.",
                  );
                const hash = new Uint8Array(
                  await crypto.subtle.digest("SHA-256", bytes),
                );
                if (
                  reply.response.headers.get("Content-Digest") !==
                  `sha-256=:${btoa(String.fromCharCode(...hash))}:`
                )
                  throw new Error("Draft checksum verification failed.");
                readDraftFile(new Uint8Array(bytes));
                result(reply);
                if (controller.signal.aborted) return;
                url.current = URL.createObjectURL(
                  new Blob([bytes], { type: "application/json" }),
                );
                setHref(url.current);
              } catch (cause) {
                if (!controller.signal.aborted) {
                  setError((cause as Error).message);
                  onFailure(cause);
                }
              } finally {
                if (!controller.signal.aborted) setBusy(false);
              }
            })();
          }}
        >
          {controlIcons.download} Prepare draft download
        </button>
        {href && (
          <a
            ref={link}
            className="button"
            href={href}
            download="catalog-draft.json"
          >
            {controlIcons.download} Save catalog-draft.json
          </a>
        )}
      </div>
      {busy && <p role="status">Prepare draft download...</p>}
      {error && (
        <p ref={notice} tabIndex={-1} role="alert">
          {error}
        </p>
      )}
    </section>
  );
}
