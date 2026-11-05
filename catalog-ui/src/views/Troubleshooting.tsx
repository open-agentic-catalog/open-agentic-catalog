import { controlIcons } from "../shell/Icon";
import { Facts } from "./Facts";
// Public guidance is independent of authenticated, explicitly loaded diagnostic details.
import { useState } from "react";
import type { Access, Client, Runtime } from "../api/client";
import { result } from "../api/client";
import { documentationLink } from "../api/session";
import { Heading, ReadState, useRead } from "./workspace";
import { JSONBlock } from "./CodeBlock";
import { CopyText } from "./CopyReference";
import { diagnosticDetails, type DiagnosticError } from "./diagnostic-details";
import * as checks from "./responses";

type Props = {
  client: Client;
  runtime: Runtime;
  access: Access | undefined;
  onFailure: (cause: unknown) => void;
  recent?: DiagnosticError | undefined;
};
export default function Troubleshooting(props: Props) {
  const manual = documentationLink(props.runtime.documentationUrl);
  return (
    <>
      <Heading
        title="Troubleshooting"
        icon="book"
        description="Recover from common Catalog UI problems and inspect safe diagnostic details."
      />
      <section className="workspace-panel">
        <h2>Find a next step</h2>
        <Facts
          rows={[
            [
              "Sign-in fails",
              "Retry sign-in or reload login options. For OIDC, ask your operator to check the configured issuer, client and redirect URI. Do not share passwords or login tokens.",
            ],
            [
              "Insufficient access",
              "Check your current permissions and OAuth scopes in My access. A visible navigation link does not grant an action. Ask an administrator for the exact tenant or namespace permission.",
            ],
            [
              "No search matches",
              "Clear restrictive filters and check the selected kind, namespace and source. Authoring drafts and released Listings are different; a release is not automatically active in discovery.",
            ],
            [
              "The record changed",
              "Reload the latest revision and review your unsaved changes before trying again. Do not overwrite changes you have not reviewed.",
            ],
            [
              "A command has an uncertain outcome",
              "A failed response can follow a successful write. Keep the prepared command and retry explicitly with its same key, target, body and revision. Do not create a replacement command to guess the outcome.",
            ],
            [
              "A dependency is unavailable",
              "Inspect Service information and retry reads explicitly after recovery. A temporary API failure does not authorize a mutation retry or imply that stored data is lost.",
            ],
          ]}
        />
        {props.access ? (
          <p>
            <a href={`${props.runtime.basePath}/access`}>My access</a>
            {" / "}
            <a href={`${props.runtime.basePath}/service`}>
              Service information
            </a>
          </p>
        ) : (
          <p>
            <a href={`${props.runtime.basePath}/catalog`}>Sign in</a> to inspect
            current access and authenticated service details.
          </p>
        )}
        {manual ? (
          <p>
            <a
              href={
                manual === "/docs/"
                  ? "/docs/guides/catalog-ui/#troubleshooting"
                  : manual
              }
              target="_blank"
              rel="noopener noreferrer"
            >
              {controlIcons["external"]}
              {manual === "/docs/"
                ? "Troubleshooting in the manual"
                : "Documentation"}{" "}
              (opens in a new tab)
            </a>
          </p>
        ) : (
          <p>
            Documentation is not configured. This guidance remains available
            without the manual.
          </p>
        )}
        <p>
          Item feedback belongs to an exact Catalog resource. For a UI problem,
          give your internal operator a description and carefully reviewed
          diagnostic details. This page configures no external support
          destination or telemetry upload.
        </p>
      </section>
      {props.access && (
        <DiagnosticPreview key={JSON.stringify(props.access)} {...props} />
      )}
    </>
  );
}

function DiagnosticPreview(props: Props) {
  const [open, setOpen] = useState(false);
  return (
    <section className="workspace-panel">
      <h2>Diagnostic details</h2>
      <p>
        Preview API/UI build facts and safe error codes or response request IDs
        available in this tab. Request IDs can identify a UI-host failure or
        correlate an upstream API response. Review the preview before copying
        it.
      </p>
      <p>
        Tokens, cookies, credentials, identities, tenant and object metadata,
        search text and raw logs are excluded. Copying stays local and makes no
        request or upload.
      </p>
      <button type="button" aria-expanded={open} onClick={() => setOpen(!open)}>
        {open ? controlIcons["close"] : controlIcons["eye"]}
        {open ? "Close diagnostic preview" : "Preview diagnostic details"}
      </button>
      {open && <DiagnosticRead {...props} />}
    </section>
  );
}

function DiagnosticRead(props: Props) {
  const api = useRead(
    "diagnostic-api-build",
    async (signal) =>
      result(
        await props.client.api.GET("/api/v1/service-information", { signal }),
        checks.ServiceInformation,
      ),
    props.onFailure,
  );
  const ui = useRead(
    "diagnostic-ui-build",
    (signal) => props.client.buildInfo(signal),
    props.onFailure,
  );
  const report = diagnosticDetails(
    api.data,
    ui.data,
    { api: api.problem, ui: ui.problem },
    props.recent,
  );
  const text = JSON.stringify(report, null, 2);
  return (
    <>
      <ReadState {...api} />
      <ReadState {...ui} />
      {!api.busy && !ui.busy && (
        <>
          <JSONBlock value={report} />
          <CopyText
            label="Copy diagnostic details"
            text={text}
            disabled={new TextEncoder().encode(text).length > 8192}
          />
        </>
      )}
    </>
  );
}
