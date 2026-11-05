# Representation adapters

`Registry.Validate` selects a pinned representation profile and returns normalized metadata, validation findings and retained package evidence to the Catalog service. Validation describes format compatibility; it does not approve a publisher or authorize execution.

| File | Responsibility |
| --- | --- |
| [registry.go](registry.go) | Profile selection, native JSON validation and Skill frontmatter parsing. |
| [a2a.go](a2a.go) | Pinned A2A ProtoJSON decoding and semantic Agent Card checks. |
| [skill_package.go](skill_package.go) | Bounded ZIP/tar inspection, safe paths and deterministic package inventory. |
| [plugin.go](plugin.go) | Agent Plugins manifest, component and MCP configuration inspection using the same archive boundary. |

Archives are inspected in memory without extracting or executing files. Preserve byte, entry, expansion, depth and path bounds before adding a supported archive case. Profile inputs and regeneration rules live in [pinned profiles](../../schemas/PROFILES.md); service-owned retained evidence lives in [catalog](../catalog/README.md).

Run `go test ./internal/adapter -shuffle=on -count=1` for adapter and adversarial archive coverage.
