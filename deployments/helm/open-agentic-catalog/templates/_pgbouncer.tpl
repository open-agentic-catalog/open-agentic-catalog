{{- define "open-agentic-catalog.pgbouncerConfig" -}}
{{- $p := .Values.pgbouncer -}}
[databases]
{{ $p.upstream.database }} = host={{ $p.upstream.host }} port={{ $p.upstream.port }} dbname={{ $p.upstream.database }}
[users]
{{ .Values.databaseRoles.api }} = pool_size={{ $p.apiPoolSize }} max_user_connections={{ $p.apiPoolSize }}
{{ .Values.databaseRoles.worker }} = pool_size={{ $p.workerPoolSize }} max_user_connections={{ $p.workerPoolSize }}
[pgbouncer]
listen_addr = 0.0.0.0
listen_port = 6432
unix_socket_dir =
pool_mode = session
auth_type = scram-sha-256
auth_file = /etc/pgbouncer/auth/userlist.txt
max_client_conn = {{ $p.maxClientConnections }}
max_db_connections = {{ add (int $p.apiPoolSize) (int $p.workerPoolSize) }}
reserve_pool_size = 0
server_reset_query = DISCARD ALL
query_wait_timeout = {{ $p.queryWaitTimeoutSeconds }}
server_connect_timeout = {{ $p.serverConnectTimeoutSeconds }}
server_idle_timeout = {{ $p.serverIdleTimeoutSeconds }}
client_tls_sslmode = require
client_tls_cert_file = /etc/pgbouncer/tls/tls.crt
client_tls_key_file = /etc/pgbouncer/tls/tls.key
server_tls_sslmode = verify-full
server_tls_ca_file = /etc/pgbouncer/trust/ca.pem
log_connections = 0
log_disconnections = 0
log_stats = 0
{{- end -}}
