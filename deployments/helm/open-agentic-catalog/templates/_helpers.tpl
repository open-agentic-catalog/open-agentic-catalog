{{- define "open-agentic-catalog.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "open-agentic-catalog.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name (include "open-agentic-catalog.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "open-agentic-catalog.resourceName" -}}
{{- $suffix := printf "-%s" .suffix -}}
{{- $maximumBaseLength := sub 63 (len $suffix) | int -}}
{{- $base := include "open-agentic-catalog.fullname" .root | trunc $maximumBaseLength | trimSuffix "-" -}}
{{- printf "%s%s" $base $suffix -}}
{{- end -}}

{{- define "open-agentic-catalog.chart" -}}
{{- regexReplaceAll "[^a-zA-Z0-9]+$" (printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63) "" -}}
{{- end -}}

{{- define "open-agentic-catalog.selectorLabels" -}}
app.kubernetes.io/name: {{ include "open-agentic-catalog.name" . | quote }}
app.kubernetes.io/instance: {{ .Release.Name | quote }}
{{- end -}}

{{- define "open-agentic-catalog.labels" -}}
helm.sh/chart: {{ include "open-agentic-catalog.chart" . | quote }}
{{ include "open-agentic-catalog.selectorLabels" . }}
app.kubernetes.io/version: {{ regexReplaceAll "[^a-zA-Z0-9]+$" (.Chart.AppVersion | replace "+" "_" | trunc 63) "" | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service | quote }}
app.kubernetes.io/part-of: "open-agentic-catalog"
{{- end -}}

{{- define "open-agentic-catalog.configData" -}}
{{- $tenancyMode := index .Values.configEnv "CATALOG_AUTH_TENANCY_MODE" -}}
{{- $diagnosticBytes := int (index .Values.configEnv "CATALOG_VALIDATION_MAX_DIAGNOSTIC_BYTES") -}}
{{- if or (lt $diagnosticBytes 1024) (gt $diagnosticBytes 262144) -}}
{{- fail "CATALOG_VALIDATION_MAX_DIAGNOSTIC_BYTES must be between 1024 and 262144" -}}
{{- end -}}
{{- range $name := keys .Values.configEnv | sortAlpha -}}
{{- $value := index $.Values.configEnv $name -}}
{{- if and (eq $name "CATALOG_SEMANTIC_SEARCH") $.Values.semanticSearch.enabled -}}
{{- $value = omit $.Values.semanticSearch "existingSecret" | toJson -}}
{{- end -}}
{{- $inactiveDedicatedTenant := and (eq $tenancyMode "shared") (eq $name "CATALOG_AUTH_DEDICATED_TENANT_ID") -}}
{{- $inactiveOrganizationClaim := and (eq $tenancyMode "dedicated") (eq $name "CATALOG_AUTH_ORGANIZATION_CLAIM") -}}
{{- if and (ne $value nil) (not $inactiveDedicatedTenant) (not $inactiveOrganizationClaim) }}
{{ $name }}: {{ $value | quote }}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "open-agentic-catalog.image" -}}
{{- if .Values.image.digest -}}
{{- printf "%s@%s" .Values.image.repository .Values.image.digest -}}
{{- else -}}
{{- printf "%s:%s" .Values.image.repository (default (.Chart.AppVersion | replace "+" "_") .Values.image.tag) -}}
{{- end -}}
{{- end -}}

{{- define "open-agentic-catalog.serviceAccountName" -}}
{{- $account := index .root.Values.serviceAccounts .role -}}
{{- if $account.create -}}
{{- default (include "open-agentic-catalog.resourceName" (dict "root" .root "suffix" .role)) $account.name -}}
{{- else -}}
{{- required (printf "serviceAccounts.%s.name is required when create=false" .role) $account.name -}}
{{- end -}}
{{- end -}}

{{- define "open-agentic-catalog.validate" -}}
{{- $fullname := include "open-agentic-catalog.fullname" . -}}
{{- $suffixes := list "migration" -}}
{{- if not .Values.federation.policy.existingConfigMap -}}
{{- $suffixes = append $suffixes "federation-policy" -}}
{{- end -}}
{{- if .Values.ui.enabled -}}
{{- $suffixes = append $suffixes "ui" -}}
{{- end -}}
{{- if .Values.pgbouncer.enabled -}}
{{- $suffixes = append $suffixes "pgbouncer" -}}
{{- end -}}
{{- range $suffix := $suffixes -}}
{{- if eq $fullname (include "open-agentic-catalog.resourceName" (dict "root" $ "suffix" $suffix)) -}}
{{- fail (printf "generated %s resource name collides with the fullname; choose a shorter or different fullnameOverride/nameOverride" $suffix) -}}
{{- end -}}
{{- end -}}
{{- $tracing := mustFromJson .Values.configEnv.CATALOG_TRACING -}}
{{- if not (kindIs "map" $tracing) -}}
{{- fail "CATALOG_TRACING must be a JSON object" -}}
{{- end -}}
{{- if and (hasKey $tracing "enabled") (not (kindIs "bool" $tracing.enabled)) -}}
{{- fail "CATALOG_TRACING.enabled must be a boolean" -}}
{{- end -}}
{{- if and $tracing.enabled (not .Values.secrets.tracingInputs) -}}
{{- range $field := list "headersFile" "caFile" "clientCertificateFile" "clientKeyFile" -}}
{{- $file := index $tracing $field -}}
{{- if kindIs "string" $file -}}
{{- if or (eq $file "/etc/open-agentic-catalog/tracing") (hasPrefix "/etc/open-agentic-catalog/tracing/" $file) -}}
{{- fail (printf "secrets.tracingInputs is required for tracing.%s under /etc/open-agentic-catalog/tracing" $field) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $verification := mustFromJson .Values.configEnv.CATALOG_TRUST_VERIFICATION -}}
{{- if not (kindIs "map" $verification) -}}
{{- fail "CATALOG_TRUST_VERIFICATION must be a JSON object" -}}
{{- end -}}
{{- if and (hasKey $verification "enabled") (not (kindIs "bool" $verification.enabled)) -}}
{{- fail "CATALOG_TRUST_VERIFICATION.enabled must be a boolean" -}}
{{- end -}}
{{- if and $verification.enabled (not .Values.trustVerification.publicKeysConfigMap) -}}
{{- fail "trustVerification.publicKeysConfigMap is required when verification is enabled" -}}
{{- end -}}
{{- if and .Values.semanticSearch.enabled (or .Values.semanticSearch.tokenFile .Values.semanticSearch.caFile) (not .Values.semanticSearch.existingSecret) -}}
{{- fail "semanticSearch tokenFile/caFile requires semanticSearch.existingSecret" -}}
{{- end -}}
{{/* Use the portable Service naming rule without requiring RelaxedServiceNameValidation. */}}
{{- if not (regexMatch "^[a-z]([-a-z0-9]*[a-z0-9])?$" (include "open-agentic-catalog.fullname" .)) -}}
{{- fail "the generated Service name must start with a lowercase letter; set fullnameOverride to a DNS label starting with a lowercase letter" -}}
{{- end -}}
{{- if eq .Values.databaseRoles.api .Values.databaseRoles.worker -}}
{{- fail "databaseRoles.api and databaseRoles.worker must be distinct" -}}
{{- end -}}
{{- if or (gt (len .Values.databaseRoles.api) 63) (gt (len .Values.databaseRoles.worker) 63) -}}
{{- fail "databaseRoles.api and databaseRoles.worker must each use at most 63 UTF-8 bytes" -}}
{{- end -}}
{{- $apiDatabase := printf "%s/%s" .Values.secrets.apiDatabase.name .Values.secrets.apiDatabase.key -}}
{{- $workerDatabase := printf "%s/%s" .Values.secrets.workerDatabase.name .Values.secrets.workerDatabase.key -}}
{{- $migrationDatabase := printf "%s/%s" .Values.secrets.migrationDatabase.name .Values.secrets.migrationDatabase.key -}}
{{- if or (eq $apiDatabase $workerDatabase) (eq $apiDatabase $migrationDatabase) (eq $workerDatabase $migrationDatabase) -}}
{{- fail "the API, worker, and migration database Secret name/key references must be distinct" -}}
{{- end -}}
{{- $apiServiceAccount := include "open-agentic-catalog.serviceAccountName" (dict "root" . "role" "api") -}}
{{- $workerServiceAccount := include "open-agentic-catalog.serviceAccountName" (dict "root" . "role" "worker") -}}
{{- $migrationServiceAccount := include "open-agentic-catalog.serviceAccountName" (dict "root" . "role" "migration") -}}
{{- if or (eq $apiServiceAccount $workerServiceAccount) (eq $apiServiceAccount $migrationServiceAccount) (eq $workerServiceAccount $migrationServiceAccount) -}}
{{- fail "the API, worker, and migration ServiceAccount names must be distinct" -}}
{{- end -}}
{{- if .Values.ui.enabled -}}
{{- $uiServiceAccount := include "open-agentic-catalog.resourceName" (dict "root" . "suffix" "ui") -}}
{{- if has $uiServiceAccount (list $apiServiceAccount $workerServiceAccount $migrationServiceAccount) -}}
{{- fail "the UI ServiceAccount must be distinct from the API, worker, and migration ServiceAccount names" -}}
{{- end -}}
{{- end -}}
{{- if .Values.pgbouncer.enabled -}}
{{- $poolerServiceAccount := include "open-agentic-catalog.resourceName" (dict "root" . "suffix" "pgbouncer") -}}
{{- if has $poolerServiceAccount (list $apiServiceAccount $workerServiceAccount $migrationServiceAccount) -}}
{{- fail "the PgBouncer ServiceAccount must be distinct from the API, worker, and migration ServiceAccount names" -}}
{{- end -}}
{{- end -}}
{{- end -}}
