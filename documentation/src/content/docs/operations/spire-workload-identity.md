---
title: Use SPIRE to read and federate Catalog metadata
description: Run SPIRE locally, read a private Resource and an authenticated ARD publication as a workload, and verify credential renewal between two Catalogs.
---

A reporting job needs to read one team's private Resource and the current published Agent. In this tutorial the job uses its own SPIFFE ID. It asks its local SPIRE Agent for a short-lived bearer token (a JWT-SVID) signed by the SPIRE Server for the Catalog audience. Catalog verifies the token and applies the reporter's current grants on each read.

First you'll make the calls yourself against a disposable Helm deployment. Then you'll run a second example in which an importing Catalog keeps reading its upstream as the credential changes. Both examples are opt-in. An ordinary Catalog installation needs neither SPIRE nor a workload socket.

## Start a Catalog and SPIRE on Minikube

Start in a Catalog source checkout on Linux amd64. Bring the [Minikube prerequisites and pinned images](deployment.mdx#maintained-local-minikube-verification), the repository Go toolchain, Python 3.11 or later, OpenSSL and the GitHub CLI. For the interactive steps, use Bash, curl and jq. The installer downloads and verifies the SPIRE 1.16.0 tools for the local Docker engine.

```sh
make spire-tools-install
make minikube-spire-lab
```

Leave this terminal running. The launcher creates a private Minikube profile, deploys PostgreSQL and Catalog, and starts the SPIRE Server, Agent and OIDC Discovery Provider. It registers the reporter's Unix UID on the Server. The Agent checks the calling process against that registration when it requests a token. Catalog reads the public signing keys from the provider over HTTPS.

Wait for **SPIRE lab ready**. In a second Bash terminal, run the printed readiness command. It connects directly to the local port-forward and verifies the certificate for `catalog-a.test` with the lab CA. Expect `{"status":"ready"}`. Run the following block in that same terminal. At the prompts, enter the kubeconfig path, profile name and port from the launcher's output. The port is the number after `127.0.0.1:` in the printed curl command.

```bash
read -r -p 'Lab kubeconfig path: ' lab_config
read -r -p 'Lab profile name: ' lab_profile
read -r -p 'Local forwarded port: ' lab_port
lab_ca="$(dirname "$lab_config")/spire/trust/ca.pem"

lab_kubectl() {
  kubectl --kubeconfig "$lab_config" --context "$lab_profile" -n test "$@"
}

lab_kubectl get pods server workloads frontend
lab_kubectl exec server -c server -- /opt/spire-server entry show \
  -spiffeID spiffe://workloads.test/reporter
```

Look for `spiffe://workloads.test/reporter` and the selector `unix:uid:11001`. This registration lets the reporter obtain its identity. Its Catalog service membership separately grants Reader in the private Resource's namespace and `discovery.read` for the tenant's publication.

## Read as the reporter

Define two helpers in the second terminal. The first sends a GET to the lab's fixed Catalog origin. The second asks the Agent for a token **as the reporter**, then pipes the authorization header directly to curl. Leave shell tracing and verbose HTTP logging off. The token stays out of terminal output, shell history and curl's command-line arguments.

```bash
set -o pipefail

catalog_get() {
  local path="$1"
  shift
  curl --silent --show-error --noproxy '*' --cacert "$lab_ca" \
    --connect-to "catalog-a.test:8443:127.0.0.1:$lab_port" \
    "$@" "https://catalog-a.test:8443$path"
}

reporter_get() {
  lab_kubectl exec workloads -c reporter -- /opt/spire-agent api fetch jwt \
    -socketPath /workload/api.sock -spiffeID spiffe://workloads.test/reporter \
    -audience open-agentic-catalog -output json |
    jq -er '.[].svids[]? | select(.spiffe_id == "spiffe://workloads.test/reporter") | "Authorization: Bearer " + .svid' |
    catalog_get "$1" --fail --header @-
}

catalog_get /.well-known/ard.json --output /dev/null --write-out '%{http_code}\n'
reporter_get /api/v1/me | jq '{subject, actorType, tenantId}'
```

The first call should return **401**. The second should identify `spiffe://workloads.test/reporter` as a `service` in tenant `00000000-0000-7000-8000-000000000001`.

```bash
private_id=$(reporter_get '/api/v1/resources?canonicalIdentifier=urn:air:tutorial.catalog.example:agents:private-reporter' |
  jq -er '.items | select(length == 1) | .[0].id')
reporter_get "/api/v1/resources/$private_id" | jq '{id, canonicalIdentifier, namespaceId}'
reporter_get /.well-known/ard.json | jq '[.entries[] | {identifier, version}]'
```

The Control API returns the private Resource ending in `private-reporter`. In the ARD manifest, look for `urn:air:tutorial.catalog.example:agents:federation-qualification` at version `1.0.0`. The private Resource is absent. In an application, have the SDK's token provider acquire the credential from the workload's own Agent. The [Go SDK example and CLI instructions](../guides/integrating.mdx#read-a-private-resource-and-publication-with-spire) use the same grants and HTTPS trust.

When finished, press **Ctrl+C** in the terminal running `make minikube-spire-lab`. The launcher stops its port-forward and removes its own profile. Wait for it to exit before starting the next example. To repeat these reads as an automated check, run `make test-minikube-spire`. For a Docker Compose checkout, `make test-interoperability-spire` exercises the client against the same authenticated endpoints.

## Renew a credential between two Catalogs

In this example A publishes an Agent. B imports it from A's authenticated ARD host. A separate writer uses the SPIFFE ID `spiffe://workloads.test/source-reader` and asks for **A's** audience. B's API uses the credential when testing the Source; B's worker uses it when synchronizing. The person administering B continues to use B's own credentials.

From the same checkout, use Docker Compose with the SPIRE tools installed above and these pinned fixture images:

```sh
docker pull pgvector/pgvector:pg18@sha256:38681dc6eaf55308e2a4c6de392d131652dcfac70e1263e35cd9b5ea3fe39782
docker pull python:3.13.18-bookworm@sha256:cc55e4ded9c95f6ce2133b3d096db43f214d280c2a8af65d83e246c8fa7d168c
make test-interoperability-spire-renewal
```

The example starts both Catalogs, imports A's Agent, interrupts credential delivery and lets the writer renew automatically. It checks that B can still find the last complete import during the interruption and that a new import succeeds after renewal. It removes the Compose project and its private inputs when it finishes. To see which Agent and snapshots your run produced, enter the directory printed after **SPIRE qualification**:

```bash
read -r -p 'Renewal run directory: ' renewal_run
jq '{sourceReadIdentity, identifier, version,
     first: (.initial | {credentialRevision, snapshotId}),
     renewed: (.unattendedRenewal | {credentialRevision, snapshotId}),
     result, cleanup}' "$renewal_run/report.json"
```

The revision and snapshot ID should both change. The published identifier and version stay the same, and both `result` and `cleanup` should say `passed`.

To try the Kubernetes delivery route, run `make test-minikube-spire-renewal`. Here the writer updates the token and its matching binding in one Kubernetes Secret. The two importing pods see the new revision without restarting. The test then disables credential resolution, checks that the next bound import fails and reads the previously indexed Agent again. The target removes its own cluster.

## Connect an existing installation

1. **Configure SPIRE.** Set the Server's `jwt_issuer` to the HTTPS issuer advertised by the OIDC Discovery Provider. In the lab both use `https://identity.test:8447`. The Server signs with `jwt_key_type = "ec-p256"`, and the provider serves keys for the `workloads.test` trust domain at its advertised `jwks_uri`. Replace these names with yours. Register each reader under its own SPIFFE ID and a workload selector your Agent can attest. Follow the upstream instructions for [installing the Server](https://spiffe.io/docs/latest/deploying/install-server/), [installing Agents](https://spiffe.io/docs/latest/deploying/install-agents/) and [registering workloads](https://spiffe.io/docs/latest/deploying/registering/).
2. **Configure Catalog A.** Set the exact issuer, audience, dedicated tenant and authenticated discovery host in the [workload authentication profile](../guides/integrating.mdx#read-a-private-resource-and-publication-with-spire). With Helm, `auth.trustBundleConfigMap` selects the ConfigMap containing the issuer CA in `ca.pem`. Give each reader trust in A's HTTPS CA. Catalog accepts one issuer. This profile uses JWT-SVIDs without organization or OAuth scope claims and requires deployment-wide scope enforcement to be off. Keep your existing issuer on installations that require scoped access tokens or interactive sign-in.
3. **Grant each read.** Provision the exact issuer and SPIFFE subject as a Catalog service membership. Give the reporter Reader in the intended namespace and tenant-wide `discovery.read` for the authenticated manifest. A writer used solely for federation needs only the publication grant on A. Publish and activate a release for the target host before checking the manifest.
4. **Configure Catalog B if you import from A.** Leave B's inbound issuer as configured. Approve A's exact HTTPS origin in the importing tenant's federation policy. With Helm, merge the [importer values](../../../../../deployments/helm/open-agentic-catalog/examples/spire-source-values.yaml) into B's complete values to enable credential resolution for its API and worker. Give only the writer access to the Agent socket. Mount the binding read-only in both Catalog roles. Follow [the source renewal procedure](federation.mdx#renew-a-source-bearer-with-spire) to inspect, test, synchronize and read back the imported entry.

For example, the Minikube launcher creates the following registration from the Server's administrative environment. When adapting it to your own installation, use the SPIFFE ID of your attested Agent as the parent and the selector for your reporter. Keep the Server's administrative socket private.

```sh
spire-server entry create \
  -parentID spiffe://workloads.test/agent \
  -spiffeID spiffe://workloads.test/reporter \
  -selector unix:uid:11001 -jwtSVIDTTL 900
```

If an otherwise valid token receives **401**, check the issuer, audience, expiry and active membership. For **403**, check the namespace grant or tenant publication grant for the specific read. A source test on B can instead report **503** for either an unavailable binding or an upstream refusal. The [federation troubleshooting table](federation.mdx#renew-a-source-bearer-with-spire) explains how to tell them apart.
