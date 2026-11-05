# Local OIDC and federation tutorial inputs

Use `make test-local-auth` to qualify the single-Catalog password setup with the existing packaged UI runner. It tests actual account and permission changes, restart, CLI login and certificate renewal. The test requires the documented local images, ports and subnet. It removes its own deployment and private inputs. Reports stay under ignored `output/playwright/local-passwords/`.

These example assets support the manual's [interactive Keycloak tutorial](../../../documentation/src/content/docs/operations/oidc-keycloak.md#run-the-interactive-local-tutorial) and [two-Catalog federation tutorial](../../../documentation/src/content/docs/guides/two-catalog-federation.mdx). Follow those procedures for complete account, migration, publication, synchronization and client steps. This directory contains no real credentials and is not a production deployment or application dependency.

| Asset | Purpose |
| --- | --- |
| [prepare.py](prepare.py) | Exclusively create an owned `0700` directory outside the checkout, with production-derived A/B profiles, separate random credentials/keys, TLS certificates, database initialization and pinned-client realm inputs. |
| [local_auth.py](local_auth.py) | Start, retain, stop and renew one local password deployment. See [local password setup](../../../documentation/src/content/docs/operations/oidc-keycloak.md#start-one-local-catalog-with-passwords). |
| [compose.py](compose.py) | Validate the selected generated metadata and invoke Compose with an explicit project name, env file and Compose file. Prepared interpolation inputs replace conflicting ambient values; forwarded selector overrides are refused. |
| [compose.yaml](compose.yaml) | Start, inspect and stop only the generated Compose project. Its databases and issuer retain their own named volumes. No database port is published. |
| [tls-frontend.go](tls-frontend.go), [Containerfile](Containerfile) | Local fixed TLS ingress for A/B API and UI origins, using the backend's pinned Go toolchain and no third-party module. Preserve exact Host; do not trust forwarding headers. |
| [credential-files.py](credential-files.py) | Convert one current owned private human CLI login into an HTTP header file and optional bare token. Check origin/expiry and refuse source-file replacement. |
| [service-token.py](service-token.py) | Acquire a service token from the exact local issuer using a private client-secret file and verified TLS; write protected token/header/claim files. |
| [service-read.py](service-read.py) | Use the installed Python SDK to verify the actual service subject and tenant-wide `resource.read` permission, then read local metadata. |
| [test_unit_tutorial_credentials.py](../../../test/packaging/test_unit_tutorial_credentials.py) | Isolated credential-file regressions; no provider, process or network is started. |
| [test_unit_tutorial_compose.py](../../../test/packaging/test_unit_tutorial_compose.py), [test_functional_tutorial_compose.py](../../../test/packaging/test_functional_tutorial_compose.py) | Isolated metadata/argument checks and separate native launcher, daemon-free Compose rendering and partial/fresh OpenSSL preparation regressions. |

`make sdk-check SDK_LANGUAGE=python` runs `service-read.py` itself with the installed wheel and sdist packages. Owned HTTPS tests cover identity and permission refusals, namespace-only grants, scope denial, malformed inputs and untrusted CA certificates. The script rejects empty tokens and malformed subject inputs before connecting. It prints fixed diagnostics without private values. These tests use no tutorial Compose stack, issuer or retained Catalog data. See the [service-client procedure](../../../documentation/src/content/docs/operations/oidc-keycloak.md#verify-cli-sdk-and-refusal) for setup and exit behavior.

From the repository root, prepare a new private location after building the images as described in the manual. Use an absolute path outside the checkout. Its resolved path accepts ASCII letters, digits, slash, dot, underscore and hyphen. Spaces, non-ASCII characters and other shell metacharacters are refused:

```sh
export CATALOG_TUTORIAL_DIRECTORY="$HOME/.catalog-tutorials/passwords-01"
python3 examples/tutorials/local-oidc/prepare.py \
  --directory "$CATALOG_TUTORIAL_DIRECTORY" \
  --backend-image open-agentic-catalog:docs-tutorial \
  --ui-image open-agentic-catalog-ui:docs-tutorial
python3 examples/tutorials/local-oidc/compose.py \
  --directory "$CATALOG_TUTORIAL_DIRECTORY" --show-project
python3 examples/tutorials/local-oidc/compose.py \
  --directory "$CATALOG_TUTORIAL_DIRECTORY" build frontend
python3 examples/tutorials/local-oidc/compose.py \
  --directory "$CATALOG_TUTORIAL_DIRECTORY" up -d postgres-a postgres-b identity frontend
```

Continue only after preparation prints `Prepared` and exits successfully. It can leave partial inputs after an OpenSSL or filesystem failure, even when `compose.env` exists. Correct the prerequisite and choose a fresh private destination. Inspect or remove an abandoned partial directory only after verifying its ownership and that no environment uses it. Follow the manual's [preparation recovery](../../../documentation/src/content/docs/operations/oidc-keycloak.md#recover-from-failed-preparation); preparation never resumes or overwrites existing inputs.

`compose.py` owns project selection for all tutorial commands. Its equivalent is `docker compose --project-name GENERATED_PROJECT --env-file PREPARED_DIRECTORY/compose.env --file examples/tutorials/local-oidc/compose.yaml COMMAND [ARGS...]`. It reads the selected owned `0700` directory's generated `0600` metadata without shell evaluation, checks its project/directory/source binding and private network, and supplies all prepared interpolation values to Docker. A conflicting `COMPOSE_PROJECT_NAME` or `CATALOG_TUTORIAL_*` value cannot redirect that child command. Missing or malformed metadata is refused before Docker; `--env-file` alone would permit an ambient project override. Forwarded project/env-file/Compose-file/directory selectors are refused, including `-p`/`-f` and attached forms. The manual's `tutorial_compose` convenience function calls this same launcher.

Preparation supplies both Catalogs before either runtime starts. A uses tenant ending `0001`, audience `open-agentic-catalog` and approved publisher-domain input `tutorial.catalog.example`. B uses tenant ending `0002`, audience `open-agentic-catalog-b`, publisher-domain input `tutorial-b.catalog.example`, and the exact `tutorial-a` outbound profile. B's mounted `b/source-credentials` directory starts empty. Acquire A's source token and deliver a complete origin-bound generation before any source request.

The default private subnet is `172.29.245.0/24`, with the frontend at `172.29.245.10`. Select a different free private subnet/address through `--subnet` and `--frontend-address` if necessary. Host clients resolve the `.test` names to loopback; Compose resolves them privately. The API frontend uses ports `8443`/`8445`, the UIs `9443`/`9445`, and Keycloak `8447`. The generated CA is `trust/ca.pem`. Each client/consumer selects its trust explicitly; no verification bypass is required.

The local CA and server certificates expire after seven days. Follow [certificate recovery](../../../documentation/src/content/docs/operations/oidc-keycloak.md#replace-expired-tutorial-certificates) to prepare a new private directory/project. `stop` retains the old network allocation; project-pinned `down` removes its containers/network and retains named volumes. Alternatively, keep it stopped and choose another free subnet/address for the new project. The fresh project does not migrate the old database or issuer state. Replace the trusted browser CA and generate fresh account/login/source credentials through the manual. Do not overwrite existing inputs, reuse old credentials or bypass verification.

The initializer creates schema owner, API and worker logins only on a fresh database volume. It never runs Catalog migrations. Invoke the owner service explicitly before starting the API/worker. Password or SQL edits do not reinitialize an existing volume, and importing the realm at startup does not overwrite existing Keycloak accounts or client secrets.

Human login files live at `a/{admin,author,reviewer,reader}-login.json` and `b/{admin,reader}-login.json`. Derived headers/tokens use the corresponding sibling stem, such as `a/author.headers` and `a/author.token`. They are private tutorial inputs, not SDK automatic credential discovery. The selected human/source tokens last 30 minutes; automation tokens last five minutes. Renew credentials explicitly and regenerate derived files after renewal.

The two public VS Code clients register the fixed native scheme/authority with `/oidc/*` for Keycloak's window-routing query support. The extension checks its exact native callback path and pending login state. Follow [desktop callback setup](../../../documentation/src/content/docs/operations/oidc-keycloak.md#connect-the-desktop-client) when updating an existing realm; startup import does not apply that change. UI callbacks remain exact HTTPS URLs.

Inspect status with `python3 examples/tutorials/local-oidc/compose.py --directory "$CATALOG_TUTORIAL_DIRECTORY" ps`. With the same launcher/directory, `stop` preserves containers, network, volumes and private inputs. Follow the manual's [restart procedure](../../../documentation/src/content/docs/operations/oidc-keycloak.md#stop-or-remove-only-this-environment): start dependencies, wait for healthy databases and verified issuer discovery, then start the Catalog services. Starting everything together can leave the API exited before the issuer is ready. Do not rerun preparation or initialization.

Before deliberate disposal, run `--show-project` and `ps -a`, confirm the selected generated name, then use `down --volumes --remove-orphans`. Remove its private files, exact hosts entry and imported browser CA separately. Do not reset the retained developer stack, remove unrelated volumes or prune Docker globally.

The existing `make test-tooling-unit` selection includes these isolated helpers. To run only their regressions from the repository root:

```sh
python3 -B -m unittest discover -s test/packaging -p 'test_unit_tutorial_*.py'
```

Native process/tool cases remain in the packaging functional selection. Run only these cases with:

```sh
python3 -B -m unittest discover -s test/packaging -p 'test_functional_tutorial_compose.py'
```

The actual Compose case requires an installed Compose CLI and runs only `config --format json`, with no reachable daemon. It checks clean, conflicting and explicit project selection, all prepared interpolation inputs, and unchanged files. Native preparation cases use owned temporary directories and OpenSSL; missing Compose/OpenSSL prerequisites produce explicit skips. They do not start containers or qualify password login, database authority, source synchronization or installed editor behavior. The manual's complete procedure requires its separate local execution. Source assets use [Apache License 2.0](../../../LICENSE). The local TLS image includes that license and the Go standard-library license under `/licenses/`.

`make local-auth-up` and `make local-auth-down` select one Catalog through `local_auth.py`. Its generated `compose.single.json` excludes B services, credentials, clients and listeners. The original `compose.py` launcher remains for the two-Catalog tutorial. Do not use it to manage the single-Catalog setup.

`test_unit_local_auth.py` checks topology decisions without processes. `test_functional_local_auth.py` checks actual private preparation, Compose rendering, unchanged restart inputs, TLS renewal and loopback port checks without starting containers. Real login and restart checks require a separately owned local deployment.

For the prepared accounts, use the [Reader and additional-user procedure](../../../documentation/src/content/docs/operations/oidc-keycloak.md#grant-reader-access-and-add-another-person). The same guide explains password changes, resets, membership suspension and separate Catalog/provider sign-out. Use [production authentication settings](../../../documentation/src/content/docs/operations/sign-in.md#replace-development-settings-for-production) when replacing development authentication.

Single-Catalog preparation retains Keycloak's standard account-console scopes. The two imported users receive `default-roles-catalog` for password self-service. Catalog UI and CLI clients keep only `basic` in their default scopes. These provider roles grant no Catalog permissions.
