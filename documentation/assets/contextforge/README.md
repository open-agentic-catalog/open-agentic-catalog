# ContextForge screenshot

`virtual-servers.png` shows the built-in Admin UI in ContextForge 1.1.0. The owned fixture uses synthetic users and server metadata.

Follow the [setup guide](../../src/content/docs/guides/contextforge.mdx) through registration. Run its optional `browser` command before the proxy checks create another virtual server.

The browser checks visible text for passwords and tokens before capture. It saves `admin-ui.png` in the owned run directory without browser storage or traces.

Stop the owned fixture after the selected checks, as the setup guide describes. Check `browser.json` for `status: passed` and all five browser flags set to `true`. Check `report.json` for `cleanup`, `resourcesRemoved` and `privateRemoved` set to `true`. Use that report's `cases.contextforge.version`, `installation.reference`, `installation.image` and `source` fields for the capture reference.

Inspect the image for credentials, private documents and unrelated browser content. Copy only the reviewed image:

```sh
install -m 0644 -- "$CONTEXTFORGE_RUN/admin-ui.png" documentation/assets/contextforge/virtual-servers.png
sha256sum -- "$CONTEXTFORGE_RUN/admin-ui.png" documentation/assets/contextforge/virtual-servers.png
```

Require identical hashes. Update the reference below from the same owned run. Do not copy raw reports, credentials, browser sessions or traces into documentation. Ordinary builds use only the reviewed PNG; they do not read private run evidence or start a browser.

Keep the native feature-disabled banner visible. It concerns ContextForge's own optional catalog, not Open Agentic Catalog.

Run `make docs-site-check` after replacement. The asset allowlist copies this exact file into root and subpath builds.

## Reviewed capture

The maintained PNG matches this completed capture. Its bytes remain unchanged after the association review on 2026-11-02.

| Field | Reviewed value |
| --- | --- |
| UI | ContextForge 1.1.0 built-in Admin UI, Virtual Servers tab, entered through `/v1/admin/`. |
| Owned run | `catalog-contextforge-964e6ecec56b` |
| Captured file | `output/interoperability/contextforge/catalog-contextforge-964e6ecec56b/admin-ui.png` |
| Captured and maintained PNG SHA-256 | `9d685b8643d874fd7d7c642d9ab975448cdc25a2cc0c0964de114ca658100f6d` |
| Selected image reference | `ghcr.io/ibm/mcp-context-forge:v1.1.0@sha256:7eb21fb9a8070df59435c03c76c40d8c71230c55ba89f884f2482d2cc81cff1c` |
| Local image ID | `sha256:453c88b12b57b1f1558a59e36e4ff8e068fab1d978eb12b7e1fdc9f1afe8b619` |
| Source fingerprint | `854cee5a8f2be0fe9dca765143fcea2f3b7d6ea8ce9654bfd315c96f712da786` |
| Browser result | `status: passed`; login, servers, gateways, logout and `localRequestsOnly` all pass. |
| Cleanup | `cleanup`, `resourcesRemoved` and `privateRemoved` are all `true`; private inputs are absent. |

Visual review confirms synthetic `native-a`, `native-b` and `native-everything` registrations, the disabled-catalog banner and no visible credentials or unrelated content. This is the built-in Admin UI, not the separate upstream Web UI. Matching hashes establish byte equality, not authenticity or privacy certification.

## Upstream registrations

`upstream-registrations.png` shows the same native Admin UI's **MCP Servers** table. The browser owner captures it after checking `native-a`, `native-b` and `native-everything`. Run `browser` immediately after `register`, before proxy checks add more servers. Its `upstream-registrations.png` remains in the owned directory until reviewed promotion.

Before each image, require no visible login password or JWT-shaped text. Also check the selected table against known fixture secrets. These finite checks do not detect every possible opaque token. Keep credential forms closed. Require all browser flags, logout and successful scoped cleanup as described above. Preserve the existing Virtual Servers image; the new table shows upstream registrations rather than curated endpoints.

The upstream table uses passing run `catalog-contextforge-0b415e6d6adc`, with browser login, registrations, logout and both integration flows complete. Scoped cleanup removed all owned resources and private inputs. Its 1920-by-1200 viewport fits every registration column; the browser asserts their bounds before capture.
