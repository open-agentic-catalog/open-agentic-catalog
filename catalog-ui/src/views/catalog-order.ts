import type { components } from "../api/schema";

export type CatalogOrder = components["schemas"]["CatalogSearchOrder"];
export const catalogOrders = [
  { value: "name_asc", label: "Name (A–Z)" },
  { value: "name_desc", label: "Name (Z–A)" },
  { value: "kind_asc", label: "Kind (A–Z)" },
  { value: "kind_desc", label: "Kind (Z–A)" },
  { value: "publisher_asc", label: "Publisher (A–Z)" },
  { value: "publisher_desc", label: "Publisher (Z–A)" },
] as const;

export function isCatalogOrder(value: unknown): value is CatalogOrder {
  return catalogOrders.some((order) => order.value === value);
}
