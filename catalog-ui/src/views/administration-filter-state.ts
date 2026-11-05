import type { DirectoryQuery } from "./AccessControls";

/** Repeated and empty selections reach API refusal instead of widening the list. */
export function administrationParameter(search: string, name: string) {
  const values = new URLSearchParams(search).getAll(name);
  return values.length === 0
    ? ""
    : values.length === 1 && values[0]
      ? values[0]
      : "unsupported";
}
export function administrationDirectory(
  search: string,
  members = false,
): DirectoryQuery {
  const state = administrationParameter(search, "state");
  const actorType = members ? administrationParameter(search, "actorType") : "";
  return {
    ...(state ? { state: state as NonNullable<DirectoryQuery["state"]> } : {}),
    ...(actorType
      ? { actorType: actorType as NonNullable<DirectoryQuery["actorType"]> }
      : {}),
  };
}
export function directorySearch(query: DirectoryQuery) {
  const values = new URLSearchParams();
  if (query.state) values.set("state", query.state);
  if (query.actorType) values.set("actorType", query.actorType);
  return values.size ? `?${values}` : "";
}
export function moderationSearch(filters: {
  state: string;
  category?: string;
}) {
  const values = new URLSearchParams();
  if (filters.state) values.set("state", filters.state);
  if (filters.category) values.set("category", filters.category);
  return values.size ? `?${values}` : "";
}
