# Catalog discovery Skill

A portable [Agent Skills](https://agentskills.io/specification) package for read-only Open Agentic Catalog discovery using `catalogctl`. Copy this entire directory, including `references/` and `LICENSE`, without renaming it. No helper script, package manager, MCP server or vendor-specific metadata is required. Format compatibility does not establish identical activation or successful model behavior in every client.

## Setup

Install the project's `catalogctl` binary on PATH before starting the assistant. The binary must support `remote capabilities`, `remote search` and `remote record`. Configure the same command environment the assistant uses:

```sh
export CATALOG_REMOTE_BASE_URL=https://catalog.example.com
export CATALOG_REMOTE_TOKEN_FILE=/run/secrets/catalog-reader-token
```

The token file must already contain a bare access token issued for this Catalog, not CLI login JSON. Do not paste it into the conversation. Use an active Catalog membership with Reader permissions at tenant or exact-namespace scope for local releases.

When OAuth scopes are enforced, the token also needs `catalog.read`. Namespace-only Reader access is valid: local Search returns only the authorized population, and exact reads require access to the selected release. No administrator or private source-read permission is needed.

Indexed/live discovery additionally requires tenant `federation.discover`, supplied by the Discovery reader template or an appropriate custom grant. That grant does not supply local `resource.read`. Without federation permission, default Search selects local releases only; explicit indexed/live origins return `403` before source queries. Use `--origin local` to avoid live disclosure for a local Reader. With the additional permission, `--origin local --origin indexed` includes indexed evidence without live queries. Live Search also needs administrator-prepared sources and approved query disclosure.

`--namespace UUID` selects local releases only. Omit origins or select only `--origin local` with it. Combining a namespace with indexed/live origins is invalid, even with federation permission. The CLI refuses it before sending a request. The equivalent HTTP API request returns `400 INVALID_ARGUMENT`.

For a private CA, optionally set `CATALOG_REMOTE_CA_FILE` to a readable PEM trust bundle. Supply these inputs through the assistant's execution environment, not its prompt. The CLI does not load `.env` or issue tokens. This Skill uses manual bearer inputs and does not refresh them. The CLI separately supports human OIDC login and explicit `--login-file` refresh; this Skill does not invoke login or select that file. Renew its token outside the assistant session.

The token-file setting takes precedence over an environment token and fails closed if unreadable.

Use the API origin without `/api/v1`, a trailing slash or credentials in the URL. HTTPS verifies the server; literal loopback HTTP such as `http://127.0.0.1:8080` is available for local tests. `http://localhost:8080`, redirects, insecure TLS and result-selected origins are not supported. Configure network/tool permission for these three remote commands only; POST Search is a read operation. Never grant blanket permission for all `catalogctl` commands. Review whether query/result disclosure to the assistant and approved live sources is acceptable.

## Install in a client

Use the client's documented project-local Skill directory. For clients supporting `.agents/skills/`, copy this directory to `<project>/.agents/skills/catalog-discovery/`. Claude Code uses `<project>/.claude/skills/catalog-discovery/`; project Skill discovery must remain enabled (an empty `--setting-sources` disables it).

Start from the project root or explicitly include that project using the client's supported directory option. Installation paths and activation syntax belong to the client, not the Agent Skills format. Start a new session after installation and check that `catalog-discovery` is available.

No repository, home configuration or permission file is modified automatically.

Ask: "Use catalog-discovery to find order-processing resources in my configured Catalog, compare the results and inspect the best local release. Do not install or run anything." A client may also select the Skill automatically for a Catalog discovery request. It must be able to execute the installed CLI and read this package's relative references, even when its working directory is elsewhere. A client without command execution needs a different integration, not an automatic HTTP fallback.

See the [workflow](SKILL.md) and [response reference](references/results.md). Test first with synthetic data and a Reader identity. Missing credentials or permissions require setup outside the conversation, not an administrative fallback. The package never installs or invokes discovered resources.

## Maintainer checks

In the Open Agentic Catalog source checkout, run `make client-skill-check`. This validates the package against the pinned Skill profile and tests the documented CLI contract using disposable in-memory/loopback fixtures. It does not call a model or prove assistant behavior. Model-backed qualification uses the separate checklist in the project's testing documentation and records exact client/package versions and observed behavior.

License: [Apache License 2.0](LICENSE).
