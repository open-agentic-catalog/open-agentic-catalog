"""Owned session-pooling qualification; no ambient DSNs or retained services."""
from __future__ import annotations

import argparse
from datetime import datetime, timedelta, timezone

from abrupt import validate_recovery_outcomes
from tls_rotation import validate_evidence
import hashlib
import importlib.util
import json
import os
import re
from pathlib import Path
import secrets
import signal
import socket
import ssl
import subprocess
import tempfile
import time

ROOT = Path(__file__).resolve().parents[2]
POOLER_IMAGE = "edoburu/pgbouncer:v1.26.0-p0@sha256:b17551c776ef7e5769ef80b956d20f85e2fd25dd8912d31d58f782aad495b711"
POSTGRES_IMAGE = "pgvector/pgvector:pg18@sha256:2358fcba361ed2233a5ed81b5fe4ca779ccb304120ce531a3bf51c0ed7e2bc11"


def supervisor():
    """Reuse the maintained bounded process-group supervisor and source digest."""
    spec = importlib.util.spec_from_file_location("catalog_functional_supervisor", ROOT / "test/functional/runner.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


SHARED = supervisor()


def command(arguments: list[str], *, timeout: int = 60, **kwargs: object) -> str:
    """Bound output/deadlines and kill owned child groups on interruption."""
    try:
        return SHARED.command(arguments, kwargs.get("env", dict(os.environ)), timeout,
                              limit=2 << 20, capture=True, failed_stdout=kwargs.get("failed_stdout")).decode().strip()
    except SHARED.Failure as error:
        raise RuntimeError(f"{arguments[0]} failed: {error}") from None


REQUIRED_CASES = frozenset("TestPgBouncer/" + name for name in (
    "compiled-processes", "compiled-processes/tls-renewal", "runtime-admission", "session-lock-and-transaction-context",
    "reset-capacity-and-cancellation", "authentication-and-client-trust",
    "upstream-trust-refusal", "tls-identities", "role-timeout-defaults", "runtime-privilege-refusal", "pooler-and-writer-recovery"))


def completed_cases(output: str) -> list[str]:
    """Reject missing, skipped or failed assertions, not only an exit status."""
    passed = set()
    for line in output.splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            raise RuntimeError("invalid Go event report") from None
        if not isinstance(event, dict):
            raise RuntimeError("invalid Go event report")
        if event.get("Action") in ("skip", "fail"):
            raise RuntimeError("incomplete Go qualification")
        if event.get("Action") == "pass" and event.get("Test"):
            passed.add(event["Test"])
    if not REQUIRED_CASES.issubset(passed) or "TestPgBouncer" not in passed:
        raise RuntimeError("missing Go qualification cases")
    return sorted(REQUIRED_CASES)


def validate_abrupt_evidence(value):
    """Require the held-work proof, not just a successful promotion."""
    if (not isinstance(value, dict) or value.get("selection") != "abrupt"
            or not all(value.get(key) is True for key in (
                "heldOnPrimary", "heldOnStandby", "writerFenced", "promoted"))
            or not all(isinstance(value.get(key), str) and re.fullmatch(
                r"[0-9A-F]{1,8}/[0-9A-F]{1,8}", value[key]) for key in ("barrier", "replayedBarrier"))
            or value.get("phase") != "recovery" or value.get("oldWriterExitCode") != 137
            or type(value.get("jobAttempt")) is not int or value["jobAttempt"] != 1
            or not isinstance(value.get("jobOwner"), str) or not 1 <= len(value["jobOwner"]) <= 128
            or type(value.get("recoverySeconds")) not in (int, float) or not 0 <= value["recoverySeconds"] <= 180
            or any(not isinstance(value.get(key), str) or not re.fullmatch(
                r"[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}", value[key]) for key in ("reservation", "job", "outbox"))
            or not isinstance(value.get("outboxSHA256"), str) or not re.fullmatch(r"sha256:[0-9a-f]{64}", value["outboxSHA256"])
            or not isinstance(value.get("processes"), list) or len(value["processes"]) != 4
            or any(not isinstance(v, dict) or set(v) != {"instanceId", "startedAt"}
                   or any(not isinstance(s, str) or not 1 <= len(s) <= 128 for s in v.values()) for v in value["processes"])
            or len({v["instanceId"] for v in value["processes"]}) != 4):
        raise RuntimeError("incomplete abrupt writer-loss evidence")
    if value.get("degradedListeners") != 4 or value.get("recoveredListeners") != 4:
        raise RuntimeError("incomplete listener recovery")
    try:
        validate_recovery_outcomes(value.get("outcomes"))
    except ValueError as error:
        raise RuntimeError(str(error)) from None
    if value["outcomes"]["jobOwner"] == value["jobOwner"]:
        raise RuntimeError("Job did not transfer to the other worker")
    high, low = (int(v, 16) for v in value["barrier"].split("/"))
    replay_high, replay_low = (int(v, 16) for v in value["replayedBarrier"].split("/"))
    if (replay_high, replay_low) < (high, low):
        raise RuntimeError("standby did not replay held work")


def private_file(path: Path, text: str) -> None:
    path.write_text(text, encoding="utf-8")
    path.chmod(0o600)


def tls_evidence(directory):
    """Retain a validated renewal checkpoint even if a later case fails."""
    path = directory / "tls-evidence.json"
    if path.stat().st_size > 65536:
        raise ValueError("oversized TLS renewal evidence")
    evidence = json.loads(path.read_text(encoding="utf-8"))
    expected = {role + suffix: "sha256:" + hashlib.sha256(ssl.PEM_cert_to_DER_cert(
        (directory / (role + suffix + ".pem")).read_text(encoding="utf-8"))).hexdigest()
        for role in ("postgres", "pooler") for suffix in ("", "-renewed", "-next")}
    validate_evidence(evidence, expected)
    return evidence


def certificates(directory: Path) -> None:
    now = datetime.now(timezone.utc)
    for authority in ("ca", "ca-next"):
        command(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "7",
                 "-subj", f"/CN=owned-pgbouncer-{authority}", "-keyout", str(directory / f"{authority}.key"),
                 "-out", str(directory / f"{authority}.pem")])
        database = directory / authority
        database.mkdir(mode=0o700)
        private_file(database / "index", "")
        private_file(database / "serial", "01\n")
        private_file(database / "issuer.ini", f"""[ca]
default_ca = issuer
[issuer]
database = {database / 'index'}
serial = {database / 'serial'}
new_certs_dir = {database}
certificate = {directory / (authority + '.pem')}
private_key = {directory / (authority + '.key')}
default_md = sha256
policy = subject
unique_subject = no
[subject]
commonName = supplied
""")
    for role, sans in (("postgres", "DNS:postgres,DNS:standby,DNS:writer,DNS:pg,DNS:expired-postgres,DNS:future-postgres,DNS:old-postgres,IP:127.0.0.1"),
                       ("pooler", "DNS:pooler,IP:127.0.0.1")):
        for variant, authority, start, end in (
                ("", "ca", -1, 2), ("-renewed", "ca", -1, 3), ("-next", "ca-next", -1, 2),
                ("-expired", "ca", -2, -1), ("-future", "ca", 1, 2)):
            name = role + variant
            command(["openssl", "req", "-new", "-newkey", "rsa:2048", "-nodes",
                     "-subj", f"/CN={role}", "-keyout", str(directory / f"{name}.key"),
                     "-out", str(directory / f"{name}.csr")])
            private_file(directory / f"{name}.ext", f"subjectAltName={sans}\nextendedKeyUsage=serverAuth\n")
            command(["openssl", "ca", "-batch", "-config", str(directory / authority / "issuer.ini"),
                     "-startdate", (now + timedelta(days=start)).strftime("%Y%m%d%H%M%SZ"),
                     "-enddate", (now + timedelta(days=end)).strftime("%Y%m%d%H%M%SZ"),
                     "-in", str(directory / f"{name}.csr"), "-notext",
                     "-extfile", str(directory / f"{name}.ext"), "-out", str(directory / f"{name}.pem")])
    command(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2",
             "-subj", "/CN=untrusted-database-ca", "-keyout", str(directory / "untrusted.key"),
             "-out", str(directory / "untrusted.pem")])
    for path in directory.iterdir():
        if path.is_file():
            path.chmod(0o600)


