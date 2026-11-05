import { Facts } from "./Facts";
// Small current labels complement, never replace, exact retained identities.
import type { components } from "../api/schema";
import type { Access, Client } from "../api/client";
import { result } from "../api/client";
import { permitsNamespace } from "../api/permissions";
import { useNamespaceAccessState } from "./namespace-access";
import { useRead } from "./workspace";
import * as checks from "./responses";

/** Resolve only the selected namespace, and only when an authorized label is missing. */
export function SelectedNamespace(props: {
  id: string;
  label?: string | undefined;
  access?: Access | undefined;
  client: Client;
  onFailure: (error: unknown) => void;
}) {
  return props.label || !props.access ? (
    <>{props.label || props.id}</>
  ) : (
    <NamespaceLookup {...props} access={props.access} />
  );
}

function NamespaceLookup(
  props: Parameters<typeof SelectedNamespace>[0] & { access: Access },
) {
  const state = useNamespaceAccessState(props, props.id);
  const allowed = permitsNamespace(
    state.access,
    "namespace.read",
    "catalog.read",
    props.id,
  );
  const read = useRead(
    props.id + String(allowed),
    async (signal) => {
      if (!allowed) return { id: props.id, label: props.id };
      const namespace = result(
        await props.client.api.GET("/api/v1/namespaces/{namespaceId}", {
          params: { path: { namespaceId: props.id } },
          signal,
        }),
        (value) =>
          checks.Namespace(value) &&
          typeof value === "object" &&
          value !== null &&
          "id" in value &&
          value.id === props.id,
      );
      return { id: props.id, label: namespace.displayName };
    },
    props.onFailure,
  );
  return <>{read.data?.id === props.id ? read.data.label : props.id}</>;
}

export function CatalogPresentation({
  display,
  includePublisher = true,
  includeLicense = true,
  namespaceId,
  onNamespace,
}: {
  display: components["schemas"]["CatalogPresentation"] | undefined;
  namespaceId?: string;
  onNamespace?: ((namespaceId: string) => void) | undefined;
  includePublisher?: boolean;
  includeLicense?: boolean;
}) {
  if (!display) return null;
  return (
    <Facts
      className="catalog-presentation"
      rows={[
        ...(includePublisher && display.publisherName
          ? ([["Publisher", display.publisherName]] as const)
          : []),
        ...(display.namespaceName
          ? ([
              [
                "Namespace",
                namespaceId && onNamespace ? (
                  <button
                    type="button"
                    className="tag-filter namespace-filter"
                    aria-label={`Filter by namespace ${display.namespaceName}`}
                    title={`Show resources in ${display.namespaceName}`}
                    onClick={() => onNamespace(namespaceId)}
                  >
                    {display.namespaceName}
                  </button>
                ) : (
                  display.namespaceName
                ),
              ],
            ] as const)
          : []),
        ...(includeLicense && display.license
          ? ([["License", display.license]] as const)
          : []),
        ...(includeLicense && display.licenseOmitted
          ? ([
              ["License", "See full record for the complete declaration."],
            ] as const)
          : []),
      ]}
    />
  );
}

export function originLabel(origin: "local" | "indexed" | "live") {
  return origin === "local"
    ? "Local Catalog"
    : origin === "indexed"
      ? "Indexed external"
      : "Live external";
}
