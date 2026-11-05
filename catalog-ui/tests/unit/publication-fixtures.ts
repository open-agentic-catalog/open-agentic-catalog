// Retained publication values only; no server, process or database setup.
export const snapshot = {
  id: "00000000-0000-7000-8000-000000000002",
  tenantId: "tenant-a",
  host: "catalog.example.com",
  audience: "anonymous" as const,
  generation: 2,
  state: "candidate" as const,
  etag: '"snapshot"',
  createdAt: "2026-10-20T00:00:00Z",
  ardProfile: "ard-test",
  aiCatalogProfile: "ai-test",
  ard: { entries: [] },
  aiCatalog: { specVersion: "0.1", host: "catalog.example.com", entries: [] },
  exclusions: [],
};
export const baseline = {
  ...snapshot,
  id: "00000000-0000-7000-8000-abcdef000001",
  generation: 1,
  state: "active" as const,
  activatedAt: "2026-10-20T00:01:00Z",
};
