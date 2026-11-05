/** A malformed or repeated status must reach the API's refusal, never widen a directory. */
export function sourceStatus(search: string) {
  const values = new URLSearchParams(search).getAll("status");
  return values.length === 0
    ? ""
    : values.length === 1 && values[0]
      ? values[0]
      : "unsupported";
}
export function sourceDirectoryURL(basePath: string, status: string) {
  const query = new URLSearchParams();
  if (status) query.set("status", status);
  return `${basePath}/sources${query.size ? `?${query}` : ""}`;
}
