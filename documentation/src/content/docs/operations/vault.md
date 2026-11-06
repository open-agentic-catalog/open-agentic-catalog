---
title: Optional Vault secret delivery
description: Deliver Catalog credentials through Vault and VSO, verify activation, and maintain or remove the integration.
---

Vault is optional. A normal Catalog installation uses Kubernetes Secrets directly. It needs no Vault server, Vault Secrets Operator (VSO), custom resource definitions (CRDs) or Catalog feature flag.

Use this guide when your Kubernetes deployment already uses Vault, or when you want to test that arrangement locally. VSO copies selected static Vault values into Kubernetes Secrets. Catalog continues to use its existing Secret references.

| Your setup | Start here |
| --- | --- |
| Test the complete integration on your workstation | [Run the isolated local lab](#run-the-isolated-local-lab). |
| Connect an existing Kubernetes deployment to Vault | [Prepare an operator-managed Vault](#prepare-an-operator-managed-vault). |
| Deploy without Vault | Follow [Kubernetes deployment](deployment.mdx) and [database roles](database-roles.mdx). |

## How delivery works

Vault's version 2 key/value engine (KV-v2) holds static credentials for existing PostgreSQL roles. VSO writes the selected values to Kubernetes Secrets. Catalog reads those Secrets. The example does not create dynamic database users or add a Vault client to Catalog.

| Vault path under the `catalog` mount | Destination Secret | Data consumed by Catalog |
| --- | --- | --- |
| `catalog-api-database` | `catalog-api-database` | `dsn`, for the API. |
| `catalog-worker-database` | `catalog-worker-database` | `dsn`, for the worker. |
| `catalog-migration-database` | `catalog-migration-database` | `dsn`, for the direct schema owner. |
| `catalog-cursor-signing` | `catalog-cursor-signing` | `key`, the startup cursor key. |
| `catalog-vault-source` | `catalog-vault-source` | Optional `bearer.json` metadata and `bearer.token`, for one source binding. |

Each destination also contains `vault-kv-version`. This marker helps an operator check delivery; Catalog does not consume it. Source metadata uses the KV version in its credential revision. The templates exclude raw provider data.

Catalog API, worker and migration Pods remain tokenless. VSO obtains Kubernetes tokens through dedicated synchronization ServiceAccounts. The owner policy reads only the migration DSN. The runtime policy reads the selected runtime paths.

The selected upstream VSO controller has cluster-wide Secret access, TokenRequest authority and workload patch permissions. A namespace-scoped VaultAuth does not limit that controller authority. Review VSO's Kubernetes RBAC separately from each role's Vault policy.

## Run the isolated local lab

Use a Linux AMD64 host with the [maintained Minikube prerequisites](deployment.mdx#maintained-local-minikube-verification). The scenario requires eight CPUs and 12 GiB of available memory. It uses enforcing Calico policies and private TLS for Vault, PostgreSQL and the source fixture.

Run these commands separately from the repository root:

```sh
make vault-tools-install
make test-minikube-vault
make test-minikube-vault-cleanup
```

The installer downloads reviewed external test inputs. It does not start a cluster. Normal checks do not install these prerequisites implicitly.

Use `MINIKUBE=/path/to/minikube` when the executable is outside PATH. Supply `MINIKUBE_PRELOAD_ARCHIVE=/path/to/archive.tar.lz4` when using the documented offline preload. These overrides apply to both test targets.

The selected inputs are Vault 2.2.1, Vault Helm chart 0.34.1, VSO 1.6.2 and kube-rbac-proxy v0.23.1. The lab verifies chart hashes, source identities and Linux AMD64 image digests. The selected images have recorded advisory findings. A passing local test does not establish production image security.

`test-minikube-vault` owns one private cluster. It initializes and unseals a single persistent Vault Raft member. It installs VSO separately, applies the downloadable examples and waits for complete secret delivery before Catalog migration or startup.

The test then verifies these operations:

- Authenticate all three PostgreSQL roles through fresh TLS connections that require passwords.
- Inspect and synchronize an authenticated source through the API and worker.
- Rotate source credentials without restarting Catalog.
- Drain Catalog, rotate database passwords and restore the deployment through coordinated maintenance.
- Reject retired credentials, wrong Vault identities and conflicting destination ownership.
- Recover from controller outage, Vault sealing, Vault Pod replacement and a source authentication failure.
- Preserve governed content, complete snapshots and idempotent command results.
- Remove synchronization resources, revoke owned access and verify cleanup.

`test-minikube-vault-cleanup` runs two smaller, separate clusters. One injects a failure after all five inputs synchronize. The other sends the runner a real SIGTERM at that point. Both require cleanup with an independent deadline. Their reports identify `cleanupTest`; they do not claim the full Catalog workflow ran.

Each target removes only its owned profile, storage, image aliases and private inputs. It preserves retained Compose data and other clusters. The tools cache remains available for another run. Ordinary checks and deployments remain independent of that cache.

Only this disposable lab initializes or unseals a test server automatically. Do not use its one-member layout, bootstrap token or unseal custody as a production configuration.

## Prepare an operator-managed Vault

Complete the [Kubernetes deployment prerequisites](deployment.mdx) through namespace, database-role and identity-provider setup. Keep Catalog stopped until the required Secrets are ready. Use a dedicated Vault KV mount and reviewed role/policy names for this example.

You need:

- An initialized Vault server and authorized Vault CLI session.
- A verified HTTPS route from VSO to Vault.
- A configured Kubernetes authentication mount and TokenReview access.
- A separately installed VSO controller and its CRDs.
- Protected files containing the three existing DSNs and the cursor key.
- Your complete Catalog values, image selection, trust and OIDC configuration.

Do not initialize an existing Vault. Do not overwrite another deployment's mount, policy or role. For multiple Catalog deployments, allocate distinct paths and role names.

Run the operator commands in Bash. Set explicit deployment inputs:

```sh
OAC_CONTEXT=my-cluster
OAC_NAMESPACE=catalog
OAC_RELEASE=catalog
OAC_PRIVATE="$HOME/.config/open-agentic-catalog/vault-example"
umask 077
mkdir -p "$OAC_PRIVATE"
chmod 700 "$OAC_PRIVATE"
```

Place the reviewed public Vault CA at `$OAC_PRIVATE/vault-ca.pem`. Set the HTTPS endpoint reachable by your operator shell:

```sh
export VAULT_ADDR=https://vault.operator.example
export VAULT_CACERT="$OAC_PRIVATE/vault-ca.pem"
```

Replace the example address. Use your existing protected Vault login. Keep tokens out of command arguments, shell tracing, Git and reports. The operator address can differ from the address reachable inside Kubernetes.

### Install VSO separately when needed

Skip installation when your operator already provides a compatible VSO controller. Review its authority and selected version before adding Catalog synchronization resources.

For a separate installation, acquire the reviewed charts with `make vault-tools-install`. Create the provider namespace explicitly. Use these values with the cached VSO chart:

```yaml
controller:
  replicas: 1
  manager:
    image:
      repository: hashicorp/vault-secrets-operator
      tag: "1.6.2@sha256:0e53c94cd2571a535d9aa36657f807c2f683ae9271e58cad203e1dc4318b7f54"
    clientCache:
      persistenceModel: none
    globalTransformationOptions:
      excludeRaw: true
    globalVaultAuthOptions:
      allowDefaultGlobals: false
    extraArgs: ["--global-vault-auth-options="]
  kubeRbacProxy:
    image:
      repository: quay.io/brancz/kube-rbac-proxy
      tag: "v0.23.1@sha256:bb7c6fbfbb00cc2892e97fd102b8f01eb19eef09ce216385a7cf68ea7a9e7fac"
defaultVaultConnection:
  enabled: false
defaultAuthMethod:
  enabled: false
csi:
  enabled: false
tests:
  enabled: false
telemetry:
  serviceMonitor:
    enabled: false
```

These image digests select Linux AMD64. Select and review the appropriate platform images for another architecture. The explicit empty global-auth option prevents fallback to the controller's default global-auth setting.

Save the values as `$OAC_PRIVATE/vso-values.yaml`. Install them into a separate provider namespace:

```sh
kubectl --context "$OAC_CONTEXT" create namespace vault-system
helm upgrade --install catalog-vso \
  .tools/minikube-vault/vault-secrets-operator-1.6.2.tgz \
  --kube-context "$OAC_CONTEXT" --namespace vault-system \
  --values "$OAC_PRIVATE/vso-values.yaml" --wait --timeout 5m
```

The Catalog release does not own this controller, its CRDs or its cluster RBAC. Follow the [VSO installation guidance](https://developer.hashicorp.com/vault/docs/deploy/kubernetes/vso/helm) for a shared installation.

### Configure Vault authentication and policies

Use an existing Kubernetes authentication mount when it is already configured. For a new dedicated setup, enable a `kubernetes` authentication mount and a KV-v2 mount named `catalog`:

```sh
vault auth enable -path=kubernetes kubernetes
vault secrets enable -path=catalog -version=2 kv
```

For an in-cluster Vault server with TokenReview permission, configure the cluster API:

```sh
vault write auth/kubernetes/config \
  kubernetes_host=https://kubernetes.default.svc:443
```

The tested Vault Helm setup enables `server.authDelegator`. Vault uses its projected ServiceAccount token and cluster CA for TokenReview. A server outside the cluster needs a reachable API endpoint and separately configured reviewer credentials/trust. Follow [Vault Kubernetes authentication](https://developer.hashicorp.com/vault/docs/auth/kubernetes) for that deployment; the in-cluster command is not sufficient.

The examples use these complete, read-only policies:

- [Runtime policy](../../../../../test/minikube/fixtures/vault/runtime-policy.hcl).
- [Owner policy](../../../../../test/minikube/fixtures/vault/owner-policy.hcl).

Apply the policies from the repository checkout:

```sh
vault policy write catalog-runtime test/minikube/fixtures/vault/runtime-policy.hcl
vault policy write catalog-owner test/minikube/fixtures/vault/owner-policy.hcl
for role in runtime owner; do
  vault write "auth/kubernetes/role/catalog-$role" \
    bound_service_account_names="catalog-vault-$role" \
    bound_service_account_namespaces="$OAC_NAMESPACE" \
    audience=vault token_policies="catalog-$role" \
    token_ttl=5m token_max_ttl=10m
done
```

Bind the exact namespace and ServiceAccount names. Do not replace them with wildcards. The synchronization ServiceAccounts differ from the Catalog workload accounts.

### Store complete input generations

Prepare `api.dsn`, `worker.dsn`, `owner.dsn` and `cursor.key` in `$OAC_PRIVATE`. Each DSN must use the existing exact role and PostgreSQL writer endpoint. Use verified TLS and the mounted CA path `/etc/open-agentic-catalog/database/ca.pem`.

Use a currently accepted password. The owner DSN connects directly to PostgreSQL. Preserve the [cursor-key format](../reference/configuration.mdx#runtime-variables). Do not generate a replacement cursor key when restoring an existing deployment unless that change is intentional.

Load the protected files without putting their contents in arguments:

```sh
vault kv put -mount=catalog -cas=0 catalog-api-database \
  dsn=@"$OAC_PRIVATE/api.dsn"
vault kv put -mount=catalog -cas=0 catalog-worker-database \
  dsn=@"$OAC_PRIVATE/worker.dsn"
vault kv put -mount=catalog -cas=0 catalog-migration-database \
  dsn=@"$OAC_PRIVATE/owner.dsn"
vault kv put -mount=catalog -cas=0 catalog-cursor-signing \
  key=@"$OAC_PRIVATE/cursor.key"
```

The compare-and-set (CAS) version prevents overwriting a concurrent update. `-cas=0` requires a new path. For later replacement, use the observed current version as the CAS value. Stop after an uncertain write result; inspect its version before another mutation.

For an authenticated source, prepare one protected JSON file with `token`, `tenantId`, `origin` and `epoch`. Use the exact tenant UUID and credential-free HTTPS origin. Use a unique deployment identifier as `epoch`. Keep it unchanged while increasing that path's KV version. Start a new epoch if you replace the path and reset its versions.

Use this structure for `$OAC_PRIVATE/source-generation.json`. Replace the token, tenant and origin for your source:

```json
{
  "token": "REPLACE_WITH_ACCEPTED_SOURCE_TOKEN",
  "tenantId": "00000000-0000-7000-8000-000000000001",
  "origin": "https://directory.example",
  "epoch": "catalog-source-a"
}
```

```sh
vault kv put -mount=catalog -cas=0 catalog-vault-source \
  @"$OAC_PRIVATE/source-generation.json"
```

The templates produce binding `bearer`, metadata file `bearer.json` and leaf `bearer.token`. The revision becomes `<epoch>-kv-<version>`. They retain the exact tenant/origin binding. Review the [source credential contract](../reference/configuration.mdx#federated-source-credential-bindings) before enabling the resolver.

If you do not use private source bindings, omit this input and remove its VaultStaticSecret from the example. Remove the unused source path from the runtime policy too. Source authentication remains optional.

## Synchronize Kubernetes Secrets

Download the [complete synchronization YAML](../../../../../test/minikube/fixtures/vault/catalog-sync.yaml), or copy it from the checkout. It contains one VaultConnection, two tokenless ServiceAccounts, two VaultAuth resources and five VaultStaticSecrets.

```sh
cp test/minikube/fixtures/vault/catalog-sync.yaml \
  "$OAC_PRIVATE/catalog-sync.yaml"
kubectl --context "$OAC_CONTEXT" --namespace "$OAC_NAMESPACE" \
  create secret generic catalog-vault-ca \
  --from-file=ca.crt="$OAC_PRIVATE/vault-ca.pem"
```

Edit the copied VaultConnection address for the HTTPS endpoint reachable by VSO. Its default is `https://vault.vault.svc:8200`. Keep `skipTLSVerify: false` and `caCertSecretRef: catalog-vault-ca`.

Apply the external resources to the selected Catalog namespace:

```sh
kubectl --context "$OAC_CONTEXT" --namespace "$OAC_NAMESPACE" \
  apply --server-side -f "$OAC_PRIVATE/catalog-sync.yaml"
for name in catalog-api-database catalog-worker-database \
  catalog-migration-database catalog-cursor-signing; do
  kubectl --context "$OAC_CONTEXT" --namespace "$OAC_NAMESPACE" \
    wait "vaultstaticsecret/$name" --for=condition=SecretSynced --timeout=2m
done
```

For the optional source, repeat the wait for `vaultstaticsecret/catalog-vault-source`.

The wait confirms a sync condition, but it is not a complete startup gate. Check every selected resource before deployment:

| Check | Required observation |
| --- | --- |
| Current controller result | `SecretSynced=True`, reason `Synced`, and `observedGeneration` equal to the resource's current generation. |
| Provider version | The intended Vault KV version agrees with `vault-kv-version` in the destination. |
| Ownership | The Secret references the exact VaultStaticSecret UID, API version, kind and name. |
| Complete data | The selected keys and protected values match the complete provider generation. No unexpected raw provider fields appear. |
| Consumer acceptance | A fresh database connection or actual source operation uses the new credentials successfully. |

Inspect public synchronization state without displaying Secret data:

```sh
kubectl --context "$OAC_CONTEXT" --namespace "$OAC_NAMESPACE" \
  get vaultstaticsecret -o json
kubectl --context "$OAC_CONTEXT" --namespace "$OAC_NAMESPACE" \
  get events --field-selector reason=SecretSyncError
```

Compare values only through protected operator tooling or files. Do not print passwords, DSNs, tokens or low-entropy credential hashes. A successful condition can remain after a destination-write failure. Warning events, ownership and complete data checks remain necessary.

The example uses `destination.create: true` and `overwrite: false`. It refuses to adopt an unrelated Secret. Resolve ownership deliberately; do not enable overwrite to bypass that refusal.

## Start and verify Catalog

Merge these references into your complete Catalog values:

```yaml
databaseTrustBundleConfigMap: catalog-database-ca
secrets:
  apiDatabase: {name: catalog-api-database, key: dsn}
  workerDatabase: {name: catalog-worker-database, key: dsn}
  migrationDatabase: {name: catalog-migration-database, key: dsn}
  cursorSigning: {name: catalog-cursor-signing, key: key}
```

Create `catalog-database-ca` with the public PostgreSQL CA under key `ca.pem`. Keep image, issuer, tenant mapping, outbound policy and other required values from your deployment. This fragment is not complete deployment values.

For private sources, also select `secrets.federationCredentials: {name: catalog-vault-source}`. Enable the resolver and configure its approved origin, network profile and trust through the [source setup](../reference/configuration.mdx#federated-source-credential-bindings). The Secret alone enables no network destination or Catalog feature.

Run the [coordinated deployment](maintenance.mdx#coordinated-release-deployment) with the complete values:

```sh
make helm-deploy-check HELM_CONTEXT="$OAC_CONTEXT" \
  HELM_NAMESPACE="$OAC_NAMESPACE" HELM_RELEASE="$OAC_RELEASE" \
  HELM_VALUES="$OAC_PRIVATE/catalog-values.yaml"
make helm-deploy HELM_CONTEXT="$OAC_CONTEXT" \
  HELM_NAMESPACE="$OAC_NAMESPACE" HELM_RELEASE="$OAC_RELEASE" \
  HELM_VALUES="$OAC_PRIVATE/catalog-values.yaml" HELM_DEPLOY_TIMEOUT=20m
```

The coordinator drains old API and worker Pods before the owner migration hook. It restores the replica counts from the selected target values. It does not inspect Secret contents, contact Vault or prove that PostgreSQL accepts a password.

Verify fresh TLS connections for each exact database role. Check API readiness, worker readiness, an authorized Catalog read and a completed worker operation. Existing pooled connections cannot prove new-password acceptance. Source test and synchronization must report the intended credential revision.

### Other optional secret inputs

Use the same external delivery pattern only when you enable the relevant capability. Keep its established format and activation procedure:

| Consumer | Existing input and required handling |
| --- | --- |
| UI sessions and OIDC client | `ui.existingSecret` contains `keys.json` and, for OIDC, `client-secret`. Follow [staged session-key rotation](maintenance.mdx#rotate-ui-session-keys). Restart UI replicas after startup-input changes. |
| Webhook encryption | `secrets.webhookEncryption` references the deployment encryption key. Follow [stopped-runtime replacement and rewrapping](webhooks.mdx#deployment-encryption-key-replacement). Preserve keys required by older backups. |
| Trace export | `secrets.tracingInputs` supplies the configured authentication and TLS files. Follow [trace credential activation](observability.mdx#verify-and-troubleshoot-tracing). Restart the affected API/worker consumers. |

The lab does not run these three additional rotation campaigns. It does not introduce automatic rollout triggers for them.

## Rotate credentials

### Replace PostgreSQL passwords

Follow the [database maintenance sequence](maintenance.mdx#rotate-credentials-and-trust). PostgreSQL has one accepted password for each exact role; Vault KV rollback does not restore that password.

1. Pause external reconciliation and autoscaling for the selected deployment.
2. Drain the API, then the worker.
3. Wait for all old runtime Pods and sessions to disappear.
4. Change the three exact passwords through your database administration procedure.
5. Write complete successor DSNs with the expected KV CAS versions.
6. Verify every selected destination before coordinated deployment.
7. Run owner migration and restore the target replicas.
8. Reject the retired passwords through fresh TLS connections.
9. Verify successor connections, Catalog reads and worker progress.

After a partial replacement, keep runtimes stopped. Correct the remaining provider inputs and destinations forward. Do not restore old KV bytes and assume the database accepts them. Do not repeat an uncertain password change blindly.

### Replace a source bearer credential

Arrange source acceptance before switching the binding when the provider supports overlap. Write one complete successor generation with a new KV version. VSO projects metadata and leaf data together.

Check the mounted revision, a real Source test and the worker's complete snapshot. API and worker do not need restarts for these per-request bindings. Retire the previous credential at the source and require its rejection. Stopping synchronization alone does not revoke it.

Do not reuse a revision or update a leaf under an existing revision. Failed source operations retain the last complete snapshot. A source authentication failure must not become a global Catalog readiness failure.

## Troubleshoot delivery and recovery

| Symptom | Check and action |
| --- | --- |
| VaultConnection fails TLS | Check VSO's address, certificate names and `caCertSecretRef`. Restore the correct public CA. Do not disable certificate verification. |
| Kubernetes login fails | Check the exact role, namespace, ServiceAccount, audience, TokenReview authority and API reachability. Repair those inputs before retrying. |
| Sync reports a destination conflict | Inspect the Secret owner reference. Remove the conflicting writer only through an approved ownership change. Keep `overwrite: false`. |
| An old Secret remains after a provider outage | Treat its freshness as unconfirmed. Running consumers can retain accepted credentials. Block maintenance until selected inputs are verified. |
| Vault is sealed | Use the provider's authorized unseal procedure. Do not initialize it again. Verify expected KV generations after recovery. |
| A Vault Pod is replaced | Preserve its Raft storage and independently held unseal material. Unseal through the existing procedure and verify synchronization. |
| VSO is unavailable | Restore the external controller. Check conditions, current versions, destination contents and actual consumer acceptance. |
| One source fails authentication | Repair that binding and test its new revision. Check unrelated readiness and preserve the last complete snapshot. |
| A database replacement is incomplete | Keep Catalog stopped. Correct the remaining accepted passwords and DSNs forward, then use coordinated deployment. |

Owner migration requires its DSN and applicable trust/configuration. It does not require source tokens or cursor keys. Gate only the inputs needed by the selected consumer.

Kubernetes stores synchronized Secret data. Base64 is not encryption. Configure Secret access, encryption at rest and protected backups separately. VSO's optional client cache does not encrypt destination Secrets.

PostgreSQL backups do not include Vault, Kubernetes Secrets or historical encryption keys. Preserve required keys and provider recovery material independently under [backup and restore](backup-restore.mdx#recovery-inventory-and-ownership).

## Remove the integration

Stop Catalog through the selected deployment's maintenance procedure before removing required startup inputs. If you are returning to native Kubernetes Secrets, provision and verify their replacement references first.

Remove the exact synchronization resources while VSO still runs:

```sh
kubectl --context "$OAC_CONTEXT" --namespace "$OAC_NAMESPACE" \
  delete vaultstaticsecret catalog-api-database catalog-worker-database \
  catalog-migration-database catalog-cursor-signing catalog-vault-source \
  --ignore-not-found --wait=true --timeout=2m
```

Deletion stops those resources from synchronizing. VSO must finish their finalizers before its controller stops. With this example's owner references, Kubernetes then deletes the created destination Secrets. Verify that they disappear and do not return after another controller reconciliation.

Remove only the Catalog-owned VaultAuth, VaultConnection, synchronization ServiceAccounts and CA Secret. Revoke the owned Vault access and delete its policies through your Vault administrator. Removing a role prevents new logins; it does not revoke issued tokens.

Disable an authentication mount only when it belongs exclusively to this deployment. Do not disable a shared mount or delete shared CRDs, RBAC or controller releases. Preserve real backup keys, unseal material and unrelated provider state.

The local test performs its scoped removal and token revocation before deleting the entire owned profile. Shared-cluster operators must select and retain their own destination and provider lifecycle policies.
