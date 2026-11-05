import { checkAccess } from "../api/session";
// Namespace-specific self-context supplements the caller's first grant page for the active editor.
import { result } from "../api/client";
import type { WorkspaceProps } from "./workspace";
import { useRead } from "./workspace";

// Resolve only the active editor's namespace when it is beyond the first
// self-context page. Never scan all tenant grants to enable an action hint.
export function useNamespaceAccess(
  props: Pick<WorkspaceProps, "client" | "access" | "onFailure">,
  namespaceId: string,
) {
  return useNamespaceAccessState(props, namespaceId).access;
}

/** Expose deliberate recovery for editors that must resolve a later-page grant. */
export function useNamespaceAccessState(
  props: Pick<WorkspaceProps, "client" | "access" | "onFailure">,
  namespaceId: string,
) {
  const initial = props.access;
  const known =
    !namespaceId ||
    !initial.nextCursor ||
    initial.namespaceGrants.some((grant) => grant.namespaceId === namespaceId);
  const key = namespaceId + JSON.stringify(initial);
  const read = useRead(
    key,
    async (signal) => ({
      key,
      access: known
        ? initial
        : result(
            await props.client.api.GET("/api/v1/me", {
              params: { query: { namespaceId } },
              signal,
            }),
            checkAccess,
          ),
    }),
    props.onFailure,
  );
  const current = read.data?.key === key;
  return {
    access: read.data?.key === key ? read.data.access : initial,
    busy: !known && ((!current && !read.error) || read.busy),
    error: known ? "" : read.error,
    refresh: read.refresh,
  };
}