def configuration() -> str:
    # Aliases exist only as negative protocol controls. The shipped chart has one
    # exact database and cannot select unsupported modes or bypass TLS.
    return """[databases]
catalog_test = host=writer port=5432 dbname=catalog_test pool_size=12
transaction = host=writer port=5432 dbname=catalog_test pool_mode=transaction pool_size=1
statement = host=writer port=5432 dbname=catalog_test pool_mode=statement pool_size=1
capacity = host=writer port=5432 dbname=catalog_test pool_size=1
timeouts = host=writer port=5432 dbname=catalog_test pool_size=1
badhost = host=wrong-name port=5432 dbname=catalog_test pool_size=1
expired = host=expired-postgres port=5432 dbname=catalog_test pool_size=1
future = host=future-postgres port=5432 dbname=catalog_test pool_size=1
oldchain = host=old-postgres port=5432 dbname=catalog_test pool_size=1
[users]
catalog_api = max_user_connections=16
catalog_worker = max_user_connections=16
[pgbouncer]
listen_addr = 0.0.0.0
listen_port = 6432
unix_socket_dir =
auth_type = scram-sha-256
auth_file = /lab/userlist.txt
pool_mode = session
max_client_conn = 40
reserve_pool_size = 0
server_reset_query = DISCARD ALL
query_wait_timeout = 3
server_connect_timeout = 3
server_login_retry = 1
server_idle_timeout = 1
client_tls_sslmode = require
client_tls_cert_file = /lab/pooler.pem
client_tls_key_file = /lab/pooler.key
server_tls_sslmode = verify-full
server_tls_ca_file = /lab/ca.pem
log_connections = 0
log_disconnections = 1
log_stats = 0
"""


