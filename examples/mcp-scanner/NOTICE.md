# MCP Scanner example notices

The first-party example uses Apache License 2.0. See the repository [LICENSE](../../LICENSE).

The optional tool image installs these independent upstream components:

| Component | License and source |
| --- | --- |
| Cisco MCP Scanner and bundled rules | [Apache License 2.0](https://github.com/cisco-ai-defense/mcp-scanner/blob/b27af3b0e66ea8d5ab9c294b47aba137f4c30752/LICENSE) |
| yara-python | [Apache License 2.0](https://github.com/VirusTotal/yara-python/blob/v4.5.4/LICENSE) |
| libyara, bundled by yara-python | [BSD 3-Clause notice](licenses/libyara-COPYING), from submodule `7ff39042be5c63682a037e13a75221d59393cf8b` |

Their wheels retain upstream licenses and attribution. The installation receipt inventories all installed distributions. Additional dependencies keep their own licenses in the image.

LiteLLM's bundled license distinguishes MIT files from any upstream `enterprise/` directory. The reviewed Linux wheel does not contain that directory. Keep its license intact. The complete tool image has multiple licenses; it is not an Apache-only bundle.

The scanner image is a local optional tool. It is not part of the Catalog backend, UI or SDK packages.
