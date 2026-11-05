/** Keep malformed or repeated selections for explicit API refusal, never an unfiltered read. */
export function webhookParameter(search: string, name: string) {
  const values = new URLSearchParams(search).getAll(name);
  return values.length === 0
    ? ""
    : values.length === 1 && values[0]
      ? values[0]
      : "unsupported";
}
export function webhookDeliveryFilters(search: string) {
  return {
    deliveryId: webhookParameter(search, "deliveryId"),
    eventId: webhookParameter(search, "eventId"),
    state: webhookParameter(search, "state"),
  };
}
export function webhookDirectoryURL(basePath: string, status: string) {
  const query = new URLSearchParams();
  if (status) query.set("status", status);
  return `${basePath}/webhooks${query.size ? `?${query}` : ""}`;
}
export function webhookDeliveryURL(
  basePath: string,
  endpoint: string,
  filters: ReturnType<typeof webhookDeliveryFilters>,
) {
  const query = new URLSearchParams();
  for (const [name, value] of Object.entries(filters))
    if (value) query.set(name, value);
  return `${basePath}/webhooks/${endpoint}${query.size ? `?${query}` : ""}`;
}
