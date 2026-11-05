import { useState } from "react";
import { Field } from "./workspace";

/** Open one exact authorized reference without searching or fetching parent records. */
export function ExactReference({
  basePath,
  path,
  label,
}: {
  basePath: string;
  path: string;
  label: string;
}) {
  const [id, setId] = useState("");
  return (
    <form
      className="workspace-toolbar workspace-lookup"
      aria-label={`Open exact ${label}`}
      method="get"
      action={`${basePath}/${path}/${encodeURIComponent(id.trim().toLowerCase())}`}
    >
      <Field
        label={`Exact ${label} ID`}
        hint="Enter the full ID to open a record you have permission to view."
      >
        <input
          required
          maxLength={36}
          pattern="[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"
          value={id}
          onChange={(event) => setId(event.target.value)}
        />
      </Field>
      <div className="workspace-actions">
        <button disabled={!id.trim()}>Open {label}</button>
      </div>
    </form>
  );
}