def owned_standby_restart(script: str) -> str:
    """Allow only this invocation's retained replica, preserving Compose escaping."""
    fresh = 'test ! -e "$$PGDATA/PG_VERSION"'
    launch = 'exec postgres -c ssl=on'
    if script.count(fresh) != 1 or script.count(launch) != 1:
        raise RuntimeError("standby fixture command changed")
    retained = ('if [ -f "$$PGDATA/qualification-owner" ]; then '
                'test "$$(cat "$$PGDATA/qualification-owner")" = "$$CATALOG_FIXTURE_OWNER"; '
                'exec gosu postgres postgres -c ssl=on -c ssl_cert_file=/tmp/server.pem '
                '-c ssl_key_file=/tmp/server.key; fi; ')
    marker = 'printf "%s" "$$CATALOG_FIXTURE_OWNER" > "$$PGDATA/qualification-owner"; '
    return script.replace(fresh, retained + fresh).replace(launch, marker + launch)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--writer-loss", choices=("planned", "abrupt"), default="planned")
    selection = parser.parse_args().writer_loss
    os.chdir(ROOT)
    report_dir = ROOT / "output" / "pgbouncer" / ("run-" + secrets.token_hex(6))
    report_dir.mkdir(parents=True, mode=0o700)
    project = "oac-pgbouncer-" + secrets.token_hex(6)
    report: dict[str, object] = {"status": "failed", "project": project, "writerLoss": selection,
        "images": {"postgres": POSTGRES_IMAGE, "pgbouncer": POOLER_IMAGE},
        "revision": command(["git", "rev-parse", "HEAD"]), "checks": [], "cleanup": False}
    paths = command(["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"]).rstrip("\0").split("\0")
    report["worktreeDigest"] = SHARED.digest_files(set(paths))
    def interrupted(_number: int, _frame: object) -> None:
        raise InterruptedError("owned qualification interrupted")
    for number in (signal.SIGINT, signal.SIGTERM):
        signal.signal(number, interrupted)
    with tempfile.TemporaryDirectory(prefix=project + "-") as temp:
        directory = Path(temp)
        creation_attempted = False
        try:
            for resource in (["ps", "-aq"], ["network", "ls", "-q"], ["volume", "ls", "-q"]):
                if command(["docker", *resource, "--filter", f"label=com.docker.compose.project={project}"]):
                    raise RuntimeError("fixture project already exists; not selected")
            certificates(directory)
            password = secrets.token_hex(24)
            private_file(directory / "postgres.env", f"POSTGRES_USER=catalog\nPOSTGRES_DB=catalog_test\nPOSTGRES_PASSWORD={password}\n")
            pooler_inputs, postgres_inputs = directory / "pooler", directory / "postgres"
            pooler_inputs.mkdir(mode=0o700)
            postgres_inputs.mkdir(mode=0o700)
            for name in ("pooler.pem", "pooler.key", "ca.pem", "untrusted.pem"):
                private_file(pooler_inputs / name, (directory / name).read_text(encoding="utf-8"))
            for variant in ("expired", "future", "original"):
                for extension in ("pem", "key"):
                    name = f"pooler-{variant}.{extension}"
                    source = f"pooler.{extension}" if variant == "original" else name
                    private_file(pooler_inputs / name, (directory / source).read_text(encoding="utf-8"))
                private_file(pooler_inputs / f"{variant}.ini", configuration().replace(
                    "/lab/pooler.pem", f"/lab/pooler-{variant}.pem").replace(
                    "/lab/pooler.key", f"/lab/pooler-{variant}.key"))
            for name in ("postgres.pem", "postgres.key"):
                private_file(postgres_inputs / name, (directory / name).read_text(encoding="utf-8"))
            private_file(postgres_inputs / "replication.sql", f"CREATE ROLE catalog_replication LOGIN REPLICATION PASSWORD '{password}'; SELECT pg_reload_conf();")
            # Existing process fixture owns creation and grant reconciliation for
            # these test-only roles. It never adds the schema owner to the pooler.
            private_file(pooler_inputs / "userlist.txt", '"catalog_api" "catalog-api"\n"catalog_worker" "catalog-worker"\n')
            private_file(pooler_inputs / "pgbouncer.ini", configuration())
            private_file(pooler_inputs / "bad-ca.ini", configuration().replace("server_tls_ca_file = /lab/ca.pem", "server_tls_ca_file = /lab/untrusted.pem"))
            # Docker may allocate a different ephemeral published port on restart.
            # Select and verify fixed owned loopback ports for recovery assertions.
            with socket.socket() as pg_listener, socket.socket() as pool_listener, socket.socket() as standby_listener:
                pg_listener.bind(("127.0.0.1", 0))
                pool_listener.bind(("127.0.0.1", 0))
                standby_listener.bind(("127.0.0.1", 0))
                standby_port = standby_listener.getsockname()[1]
                pg_port, pool_port = pg_listener.getsockname()[1], pool_listener.getsockname()[1]
            compose = {
                "services": {
                    "postgres": {"image": POSTGRES_IMAGE, "env_file": [str(directory / "postgres.env")],
                        "ports": [f"127.0.0.1:{pg_port}:5432"], "networks": {"default": {"aliases": ["wrong-name", "writer"]}}, "volumes": ["data:/var/lib/postgresql", f"{postgres_inputs}:/lab:ro"],
                        "entrypoint": ["/bin/sh", "-ec", "cp /lab/postgres.key /tmp/server.key; cp /lab/postgres.pem /tmp/server.pem; chown postgres:postgres /tmp/server.key /tmp/server.pem; chmod 600 /tmp/server.key; exec /usr/local/bin/docker-entrypoint.sh postgres -c ssl=on -c ssl_cert_file=/tmp/server.pem -c ssl_key_file=/tmp/server.key"]},
                    "pooler": {"image": POOLER_IMAGE, "user": f"{os.getuid()}:{os.getgid()}",
                        "entrypoint": ["/usr/bin/pgbouncer"], "command": ["/lab/pgbouncer.ini"],
                        "read_only": True, "cap_drop": ["ALL"], "security_opt": ["no-new-privileges:true"],
                        "ports": [f"127.0.0.1:{pool_port}:6432"], "volumes": [f"{pooler_inputs}:/lab:ro"]}
                }, "volumes": {"data": {}}}
            compose["services"]["bad-ca"] = dict(compose["services"]["pooler"],
                command=["/lab/bad-ca.ini"], ports=["127.0.0.1::6432"])
            for variant in ("expired", "future", "old"):
                # Separate owned endpoints isolate validity faults from the
                # writer and keep successful neighbors available throughout.
                inputs = directory / f"{variant}-postgres"
                inputs.mkdir(mode=0o700)
                for extension in ("pem", "key"):
                    source = f"postgres.{extension}" if variant == "old" else f"postgres-{variant}.{extension}"
                    private_file(inputs / f"postgres.{extension}", (directory / source).read_text(encoding="utf-8"))
                compose["services"][f"{variant}-postgres"] = dict(compose["services"]["postgres"],
                    ports=["127.0.0.1::5432"], networks={"default": {}},
                    volumes=[f"{variant}-data:/var/lib/postgresql", f"{inputs}:/lab:ro"])
                compose["volumes"][f"{variant}-data"] = {}
                config_name = "original" if variant == "old" else variant
                compose["services"][f"{variant}-pooler"] = dict(compose["services"]["pooler"],
                    command=[f"/lab/{config_name}.ini"], ports=["127.0.0.1::6432"])
            # Reuse the maintained disposable physical standby definition. Render
            # only its file; no retained project is selected or contacted.
            standby = json.loads(command(["docker", "compose", "--env-file", "/dev/null", "-f", "compose.yaml", "-f", "test/functional/failover.compose.yaml", "--profile", "failover", "config", "--format", "json"]))["services"]["standby"]
            standby.pop("profiles", None)
            standby["ports"] = [f"127.0.0.1:{standby_port}:5432"]
            standby["networks"] = {"default": {"aliases": ["wrong-name"]}}
            standby["environment"] = {"PGPASSWORD": password, "PGDATA": "/var/lib/postgresql/18/docker", "CATALOG_FIXTURE_OWNER": project}
            standby["volumes"] = [{"type": "volume", "source": "standby-data", "target": "/var/lib/postgresql"}, {"type": "bind", "source": str(postgres_inputs), "target": "/lab", "read_only": True}]
            # pg_basebackup retains configuration/data; endpoint-specific TLS
            # files remain transient and private in the invocation's mount.
            standby["command"][0] = "cp /lab/postgres.key /tmp/server.key; cp /lab/postgres.pem /tmp/server.pem; chown postgres:postgres /tmp/server.*; chmod 600 /tmp/server.key; " + standby["command"][0].replace("exec postgres -c synchronous_standby_names=", "exec postgres -c ssl=on -c ssl_cert_file=/tmp/server.pem -c ssl_key_file=/tmp/server.key -c synchronous_standby_names=")
            standby["command"][0] = owned_standby_restart(standby["command"][0])
            compose["services"]["standby"] = standby
            compose["volumes"]["standby-data"] = {}
            private_file(directory / "compose.json", json.dumps(compose))
            report["configurationSHA256"] = hashlib.sha256(configuration().encode()).hexdigest()
            launcher = ["docker", "compose", "--env-file", "/dev/null", "-f", str(directory / "compose.json"), "-p", project]
            creation_attempted = True
            services = ("postgres", "pooler", "bad-ca", "expired-postgres", "future-postgres", "expired-pooler", "future-pooler", "old-postgres", "old-pooler")
            command([*launcher, "up", "-d", *services], timeout=90)
            # Authenticated TCP readiness avoids PostgreSQL's temporary init server.
            deadline = time.monotonic() + 60
            while time.monotonic() < deadline:
                try:
                    if all(command([*launcher, "exec", "-T", service, "/bin/sh", "-ec",
                                    'PGPASSWORD="$POSTGRES_PASSWORD" psql -h 127.0.0.1 -XAt -v ON_ERROR_STOP=1 -U catalog -d catalog_test -c "SELECT 1"']) == "1"
                           for service in ("postgres", "expired-postgres", "future-postgres", "old-postgres")):
                        break
                except RuntimeError:
                    pass
                time.sleep(0.2)
            else:
                raise RuntimeError("PostgreSQL authenticated startup deadline")
            command([*launcher, "exec", "-T", "old-postgres", "psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", "catalog", "-d", "catalog_test", "-c",
                     "CREATE ROLE catalog_api LOGIN NOINHERIT PASSWORD 'catalog-api'; CREATE ROLE catalog_worker LOGIN NOINHERIT PASSWORD 'catalog-worker';"])
            command([*launcher, "exec", "-T", "postgres", "/bin/sh", "-ec", 'cat >> "$PGDATA/pg_hba.conf" <<EOF\nhost replication catalog_replication all scram-sha-256\nEOF\ncp /lab/replication.sql /tmp/replication.sql; chown postgres:postgres /tmp/replication.sql; chmod 600 /tmp/replication.sql; PGPASSWORD="$POSTGRES_PASSWORD" psql -h 127.0.0.1 -XAt -v ON_ERROR_STOP=1 -U catalog -d catalog_test -f /tmp/replication.sql'])
            command([*launcher, "up", "-d", "standby"], timeout=90)
            deadline = time.monotonic() + 60
            while time.monotonic() < deadline:
                try:
                    if command([*launcher, "exec", "-T", "standby", "psql", "-XAt", "-U", "catalog", "-d", "catalog_test", "-c", "SELECT pg_is_in_recovery()"] ) == "t":
                        break
                except RuntimeError:
                    pass
                time.sleep(0.2)
            else:
                raise RuntimeError("PostgreSQL standby startup deadline")
            def address(service: str, port: str) -> str:
                value = command([*launcher, "port", service, port])
                if not value.startswith("127.0.0.1:") or not value.split(":")[1].isdigit():
                    raise RuntimeError("fixture listener must be loopback")
                return value
            pg_address, pool_address = address("postgres", "5432"), address("pooler", "6432")
            trust = f"sslmode=verify-full&sslrootcert={directory / 'ca.pem'}"
            env = dict(os.environ, GOWORK="off", GOFLAGS="-mod=readonly",
                CATALOG_TEST_DATABASE_DSN=f"postgres://catalog:{password}@{pg_address}/catalog_test?{trust}",
                CATALOG_TEST_PGBOUNCER_DSN=f"postgres://{pool_address}/catalog_test?{trust}",
                CATALOG_TEST_PGBOUNCER_PROMOTED_OWNER_DSN=f"postgres://catalog:{password}@{address('standby', '5432')}/catalog_test?{trust}",
                CATALOG_TEST_PGBOUNCER_BAD_CA_DSN=f"postgres://{address('bad-ca', '6432')}/catalog_test?{trust}",
                CATALOG_TEST_PGBOUNCER_PYTHON=os.environ.get("PYTHON", os.sys.executable),
                CATALOG_TEST_PGBOUNCER_WRITER_FILE=str(directory / "writer"),
                CATALOG_TEST_PGBOUNCER_WRITER_LOSS=selection,
                CATALOG_TEST_PGBOUNCER_EVIDENCE_FILE=str(directory / "writer-evidence.json"),
                CATALOG_TEST_PGBOUNCER_PROJECT=project,
                CATALOG_TEST_PGBOUNCER_COMPOSE=str(directory / "compose.json"))
            env["CATALOG_TEST_PGBOUNCER_TLS_DIRECTORY"] = str(directory)
            env["CATALOG_TEST_PGBOUNCER_TLS_EVIDENCE"] = str(directory / "tls-evidence.json")
            for role, port in (("POOLER", "6432"), ("POSTGRES", "5432")):
                env[f"CATALOG_TEST_PGBOUNCER_OLD_{role}_DSN"] = f"postgres://{address('old-' + role.lower(), port)}/catalog_test?{trust}"
            for variant in ("expired", "future"):
                env[f"CATALOG_TEST_PGBOUNCER_{variant.upper()}_DSN"] = f"postgres://{address(variant + '-pooler', '6432')}/catalog_test?{trust}"
                env[f"CATALOG_TEST_PGBOUNCER_{variant.upper()}_POSTGRES_DSN"] = f"postgres://{address(variant + '-postgres', '5432')}/catalog_test?{trust}"
            for service in (*services, "standby"):
                identifier = command([*launcher, "ps", "-q", service])
                report.setdefault("runningImageIDs", {})[service] = command(["docker", "inspect", "--format", "{{.Image}}", identifier])
            report["poolerVersion"] = command([*launcher, "exec", "-T", "pooler", "/usr/bin/pgbouncer", "--version"]).splitlines()[0]
            def failed_go_stdout(raw: bytes) -> None:
                # Keep bounded assertion diagnostics in private output; failed
                # children still fail through the shared supervisor's fixed code.
                text = raw.decode(errors="replace")
                for private in (password, "catalog-api", "catalog-worker", "incorrect-secret-sentinel", str(directory)):
                    text = text.replace(private, "[private]")
                private_file(report_dir / "failed-tests.jsonl", text)
            output = command(["go", "test", "-json", "-race", "-tags=integration", "./test/functional",
                "-run", "^TestPgBouncer$", "-count=1", "-timeout=10m"], timeout=660, env=env, failed_stdout=failed_go_stdout)
            if any(private in output for private in (password, "catalog-api", "catalog-worker", "incorrect-secret-sentinel")):
                raise RuntimeError("synthetic credential appeared in command output")
            private_file(report_dir / "tests.jsonl", output)
            report["checks"] = completed_cases(output)
            report["tlsRenewalEvidence"] = tls_evidence(directory)
            if selection == "abrupt":
                evidence = json.loads((directory / "writer-evidence.json").read_text(encoding="utf-8"))
                validate_abrupt_evidence(evidence)
                report["writerLossEvidence"] = evidence
            final_paths = command(["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"]).rstrip("\0").split("\0")
            if SHARED.digest_files(set(final_paths)) != report["worktreeDigest"]:
                raise RuntimeError("qualification source changed")

            report["status"] = "passed"
        except Exception as error:
            report["failure"] = str(error) if isinstance(error, RuntimeError) else type(error).__name__
            if (directory / "tls-evidence.json").exists():
                try:
                    report["tlsRenewalEvidence"] = tls_evidence(directory)
                except (OSError, ValueError):
                    report["tlsRenewalEvidenceIncomplete"] = True
            if (directory / "compose.json").exists():
                try:
                    logs = command([*launcher, "logs", "--no-color", "--tail", "100"])
                    for private in (password, "catalog-api", "catalog-worker", str(directory)):
                        logs = logs.replace(private, "[private]")
                    private_file(report_dir / "services.txt", logs)
                except (OSError, RuntimeError, subprocess.TimeoutExpired):
                    pass
        finally:
            for number in (signal.SIGINT, signal.SIGTERM):
                signal.signal(number, signal.SIG_IGN)
            # No project mutation is attempted before the absence preflight.
            # A collision never authorizes cleanup of someone else's resources.
            if not creation_attempted:
                report["cleanup"] = True
            if creation_attempted:
                launcher = ["docker", "compose", "--env-file", "/dev/null", "-f", str(directory / "compose.json"), "-p", project]
                try:
                    command([*launcher, "down", "--volumes", "--remove-orphans"], timeout=90)
                    remaining = command(["docker", "ps", "-aq", "--filter", f"label=com.docker.compose.project={project}"])
                    networks = command(["docker", "network", "ls", "-q", "--filter", f"label=com.docker.compose.project={project}"])
                    volumes = command(["docker", "volume", "ls", "-q", "--filter", f"label=com.docker.compose.project={project}"])
                    report["cleanup"] = not remaining and not networks and not volumes
                except (OSError, RuntimeError, subprocess.TimeoutExpired):
                    report["cleanup"] = False
    (report_dir / "report.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    print(f"PgBouncer qualification: {report['status']}; cleanup={report['cleanup']}; report={report_dir.relative_to(ROOT)}")
    return 0 if report["status"] == "passed" and report["cleanup"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
