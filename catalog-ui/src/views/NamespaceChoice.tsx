import { availablePermission } from "../api/permissions";
import { result } from "../api/client";
import { ChoiceField } from "./authoring";
import { ChoicePrerequisite } from "./ChoicePrerequisite";
import * as checks from "./responses";
import { Field, type WorkspaceProps } from "./workspace";

/** Directory reads are optional; exact namespace authority is checked separately. */
export function NamespaceChoice({
  workspace,
  value,
  onChange,
  label = "Namespace",
  preserveDraft = false,
}: {
  workspace: WorkspaceProps;
  value: string;
  onChange: (value: string) => void;
  label?: string;
  preserveDraft?: boolean;
}) {
  return availablePermission(
    workspace.access,
    "namespace.read",
    "catalog.read",
  ) ? (
    <ChoiceField
      label={label}
      manualLabel={`${label} ID`}
      nextSteps={
        <ChoicePrerequisite
          props={workspace}
          kind="namespace"
          newTab={preserveDraft}
        />
      }
      value={value}
      onChange={onChange}
      onFailure={workspace.onFailure}
      load={async (cursor, signal) => {
        const page = result(
          await workspace.client.api.GET("/api/v1/namespaces", {
            params: { query: { limit: 25, ...(cursor ? { cursor } : {}) } },
            signal,
          }),
          checks.NamespacePage,
        );
        return {
          ...page,
          items: page.items.map((item) => ({
            id: item.id,
            label: `${item.displayName} / ${item.slug} / ${item.id}`,
          })),
        };
      }}
    />
  ) : (
    <Field
      label={`${label} ID`}
      hint="Enter an exact namespace ID. Your current access does not allow listing namespace names. The Catalog checks the destination when you confirm."
    >
      <input
        required
        value={value}
        onChange={(event) => onChange(event.target.value)}
      />
    </Field>
  );
}
