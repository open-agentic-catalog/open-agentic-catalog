# Monitoring screenshots

`catalog-overview.png` shows the first six panels of the provisioned Grafana dashboard. It uses real samples from a disposable local Catalog, Prometheus and Grafana stack. The data is synthetic. The image illustrates controls and signals, not production capacity. No customer data, passwords, cookies or saved browser sessions enter the manual.

The [dashboard JSON](../../../deployments/monitoring/dashboards/catalog.json) owns the panel layout and queries. The [monitoring guide](../../src/content/docs/guides/performance-and-monitoring.md#read-the-grafana-dashboard) owns reader instructions. Refresh the image when the pictured controls or panels change.

## Prepare a separate local fixture

Install the documented Docker/Compose and Go prerequisites. Install the repository browser tools with `make ui-install` and `make ui-browser-install` if needed. Run the commands below from the repository root in one shell.

Choose an unused subnet and loopback ports. Check existing Docker networks and listeners first. Do not stop another project to free a port. The example subnet and collector ports are choices, not reserved resources:

```sh
CAPTURE_PROJECT="catalog-monitoring-docs-$(python3 -c 'import secrets; print(secrets.token_hex(4))')"
CAPTURE_IMAGE="open-agentic-catalog:$CAPTURE_PROJECT"
CAPTURE_SUBNET=172.30.253.0/24
CAPTURE_GRAFANA_PORT=13088
CAPTURE_PROMETHEUS_PORT=19088
CAPTURE_API_PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')
mkdir -p output/playwright/monitoring

capture_make() {
  env -i PATH="$PATH" HOME="$HOME" DOCKER_CONTEXT=default \
    make CATALOG_ENV_FILE=/dev/null COMPOSE_PROJECT="$CAPTURE_PROJECT" \
    IMAGE="$CAPTURE_IMAGE" CATALOG_COMPOSE_SUBNET="$CAPTURE_SUBNET" "$@"
}

capture_make compose-up LOCAL_MONITORING=on LOCAL_UI=off LOCAL_WEBHOOKS=off \
  LOCAL_EMBEDDINGS=off LOCAL_DEMO_ARCHIVES=off LOCAL_DEMO_RUNTIMES=off \
  CATALOG_COMPOSE_HTTP_PORT="$CAPTURE_API_PORT" CATALOG_COMPOSE_POSTGRES_PORT=0 \
  CATALOG_MONITORING_GRAFANA_PORT="$CAPTURE_GRAFANA_PORT" \
  CATALOG_MONITORING_PROMETHEUS_PORT="$CAPTURE_PROMETHEUS_PORT"
```

The clean environment excludes inherited runtime credentials and the developer `.env`. A nonstandard Docker credential-directory setup can explicitly pass its reviewed `DOCKER_CONFIG` path. The launcher builds a separate image and performs migration/bootstrap only in the selected project. The API port must be positive because Compose also uses it in the public base URL. The socket command finds a candidate; Docker still decides whether the port is free at startup. If startup fails, inspect and clean only this project.

Seed the fixture through the shipped command:

```sh
docker exec "$CAPTURE_PROJECT-api-1" /usr/local/bin/catalogctl smoke \
  --base-url http://127.0.0.1:8080 --run-id monitoring-docs --timeout 3m \
  --admin-token local-admin --contributor-token local-contributor \
  --reviewer-token local-reviewer --publisher-token local-publisher \
  --reader-token local-reader --other-tenant-token other-tenant-admin \
  > output/playwright/monitoring/seed.json
```

These tokens are synthetic development identities. This command mutates only the new fixture. Do not run it against a retained Catalog merely to refresh an image.

## Generate samples and capture

Reuse the existing bounded read client to populate the charts:

```sh
python3 - "$CAPTURE_API_PORT" <<'PY'
import json, pathlib, sys
p = pathlib.Path('output/playwright/monitoring/profile.json')
p.write_text(json.dumps({'scenario': 'local', 'query': '', 'port': int(sys.argv[1]),
                        'warmupSeconds': 5, 'seconds': 180, 'workers': 1,
                        'maxRequests': 50000}))
p.chmod(0o600)
PY
go run ./test/performance output/playwright/monitoring/profile.json \
  output/playwright/monitoring/workload.json
```

Wait for healthy API and worker scrapes. Check `up{catalog_deployment="local",job=~"catalog-(api|worker)"}` in this fixture's Prometheus. Both targets must report `1`. Confirm positive request-rate samples and finite latency data before capture. Require a passing workload with no failed reads. This workload supplies an illustration; it is not a controlled performance comparison.

Use the installed Playwright library from `catalog-ui/node_modules/playwright`. Open an ephemeral Chromium context at `http://127.0.0.1:<selected Grafana port>/login`. Read `output/monitoring/<CAPTURE_PROJECT>/grafana-password` inside the automation process. Fill the username and password fields, then click **Log in**. Do not pass the password as a command argument, print it, record a trace or save browser storage.

The following capture uses the provisioned dashboard UID directly. Review the folder navigation and role controls separately:

```sh
export CAPTURE_PROJECT CAPTURE_GRAFANA_PORT
env -u WAYLAND_DISPLAY node <<'JS'
const fs = require('node:fs');
const { chromium } = require('./catalog-ui/node_modules/playwright');
(async () => {
  const browser = await chromium.launch({ headless: true });
  try {
    const context = await browser.newContext({ viewport: { width: 1280, height: 1200 }, colorScheme: 'light' });
    const page = await context.newPage();
    const base = 'http://127.0.0.1:' + process.env.CAPTURE_GRAFANA_PORT;
    await page.goto(base + '/login');
    await page.getByRole('textbox', { name: 'Email or username' }).fill('catalog');
    const password = fs.readFileSync('output/monitoring/' + process.env.CAPTURE_PROJECT + '/grafana-password', 'utf8').trim();
    await page.getByRole('textbox', { name: 'Password', exact: true }).fill(password);
    await page.getByRole('button', { name: 'Log in', exact: true }).click();
    await page.waitForURL(u => u.pathname !== '/login');
    await page.goto(base + '/d/catalog-overview?from=now-5m&to=now&var-datasource=catalog-prometheus&var-deployment=local&var-role=$__all&theme=light');
    await page.getByRole('heading', { name: 'Completed requests in window', exact: true }).waitFor();
    await page.getByRole('button', { name: 'Close menu', exact: true }).click();
    await page.waitForTimeout(5000);
    const panel = await page.getByRole('region', { name: 'Completed requests in window', exact: true }).boundingBox();
    if (!panel || panel.y < 0) throw new Error('Dashboard viewport is incomplete');
    await page.screenshot({ path: 'output/playwright/monitoring/catalog-overview.png',
      clip: { x: 0, y: 0, width: 1280, height: Math.ceil(panel.y + panel.height + 8) } });
    await context.close();
  } finally {
    await browser.close();
  }
})().catch(() => { console.error('Capture failed; inspect the owned fixture without exposing credentials.'); process.exitCode = 1; });
JS
```

The image uses `Catalog Prometheus`, deployment `local`, Role `All`, a five-minute range and the light Grafana theme. Keep selector labels and legends readable. The native screenshot clip excludes lower rows without editing the chart content. The command-scoped Wayland adjustment follows the existing WSL headless-browser workaround.

Check **Dashboards**, **Catalog** and **Catalog overview** in the browser. Check Role `api` and `worker` separately before restoring `All`. API-only selection must not show worker snapshot values. Inspect the browser's console and failed requests. Close the context and browser after capture. Keep raw screenshots in `output/playwright/monitoring/`; copy only the reviewed image to this directory.

Register the image in `documentation/scripts/site.mjs`. Preserve descriptive alternative text, the synthetic-data caption and full-size link in the guide. Run `make check` and `make docs-site-check`. Review the rendered image at desktop/mobile widths, both site themes and root/non-root bases.

Run `make test-monitoring` separately for collection, query and recovery qualification. That target cleans up its own stack and does not capture screenshots. Do not add browser traffic to its timed phase or retain its resources for this workflow. Record source/tool/image identities, results and failures under ignored `output/`.

## Clean up this fixture

Run cleanup after success, failure or interruption. These commands delete the selected fixture's data; check the unique project name first:

```sh
capture_make compose-reset LOCAL_MONITORING=on
docker image rm "$CAPTURE_IMAGE"
```

Verify that no containers, networks or volumes remain with `com.docker.compose.project=$CAPTURE_PROJECT`. Remove only `output/monitoring/$CAPTURE_PROJECT/grafana-password` and its now-empty private directory. Keep reviewed evidence, shared image layers and other projects. Do not prune Docker or reset the retained development stack.

## Additional diagnostic views

| Image | Owner and selected state | Evidence scope |
| --- | --- | --- |
| `catalog-database-worker.png` | The separate monitoring fixture above, dashboard panels 7-12. | Real synthetic samples with Catalog Prometheus, deployment local, Role All and a 30-minute window. |
| `prometheus-target-health.png` | The same fixture, Prometheus `/targets`. | Both latest scrapes succeed; this is not application readiness. |
| `grafana-dashboard-import.png` | The same fixture, Grafana dashboard import. | Shipped JSON prepared with unused UID `catalog-docs-import`; cancelled without importing. |
| `jaeger-job-attempt.png` | The selected tracing-viewer fixture below, exact worker attempt. | A separate worker root with its link to the enqueue cause. |
| `jaeger-job-cause.png` | The same trace pair, opened through the actual link. | Highlighted enqueue and its parent HTTP request, not the replay. |

For the database/worker image, use the dashboard URL above with `from=now-30m`. Wait for finite samples in panels 7-12. Scroll to render the final panel, then scroll the first selected panel below the toolbar. Capture the complete six-panel region without changing dashboard layout or samples.

For target health, open the owned Prometheus `/targets` page. Require both `catalog-api` and `catalog-worker` to be UP. Capture their endpoint rows without authentication settings.

For import, open Grafana **Dashboards > New > Import**. Upload `deployments/monitoring/dashboards/catalog.json`. Choose a synthetic name and folder, then change the UID to an unused value. Require the availability result before capture. Do not select **Import**. Cancel, then verify that the unused UID is absent and `catalog-overview` remains unchanged. The pinned form has no data-source mapping prompt; select Prometheus in the dashboard afterward.

### Capture a real Job trace pair

Use the [tracing harness](../../../test/tracing/README.md#query-a-real-job-handoff-in-jaeger). Check the fixed viewer project and selected loopback ports before startup. Do not borrow or stop an existing viewer.

```sh
make trace-viewer-up
make test-tracing TRACING_VIEWER=on
```

Require a successful report and Catalog fixture cleanup. Read its `tracingViewer.backend.queryUrl` and exact `spans` references inside the browser script. Use only that local origin. Open the worker attempt and verify its separate root and federation-sync child. Capture the timeline with the native link control visible and raw attributes closed.

Click that link. Require a new tab with the exact enqueue trace and highlighted enqueue span. Expand its parent HTTP request and verify the reported request span ID. Capture the cause timeline with raw attributes closed. Do not reconstruct spans or substitute the replay trace.

Close the browser, then run `make trace-viewer-down`. Verify that the owned viewer container and network are absent before promoting the pair. Keep the report and actual trace IDs under ignored output; they are synthetic capture references, not application configuration.

The current additional Grafana and Prometheus images use owned fixture `catalog-monitoring-docs-d257158b`, with 6,298 successful synthetic reads and no failed reads. Import cancellation and scoped cleanup passed. The Jaeger pair uses passing report `output/functional/run-d1fc75ba5208/report.json`; native link navigation and viewer cleanup passed.
