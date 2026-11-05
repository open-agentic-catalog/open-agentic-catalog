# Discover developer tools with LangGraph

Use this example to ask a local model for resources in your Catalog. The assistant uses the [Python SDK](../../sdk/python/README.md) to search locally governed releases and inspect selected records. It can recommend Agents, MCP Servers, Skills and Plugins. It does not install resources, invoke endpoints or change Catalog data.

[LangGraph](https://docs.langchain.com/oss/python/langgraph/overview) is a Python library for stateful model and tool workflows. [ChatOllama](https://docs.langchain.com/oss/python/integrations/chat/ollama) connects this example to [Ollama](https://docs.ollama.com/quickstart), a local model server. The assistant's two tools have fixed read-only capabilities. The model cannot add tools, choose a Catalog origin, change credentials or select a namespace.

Use the [integration example chooser](../../documentation/src/content/docs/guides/integrating.mdx#choose-an-integration-example) to compare this graph with the terminal assistant Skill, OIDC tutorial and webhook receiver.

## Before you start

| Requirement | Prepare it |
| --- | --- |
| Python and installer | Python 3.12-3.14, GNU Make and [uv](https://docs.astral.sh/uv/getting-started/installation/) 0.13.4. Select that exact uv executable in the install command below. |
| Go for installation | Use the toolchain selected by `go.mod`. The maintained installer runs `go env GOROOT` and hashes that Go binary when recording installed inputs. Running the installed Python agent itself does not require Go. |
| Populated Catalog | Follow the owned [curated sample Catalog launch](../../documentation/src/content/docs/guides/sample-catalog.mdx#start-the-catalog-with-compose-and-load-sample-data). Keep the same project, ports, archive inputs and receipt through the launcher or standalone sequence. Do not seed or change another person's retained stack. |
| Read access | An active [membership](../../documentation/src/content/docs/reference/glossary.md#membership) with `resource.read` in **Catalog demo** and **IDE plugins**, or tenant-wide read permission. Reader supplies this permission. Enforced OAuth [scopes](../../documentation/src/content/docs/reference/glossary.md#scope) additionally require `catalog.read`. |
| Local model | Tool-capable [Ollama](https://docs.ollama.com/quickstart); the selected runtime is 0.40.10 and model is `qwen2.5:7b`. Exact runtime/model identities live in `test/langgraph/model.json`. |
| Capacity and downloads | About 4.7 GB for model weights and at least 8 GB RAM for inference, plus Catalog capacity. Dependency/image/weight acquisition needs internet and ordinary disk space; inference stays local. |

Sample metadata does not start a resource runtime. The assistant makes no request to a discovered resource. It needs no administrator, source-management or mutation authority.

The assistant searches **local releases only**. Indexed and live federation entries are excluded by its fixed `origins=["local"]` request. A namespace filter narrows that local collection and never grants access. Leave it unset for the four-kind tour because the samples span two namespaces.

### Check the published samples

Open the sample UI and choose its configured reader, or inspect the four known matches with the sample CLI. The sample guide selects API port 18086 and UI port 14186. If you chose other ports, retain the actual API port in `CATALOG_COMPOSE_HTTP_PORT` or replace the API origin below. Keep this verified origin for the agent's later requests.

```sh
make build
export CATALOG_REMOTE_BASE_URL="http://127.0.0.1:${CATALOG_COMPOSE_HTTP_PORT:-18086}"
unset CATALOG_REMOTE_TOKEN_FILE
export CATALOG_REMOTE_TOKEN=local-reader # Development fixture only.
./bin/catalogctl remote search --origin local --kind skill --text testing
./bin/catalogctl remote search --origin local --kind mcp_server --text everything
./bin/catalogctl remote search --origin local --kind plugin --text "web testing toolkit"
./bin/catalogctl remote search --origin local --kind agent --text "hello world"
```

| Kind | Expected readable local sample | Namespace |
| --- | --- | --- |
| Skill | Web Application Testing | Catalog demo |
| MCP Server | MCP Everything | Catalog demo |
| Plugin | Web Testing Toolkit | IDE plugins |
| Agent | A2A Hello World | Catalog demo |

Check `data.items[].displayName`, `type: "local"` and `local.release.id`. Release UUIDs belong to your fixture; do not copy one from another run. If a named result is missing, fix dataset selection, release eligibility or read access before starting the model. The [search cookbook](../../documentation/src/content/docs/guides/search-cookbook.mdx) supplies the complete UI/CLI recipes.

### Install the locked environment

Run these commands from the repository root:

```sh
make langgraph-tools-install UV=/path/to/uv-0.13.4 DEV_PYTHON=python3
```

Replace the uv path with your installed executable. Installation creates `examples/langgraph/.venv/` from the checked-in lock and installs the current local SDK source. It downloads Python dependencies when needed, but no model weights. Rerun installation after a root version bump to refresh local SDK metadata. Contributor checks are a [separate verification path](#verify-the-example), not prerequisites for asking the first question.

`make clean` removes this owned environment, including its installation evidence. Run the installation command again before using the example. Other personal virtual environments and model downloads are preserved; see [cleanup scope](../../documentation/src/content/docs/contributing/build-and-release.md#clean-local-builds-and-dependencies).

The framework dependencies belong only to this optional example. They are not dependencies of the SDK package, API image or UI image. The locked closure includes dependencies with Mozilla Public License 2.0 terms. See [the dependency policy](../../documentation/src/content/docs/reference/dependencies.md) before redistributing that environment.

## Start a local model

Run your own [Ollama](https://docs.ollama.com/quickstart) service on literal loopback. Select a local model with [tool-call support](https://docs.ollama.com/capabilities/tool-calling). The selected runtime is Ollama 0.40.10. Its exact container image is recorded in `test/langgraph/model.json`. The default model is [Qwen2.5 7B](https://ollama.com/library/qwen2.5).

For an installed Ollama executable, start the service in a separate terminal:

```sh
OLLAMA_HOST=127.0.0.1:11434 OLLAMA_NO_CLOUD=1 ollama serve
```

Download the selected model explicitly:

```sh
OLLAMA_HOST=127.0.0.1:11434 ollama pull qwen2.5:7b
```

Wait for the download to finish before running the assistant. In another terminal, check the maintained model's identity and tool capability:

```sh
examples/langgraph/.venv/bin/python -B - <<'PY'
import json
from examples.langgraph.agent import DemoError, check_local_model

profile = json.load(open("test/langgraph/model.json"))
try:
    check_local_model("http://127.0.0.1:11434", profile["model"], profile["digest"])
except DemoError as error:
    raise SystemExit(str(error)) from None
print("Pinned local model is ready.")
PY
```

Use the selected loopback port in this check if you changed it. `local_model_unavailable` means the service/model is not ready. `local_model_identity_changed` means the installed model does not match the checked-in qualification identity.

The selected weights use about 4.7 GB of disk space. Allow at least 8 GB of RAM for local inference, plus capacity for the Catalog. Downloading weights needs internet access and disk space. Inference runs locally after the download. Model tags can change; retain the model digest and runtime identity with qualification results. Other models may produce different tool calls or refuse the required response format. A small model is not guaranteed to complete every request.

The example accepts only an HTTP model origin with literal `127.0.0.1` or `::1`. It refuses hosted origins and cloud model names. A bounded local model check also rejects remote-backed models and models without declared tool support. Do not configure an Ollama service that forwards inference to a hosted provider. Configure resource limits and local access controls for your service separately.

### Use the pinned container instead

This alternative uses the image in `test/langgraph/model.json` and retains downloaded weights on ordinary disk. Run it from the repository root. Choose another container name or port if these are already used.

```sh
mkdir -p output/langgraph-model
chmod 700 output/langgraph-model
docker run --rm --name catalog-langgraph-model \
  --user "$(id -u):$(id -g)" --cap-drop=ALL \
  --security-opt=no-new-privileges --pids-limit=256 --memory=8g --cpus=4 \
  -p 127.0.0.1:11434:11434 \
  -e HOME=/model -e OLLAMA_MODELS=/model/models -e OLLAMA_NO_CLOUD=1 \
  -e OLLAMA_HOST=0.0.0.0:11434 -e OLLAMA_CONTEXT_LENGTH=4096 \
  -e OLLAMA_NUM_PARALLEL=1 -e OLLAMA_MAX_LOADED_MODELS=1 \
  -v "$PWD/output/langgraph-model:/model" \
  "$(python3 -c 'import json; print(json.load(open("test/langgraph/model.json"))["runtimeImage"])')"
```

In another terminal, download the weights if they are absent:

```sh
docker exec catalog-langgraph-model ollama pull qwen2.5:7b
```

The image listens inside its container; only loopback port 11434 is published. After the pull, run the model-readiness check above. Stop only this container with `docker stop catalog-langgraph-model`. The cache remains for later runs. No Catalog data or credentials are mounted into it.

## Ask a discovery question

Supply `CATALOG_SDK_TOKEN` through your development secret mechanism. Do not put a real token in source files, command arguments or shell history. The SDK does not use the UI cookie or acquire a token automatically.

For the owned development API verified above:

```sh
export CATALOG_SDK_ORIGIN="$CATALOG_REMOTE_BASE_URL"
export CATALOG_SDK_TOKEN=local-reader # Development fixture only.
export LANGGRAPH_ALLOW_LOOPBACK_HTTP=1
export LANGGRAPH_MODEL_ORIGIN=http://127.0.0.1:11434
export LANGGRAPH_MODEL=qwen2.5:7b
unset LANGGRAPH_NAMESPACE_ID
export LANGGRAPH_QUESTION='Find one Skill for web testing. Use the search term testing, inspect a matching release, then explain your choice.'
make langgraph-demo
```

Use the API origin verified in the published-sample checks above. The sample guide's API port 18086 and UI port 14186 serve different interfaces. Loopback HTTP is an explicit development option. Use HTTPS for a remote Catalog. Set `CATALOG_SDK_CA_FILE` to a PEM CA file when that API uses private trust. Certificate and hostname verification remain enabled.

The [human OIDC branch](#use-a-human-oidc-login) below supplies a concrete alternative to the synthetic development token. The example takes one access token per run and does not refresh it.

These environment inputs belong to the example; the SDK does not load them automatically. The Python program does not load `.env`. The Make launcher retains the repository's normal environment-file rules; its isolated test targets bypass those inputs.

The launcher preserves `LANGGRAPH_*` environment and command-line values as literal text. It does not evaluate Make expressions in them. Quote shell assignments that contain spaces or special characters. The repository's workflow `.env` remains trusted Make syntax.

### Ask all four questions

Keep the same Catalog/model inputs and leave `LANGGRAPH_NAMESPACE_ID` unset. Run each question separately:

```sh
export LANGGRAPH_QUESTION='Find one demonstration MCP Server. Use the search term everything and kind mcp_server, inspect a matching release, then explain your choice.'
make langgraph-demo
export LANGGRAPH_QUESTION='Find one browser-testing Plugin. Use the search term web testing toolkit and kind plugin, inspect a matching release, then explain your choice.'
make langgraph-demo
export LANGGRAPH_QUESTION='Find one simple A2A demonstration Agent. Use the search term hello world and kind agent, inspect a matching release, then explain your choice.'
make langgraph-demo
```

The expected named matches are **MCP Everything**, **Web Testing Toolkit** and **A2A Hello World**. Model selection and wording can vary. A successful recommendation still requires Search followed by an exact read of that same local release during this run. A recommendation does not establish that a runtime is reachable or a package is safe to execute.

To demonstrate namespace filtering separately, obtain the actual **Catalog demo** UUID from an authorized read. Set `LANGGRAPH_NAMESPACE_ID` to it, then repeat the Skill question. This excludes the Plugin in **IDE plugins**, while retaining eligible Catalog demo releases. Unset the variable before returning to the four-kind tour.

### Read the answer

Successful runs print JSON to stdout. This annotated shape uses the sample Skill's metadata. `RELEASE_UUID_FROM_THIS_RUN` stands for the actual UUID printed by your run, not an ID to submit. Model summary/reason text can vary:

```json
{
  "summary": "A matching published Skill is available.",
  "search_incomplete": false,
  "recommendations": [
    {
      "release_id": "RELEASE_UUID_FROM_THIS_RUN",
      "kind": "skill",
      "name": "Web Application Testing",
      "description": "Complete upstream Apache-2.0 Skill package. Copy the entire folder into your Skill-capable client; Python and Playwright/browser installation required. See samples/README.md.",
      "version": "source-8a1541c4",
      "license": "Apache-2.0",
      "documentation": "https://github.com/anthropics/skills/tree/8a1541c4a3ffa5a20a5a91de0dcf3f0bab1d1ef4/skills/webapp-testing",
      "git_handoffs": [
        {
          "repository": "https://github.com/anthropics/skills",
          "commit": "8a1541c4a3ffa5a20a5a91de0dcf3f0bab1d1ef4",
          "directory": "skills/webapp-testing"
        }
      ],
      "reason": "Its description matches the requested task."
    }
  ]
}
```

After selected records are read, the final model call uses the answer JSON Schema and has no tools. The program accepts complete JSON, including the framework's usual JSON code fence. It does not repair truncated JSON. The model supplies the summary and reason. The program supplies resource facts from successful exact record reads. A recommendation must name a release returned by Search and inspected in the same run. Duplicate or unread recommendations fail instead of becoming invented resources.

`release_id` is the bare local ListingRelease UUID, not a Resource UUID or the Search item's `local:`-prefixed ID. `name`, `kind`, `version`, license and safe links come from the exact record read. `summary` and `reason` are model prose. `git_handoffs` are inspected immutable source references, not a claim that a package was downloaded. See [result evidence](../../documentation/src/content/docs/guides/understand-results.mdx) for those limits.

An empty recommendation list is valid when Search has no useful match. The model chooses the Search text; a task can need a more specific keyword. Try a known listing term before you infer that the Catalog lacks that resource. `search_incomplete: true` means a Search reported partial results or more pages remained beyond the example's budget. It does not mean that all matching resources were inspected. Treat the model's prose as advice to review, not a verified assessment.

## Use a human OIDC login

Prepare the [interactive Keycloak tutorial](../../documentation/src/content/docs/operations/oidc-keycloak.md#run-the-interactive-local-tutorial), its public `catalog-cli` client and a human Reader account. The [two-Catalog tutorial](../../documentation/src/content/docs/guides/two-catalog-federation.mdx) supplies a local Agent on A through its initial publication steps. An already populated OIDC Catalog is another valid input.

[OIDC and PKCE](../../documentation/src/content/docs/reference/glossary.md#oidc-and-pkce) let the CLI verify the provider login and bind its authorization-code exchange. The provider account/password and Catalog membership remain separate.

This branch searches A's local Agent. It does not promise the four curated development samples in a fresh OIDC setup. Development seeding and `demoArchives` are not OIDC loaders. To search local content on B, first adopt, validate, review and publish it there; the assistant cannot inspect B's external observations.

Use the private CA from the interactive tutorial. Sign in to a new selected credential file:

```sh
mkdir -p "$HOME/.catalog-login"
chmod 700 "$HOME/.catalog-login"
./bin/catalogctl auth login \
  --issuer https://identity.test:8447/realms/catalog \
  --client-id catalog-cli \
  --base-url https://catalog-a.test:8443 \
  --scope catalog.read \
  --issuer-ca-file "$CATALOG_TUTORIAL_DIRECTORY/trust/ca.pem" \
  --ca-file "$CATALOG_TUTORIAL_DIRECTORY/trust/ca.pem" \
  --file "$HOME/.catalog-login/langgraph.json"
```

If that file already exists and a new login is needed, first run `./bin/catalogctl auth logout --file "$HOME/.catalog-login/langgraph.json"`. Do not overwrite or edit it. Check the UI's **My access** for the expected human subject, tenant and applicable read permission.

Validate the selected login's API access:

```sh
unset CATALOG_REMOTE_TOKEN CATALOG_REMOTE_TOKEN_FILE
./bin/catalogctl remote capabilities \
  --base-url https://catalog-a.test:8443 \
  --login-file "$HOME/.catalog-login/langgraph.json" \
  --issuer-ca-file "$CATALOG_TUTORIAL_DIRECTORY/trust/ca.pem" \
  --ca-file "$CATALOG_TUTORIAL_DIRECTORY/trust/ca.pem"
```

Capabilities describe server features, not the identity document. The login command verifies the human through `/api/v1/me`; **My access** displays current identity and grants. A token scope is only an additional ceiling.

Select the same API and model inputs, and set the question to the local Agent's known listing text:

```sh
export CATALOG_SDK_ORIGIN=https://catalog-a.test:8443
export CATALOG_SDK_CA_FILE="$CATALOG_TUTORIAL_DIRECTORY/trust/ca.pem"
unset CATALOG_SDK_TOKEN LANGGRAPH_ALLOW_LOOPBACK_HTTP LANGGRAPH_NAMESPACE_ID
export LANGGRAPH_MODEL_ORIGIN=http://127.0.0.1:11434
export LANGGRAPH_MODEL=qwen2.5:7b
export LANGGRAPH_QUESTION='Find one Agent using the search text federation tutorial and kind agent, inspect its local release, then explain your choice.'
python3 - <<'PY'
import datetime
import json
import os
from pathlib import Path

credential_path = Path.home() / ".catalog-login" / "langgraph.json"
if not os.environ.get("LANGGRAPH_QUESTION"):
    raise SystemExit("Set LANGGRAPH_QUESTION to the question for this run.")
with credential_path.open() as stream:
    credential = json.load(stream)
if credential["apiOrigin"] != os.environ["CATALOG_SDK_ORIGIN"]:
    raise SystemExit("The login belongs to a different Catalog.")
expiry = datetime.datetime.fromisoformat(credential["expiresAt"].replace("Z", "+00:00"))
if expiry <= datetime.datetime.now(datetime.timezone.utc):
    raise SystemExit("Sign in again before running the assistant.")
environment = dict(os.environ, CATALOG_SDK_TOKEN=credential["accessToken"])
os.execve(
    "examples/langgraph/.venv/bin/python",
    ["examples/langgraph/.venv/bin/python", "-B", "examples/langgraph/agent.py"],
    environment,
)
PY
```

The tutorial's expected match is **Federation tutorial Agent**, version `1.0.0`, identifier `urn:air:tutorial.catalog.example:agents:federation-demo`. For another Catalog, use its known local listing text. This explicitly reads the current private CLI JSON file created and validated above. It is not automatic SDK credential discovery or a public token-export command. Keep the directory/file owned and private, avoid shell tracing and never print the file or token. The token enters only this child process's environment.

The assistant does not refresh that token. A supported `remote --login-file` read can refresh it when configured; then start a new assistant run. If fresh login is required, log out the selected file before signing in again. Certificate and hostname verification remain enabled for the API.

## Understand the tools and limits

| Tool | Allowed behavior |
| --- | --- |
| `search_catalog` | Search local releases by text and optional resource kinds. The configured namespace applies to every call. |
| `read_catalog_record` | Read the exact local release UUID selected from this run's Search results. |

Search uses ten results per page and follows at most two pages per tool call. Continuation retains the same query, kinds, namespace, page size and identity. An empty page can still have another cursor. The assistant cannot enumerate the Catalog without bounds.

| Limit | Value |
| --- | --- |
| User question | 2,000 UTF-8 bytes |
| Tool search text | 500 UTF-8 bytes |
| Catalog requests | At most 16 per run |
| Catalog request deadline | 15 seconds |
| Catalog response | At most 1 MiB |
| Graph steps | At most 14 |
| Agent workflow | 300 seconds by default; `--timeout` permits a positive value through 600 seconds |
| Recommendations | At most five inspected local releases |

To select a different deadline, run the same program directly:

```sh
examples/langgraph/.venv/bin/python -B examples/langgraph/agent.py --timeout 300
```

The program still reads the question and credentials from the same environment. A longer deadline does not increase page, request or response budgets. CPU inference speed depends on the model and host. Each model HTTP operation also has a 90-second timeout. The bounded model preflight runs before the agent workflow.

The program sends a bounded metadata selection to the model, not complete native documents, private extension maps, credentials or response bodies. Documentation and Git links must be credential-free HTTPS. Links are displayed; the program does not fetch them.

## Permissions, privacy and failures

Every Catalog request uses current backend authorization. An exact record can disappear after Search because access or publication changed. The example stops on authentication, authorization, dependency or record-read failure. It does not use administrator credentials as a fallback, select another release silently or retry requests automatically.

The process disables LangSmith tracing and legacy LangChain tracing. Questions and selected metadata still enter the local model and this process's memory. Use synthetic data until your policy permits that disclosure. The example adds no checkpointer, conversation database, retained transcript or hosted telemetry. You remain responsible for the model service's access controls, logs and cache.

Descriptions are untrusted data. They do not change tools, destinations or credentials. Restricting tools and checking release identities prevents several unsafe actions, but does not make a language model's explanation correct.

Failures print a fixed diagnostic to stderr and return a nonzero exit status. They do not print private API bodies or raw dependency errors.

| Diagnostic | What to check |
| --- | --- |
| `question_required` or `invalid_input` | Supply a nonempty question within the byte limit and a finite deadline. |
| `invalid_configuration` | Supply the Catalog origin, token and valid optional UUID/settings. |
| `local_model_origin_required` or `local_model_required` | Use an explicitly local Ollama origin and local model. |
| `local_model_unavailable` or `tool_calling_model_required` | Start the selected local service, download its model and check declared tool support. |
| `local_model_identity_changed` | The qualification model differs from the checked-in digest. Restore the selected model or review an explicit pin update. |
| `catalog_unauthenticated` | Renew the token and check issuer, audience and active membership. |
| `catalog_forbidden` | Check current exact-scope permission and the OAuth ceiling. |
| `catalog_record_unavailable` | Start a new discovery request after checking current access and release state. |
| `catalog_unavailable` or `catalog_<SDK code>` | Check API reachability, response bounds and safe SDK diagnostics. |
| `model_did_not_search` | Use a tool-capable local model and a clear discovery question. No recommendation was accepted. |
| `model_recommended_unread_resource` | The model named a release it did not inspect. No recommendation was accepted. |
| `model_turn_limit_exceeded` or `request_budget_exhausted` | Narrow the question rather than removing the work bounds. |
| `deadline_exceeded` | Check API/model availability and local inference resources. Increase the finite deadline only when appropriate. |
| `agent_failed` | The model or graph failed, or its final answer did not match the required JSON shape. |

For no useful matches, repeat the known term directly with `remote search --origin local`. Check the selected Catalog, released version, namespace and Reader grants. Federation observations are outside this assistant's search. For a slow or unavailable model, check the literal loopback port, running process/container, downloaded weights and RAM before changing a deadline. Keep hosted inference disabled and preserve the request/graph budgets.

## Stop only your local model

For the foreground `ollama serve` command, use Ctrl-C in that terminal. For the container alternative, run `docker stop catalog-langgraph-model` for the container you started. Do not stop an unrelated model or the retained Catalog.

The Python environment and downloaded weights remain on disk. Catalog records remain governed by the separately selected sample/OIDC setup and its cleanup instructions. Signing out with `catalogctl auth logout --file "$HOME/.catalog-login/langgraph.json"` removes that local credential file; it does not revoke provider sessions or delete Catalog data.

## Verify the example

| Command | What it proves |
| --- | --- |
| `make test-langgraph-unit` | Isolated tool/graph behavior, input and output checks, bounds, cancellation and safe diagnostics. Uses in-memory test doubles, with no listeners or model calls. |
| `make test-langgraph` | Units plus the actual SDK and graph against an owned local Catalog HTTP fixture. A scripted model chooses known tool calls; this is not real-model qualification. |
| `make test-langgraph-model LANGGRAPH_MODEL_ORIGIN=http://127.0.0.1:11434` | Adds a real local model to the owned Catalog fixture. The model must Search, inspect and recommend actual fixture releases. |

The functional fixture creates its own Catalog and synthetic identities. It does not query or change your retained Compose Catalog. It verifies continuation across more than one page, exact readback, cross-tenant isolation and refusal of invalid credentials. Its process and temporary directory must clean up successfully.

The model target requires an explicitly selected running local service and downloaded model. It checks the `qwen2.5:7b` digest recorded in `test/langgraph/model.json`; the interactive example accepts other local tool-capable models. The same file records the selected Ollama image for reproducible setup. The target does not start Ollama or download weights.

You own model-service shutdown and its cache. A scripted-model pass does not establish real-model behavior; require the separate real-model target when you change the model or agent instructions.

Unit coverage belongs to this optional example. It is not backend Go coverage or a measure of model quality. Reports and dependency inventories stay under ignored `output/langgraph/`. Use the [testing guide](../../documentation/src/content/docs/contributing/testing.md#optional-langgraph-discovery-example) for the broader verification strategy.
