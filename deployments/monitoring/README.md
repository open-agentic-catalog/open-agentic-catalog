# Optional monitoring assets

Use [Monitoring with Prometheus and Grafana](../../documentation/src/content/docs/operations/helm-monitoring.mdx) for collector setup, manual dashboard import and file provisioning from this directory. Use the [Grafana walkthrough](../../documentation/src/content/docs/guides/performance-and-monitoring.md#read-the-grafana-dashboard) for dashboard navigation, a screenshot and panel interpretation. The [Prometheus procedure](../../documentation/src/content/docs/guides/performance-and-monitoring.md#inspect-prometheus-collection) explains target checks. Traces use the independently enabled [Jaeger viewer](../../documentation/src/content/docs/operations/observability.mdx#view-traces-locally).

These files configure the opt-in local stack in [compose.monitoring.yaml](../../compose.monitoring.yaml) and supply shared image pins, dashboard and Grafana assets to the [independent companion chart](../helm/open-agentic-catalog-monitoring/README.md). The Catalog emits application metrics without either collector. Production monitoring discovery, storage, infrastructure exporters and alerts belong to the adopting operator.

| Path | Purpose |
| --- | --- |
| [images.json](images.json) | Reviewed external-tool image pins consumed by the monitoring workflow. |
| [prometheus.yaml](prometheus.yaml) | Local API/worker scrape configuration. |
| [kubernetes-scrape.example.yaml](kubernetes-scrape.example.yaml) | Operator-adapted per-Pod discovery for an existing Prometheus or Collector Prometheus receiver; not installed automatically. |
| [grafana.ini](grafana.ini) | Local Grafana configuration and exposure controls. |
| [dashboards/catalog.json](dashboards/catalog.json) | Authoritative Catalog dashboard and query expressions. |
| [provisioning/](provisioning) | Data source and dashboard provisioning plus explicit empty alerting/plugin configuration. |

The [performance and monitoring guide](../../documentation/src/content/docs/guides/performance-and-monitoring.md) explains enablement, query interpretation, operator label adaptation and cleanup. The [observability runbook](../../documentation/src/content/docs/operations/observability.mdx) defines application metrics. Do not point the local fixtures at another project's monitoring or treat local scrape labels as production requirements.

Keep dashboard expressions authoritative here; [test/monitoring/](../../test/monitoring/README.md) extracts them for semantic checks. `make test-monitoring` exercises an owned disposable stack. `make compose-up LOCAL_MONITORING=on` instead retains a developer stack and history. Destructive reset is separate and must preserve Catalog PostgreSQL unless its deletion was explicitly selected.

The Job and webhook retry panels show process-local scheduling rates and mean saved delays, including hints and jitter. They do not measure actual queue wait or reconstruct durable history. Missing observations and unavailable scrapes remain distinct from observed zero rates; the mean is absent when its interval contains no retry samples.

Maintain stable digest-pinned upstream images with a recent version/license check and the affected Compose/Helm monitoring tests. The [dependency reference](../../documentation/src/content/docs/reference/dependencies.md#optional-local-monitoring-tools) defines this lightweight update workflow and retains historical scan context; there is no separate monitoring-image security approval gate. The chart packages references and configuration, not the upstream images themselves.
