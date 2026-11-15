/** Malformed or repeated criteria must reach API refusal, never widen a directory. */
function sourceFilter(search: string, name: string) {
  const values = new URLSearchParams(search).getAll(name);
  return values.length === 0
    ? ""
    : values.length === 1 && values[0]
      ? values[0]
      : "unsupported";
}
export function sourceStatus(search: string) {
  return sourceFilter(search, "status");
}
export function sourceSyncState(search: string) {
  return sourceFilter(search, "syncState");
}
export function sourceDirectoryURL(
  basePath: string,
  status: string,
  syncState = "",
) {
  const query = new URLSearchParams();
  if (status) query.set("status", status);
  if (syncState) query.set("syncState", syncState);
  return `${basePath}/sources${query.size ? `?${query}` : ""}`;
}
