import { useState } from "react";
import { permitsNamespace } from "../api/permissions";
import { result } from "../api/client";
import { page as checkedPage } from "../api/values";
import * as checks from "./responses";
import { ChoiceField } from "./authoring";
import { Field } from "./workspace";
import type { WorkspaceProps } from "./workspace";

/** Optional bounded convenience, while exact IDs preserve cross-listing replacements. */
export function ReleaseReplacement({
  props,
  namespaceId,
  listingId,
  releaseId,
  value,
  onChange,
  required,
}: {
  props: WorkspaceProps;
  namespaceId: string;
  listingId: string;
  releaseId: string;
  value: string;
  onChange: (value: string) => void;
  required: boolean;
}) {
  const [show, setShow] = useState(false);
  const allowed = permitsNamespace(
    props.access,
    "resource.read_history",
    "catalog.read",
    namespaceId,
  );
  return (
    <fieldset>
      <legend>Replacement guidance</legend>
      <Field
        label="Replacement release ID"
        hint="Enter the UUID of another published or deprecated release in this tenant. It can belong to another listing. The Catalog checks it when you confirm."
      >
        <input
          required={required}
          value={value}
          onChange={(event) => onChange(event.target.value)}
        />
      </Field>
      {allowed && (
        <button
          type="button"
          aria-expanded={show}
          onClick={() => setShow(!show)}
        >
          {show ? "Hide" : "Choose"} same-listing replacement
        </button>
      )}
      {show && allowed && (
        <ChoiceField
          label="Same-listing replacement"
          value={value}
          onChange={onChange}
          required={false}
          onFailure={props.onFailure}
          nextSteps={
            <p>
              Only other published or deprecated releases are offered on this
              page. Page forward or enter an exact cross-listing ID above. No
              selection is required for deprecation.
            </p>
          }
          load={async (cursor, signal) => {
            const page = result(
              await props.client.api.GET(
                "/api/v1/catalog-listings/{listingId}/releases",
                {
                  params: {
                    path: { listingId },
                    query: { limit: 25, ...(cursor ? { cursor } : {}) },
                  },
                  signal,
                },
              ),
              checkedPage(checks.ListingRelease),
            );
            return {
              ...page,
              items: page.items
                .filter(
                  (item) =>
                    item.id !== releaseId &&
                    (item.state === "published" || item.state === "deprecated"),
                )
                .map((item) => ({
                  id: item.id,
                  label: `${item.id} (${item.state})`,
                })),
            };
          }}
        />
      )}
    </fieldset>
  );
}
