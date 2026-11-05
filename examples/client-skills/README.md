# Assistant client Skills

[catalog-discovery](catalog-discovery/README.md) is a self-contained, client-neutral Agent Skills package for the installed `catalogctl` read commands. Copy the whole package into a supported client's Skill directory; it has no build-time or runtime dependency on the Catalog source checkout.

Run `make client-skill-check` from the repository root for package and CLI workflow checks. The [assistant guide](../../documentation/src/content/docs/guides/assistant-discovery.md) explains access and usage; report deterministic checks separately from model-backed behavior. No Skill is installed automatically.
