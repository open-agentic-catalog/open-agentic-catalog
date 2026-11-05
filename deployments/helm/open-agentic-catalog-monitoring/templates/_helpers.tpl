{{/* Include namespace in names so cross-namespace discovery roles cannot collide. */}}
{{- define "catalog-monitoring.name" -}}
{{- printf "cm-%s-%s" (.Release.Name | trunc 28 | trimSuffix "-") (printf "%s/%s" .Release.Namespace .Release.Name | sha256sum | trunc 8) -}}
{{- end -}}

{{- define "catalog-monitoring.labels" -}}
app.kubernetes.io/name: open-agentic-catalog-monitoring
app.kubernetes.io/instance: {{ .Release.Name | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service | quote }}
app.kubernetes.io/part-of: open-agentic-catalog
{{- end -}}

{{- define "catalog-monitoring.targetNamespace" -}}
{{- default .Release.Namespace .Values.target.namespace -}}
{{- end -}}

{{- define "catalog-monitoring.image" -}}
{{- $pins := .root.Files.Get "files/images.json" | fromJson -}}
{{- required "build the complete chart with make monitoring-package" (default (index $pins .component) (index .root.Values .component "image")) -}}
{{- end -}}

{{- define "catalog-monitoring.prometheus" -}}
global:
  scrape_interval: {{ .Values.prometheus.scrapeIntervalSeconds }}s
  scrape_timeout: {{ .Values.prometheus.scrapeTimeoutSeconds }}s
scrape_configs:
  - job_name: catalog-pods
    metrics_path: /metrics
    scheme: http
    scrape_protocols: [PrometheusText0.0.4]
    kubernetes_sd_configs:
      - role: pod
        namespaces:
          names: [{{ include "catalog-monitoring.targetNamespace" . | quote }}]
        selectors:
          - role: pod
            label: {{ printf "app.kubernetes.io/instance=%s,app.kubernetes.io/component in (api,worker)" .Values.target.release | quote }}
    relabel_configs:
      - source_labels: [__meta_kubernetes_pod_label_app_kubernetes_io_instance]
        regex: {{ regexQuoteMeta .Values.target.release | quote }}
        action: keep
      - source_labels: [__meta_kubernetes_pod_label_app_kubernetes_io_component, __meta_kubernetes_pod_container_port_name]
        regex: 'api;http|worker;operations'
        action: keep
      - source_labels: [__meta_kubernetes_pod_phase]
        regex: Running
        action: keep
      - source_labels: [__meta_kubernetes_pod_label_app_kubernetes_io_component]
        target_label: catalog_role
      - target_label: catalog_deployment
        replacement: {{ .Values.target.deploymentLabel | quote }}
    sample_limit: 10000
    label_limit: 16
    label_name_length_limit: 128
    label_value_length_limit: 256
    body_size_limit: 1MB
{{- end -}}

{{- define "catalog-monitoring.grafana" -}}
{{- .Files.Get "files/grafana.ini" | replace "admin_user = catalog" "admin_user = $__file{/run/secrets/admin_user}" | replace "/run/secrets/grafana_password" "/run/secrets/admin_password" -}}
{{- end -}}

{{- define "catalog-monitoring.datasource" -}}
apiVersion: 1
datasources:
  - name: Catalog Prometheus
    type: prometheus
    uid: catalog-prometheus
    access: proxy
    url: http://{{ include "catalog-monitoring.name" . }}-prometheus:9090
    isDefault: true
    editable: false
    jsonData:
      timeInterval: {{ printf "%ds" (int .Values.prometheus.scrapeIntervalSeconds) | quote }}
{{- end -}}
