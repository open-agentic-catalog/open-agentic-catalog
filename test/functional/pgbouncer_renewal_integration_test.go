//go:build integration

package functional_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/federation"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/jobs"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
)

type poolerBackendIdentity struct {
	PID             int    `json:"pid"`
	StartedAt       string `json:"startedAt"`
	PostmasterStart string `json:"postmasterStart"`
	Role            string `json:"role"`
	TLS             bool   `json:"tls"`
}

type poolerRenewalPhase struct {
	Name            string                `json:"name"`
	Container       string                `json:"poolerContainer"`
	PoolerLeaf      string                `json:"poolerLeaf"`
	PostgresLeaf    string                `json:"postgresLeaf"`
	PostmasterStart string                `json:"postmasterStart"`
	APIInstance     string                `json:"apiInstance"`
	WorkerInstance  string                `json:"workerInstance"`
	JobID           string                `json:"jobID"`
	JobState        jobs.State            `json:"jobState"`
	APIBackend      poolerBackendIdentity `json:"apiBackend"`
	WorkerBackend   poolerBackendIdentity `json:"workerBackend"`
}

func qualifyPoolerTLSRenewal(t *testing.T, f processFixture, cfg config.Config, peerURL, namespaceID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	directory := os.Getenv("CATALOG_TEST_PGBOUNCER_TLS_DIRECTORY")
	trustFile := filepath.Join(directory, "runtime-ca.pem")
	oldTrust, err := os.ReadFile(filepath.Join(directory, "ca.pem"))  // #nosec G304,G703 -- fixed public CA in the explicitly owned fixture directory.
	if err != nil || os.WriteFile(trustFile, oldTrust, 0600) != nil { // #nosec G703 -- fixed owned public trust input, never a request path.
		t.Fatal("prepare runtime trust input")
	}
	leaves := make(map[string]*x509.Certificate)
	for _, role := range []string{"pooler", "postgres"} {
		for _, suffix := range []string{"", "-renewed", "-next"} {
			pair, err := tls.LoadX509KeyPair(filepath.Join(directory, role+suffix+".pem"), filepath.Join(directory, role+suffix+".key"))
			if err != nil || len(pair.Certificate) != 1 {
				t.Fatal("load exact renewal specimen")
			}
			leaf, err := x509.ParseCertificate(pair.Certificate[0])
			if err != nil {
				t.Fatal("parse renewal specimen")
			}
			leaves[role+suffix] = leaf
		}
	}
	transition := func(t *testing.T, action string) string {
		t.Helper()
		qualified := f
		qualified.t = t
		command := exec.CommandContext(ctx, os.Getenv("CATALOG_TEST_PGBOUNCER_PYTHON"), "-B", filepath.Join("..", "pgbouncer", "tls_rotation.py"), action) // #nosec G204,G702 -- fixed owned operator fixture, finite actions selected below.
		command.Env = os.Environ()
		var result struct {
			Phase     string `json:"phase"`
			Container string `json:"poolerContainer"`
		}
		decodeProcessJSON(t, qualified.run(ctx, command, 0), &result)
		if result.Phase != action || len(result.Container) != 64 {
			t.Fatal("TLS operator returned incomplete activation evidence")
		}
		return result.Container
	}
	restored := false
	// Child process cleanup runs first. Restore only this fixture's original,
	// unrevoked inputs so later protocol controls keep their initial contract.
	t.Cleanup(func() {
		if restored {
			return
		}
		cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		command := exec.CommandContext(cleanup, os.Getenv("CATALOG_TEST_PGBOUNCER_PYTHON"), "-B", filepath.Join("..", "pgbouncer", "tls_rotation.py"), "restore") // #nosec G204,G702 -- selected invocation-owned fixture, fixed restoration only.
		if err := command.Run(); err != nil {
			t.Error("restore owned TLS baseline after failure")
		}
	})
	proofs := make(map[string]any)
	phases := make([]poolerRenewalPhase, 0, 5)
	var sourceID string
	previousLeaf := ""
	var previous poolerRenewalPhase
	for _, phase := range []string{"baseline", "same-ca", "overlap", "successor", "retired"} {
		if !t.Run(phase, func(t *testing.T) {
			qualified := f
			qualified.t = t
			suffix := "-next"
			if phase == "baseline" {
				suffix = ""
			} else if phase == "same-ca" || phase == "overlap" {
				suffix = "-renewed"
			}
			ownerDSN := renewalDSN(t, os.Getenv("CATALOG_TEST_PGBOUNCER_PROMOTED_OWNER_DSN"), trustFile)
			container := poolerContainerID(t, ctx)
			if phase != "baseline" {
				if phase == "same-ca" || phase == "successor" {
					transition(t, "stage-"+phase)
					assertRenewalPeer(t, ctx, renewalConfig(t, os.Getenv("CATALOG_TEST_PGBOUNCER_DSN"), trustFile, "catalog_api"), leaves["pooler"+previousLeaf])
					assertRenewalPeer(t, ctx, renewalConfig(t, ownerDSN, trustFile, ""), leaves["postgres"+previousLeaf])
					proofs[map[string]string{"same-ca": "sameCAFileOnlyNoActivation", "successor": "successorFileOnlyNoActivation"}[phase]] = true
				}
				poolerCompose(t, ctx, "stop", "pooler")
				awaitRenewalDrain(t, ctx, ownerDSN)
				container = transition(t, phase)
				if container == previous.Container {
					t.Fatal("pooler consumer was not replaced")
				}
			}
			active := poolerRenewalPhase{Name: phase, Container: container}
			active.PoolerLeaf = assertRenewalPeer(t, ctx, renewalConfig(t, os.Getenv("CATALOG_TEST_PGBOUNCER_DSN"), trustFile, "catalog_api"), leaves["pooler"+suffix])
			active.PostgresLeaf = assertRenewalPeer(t, ctx, renewalConfig(t, ownerDSN, trustFile, ""), leaves["postgres"+suffix])
			owner := renewalSQL(t, ctx, renewalConfig(t, ownerDSN, trustFile, ""), leaves["postgres"+suffix], "catalog", false)
			active.PostmasterStart = owner.PostmasterStart
			restartExpected := phase == "same-ca" || phase == "successor"
			if phase != "baseline" && restartExpected != (owner.PostmasterStart != previous.PostmasterStart) {
				t.Fatal("writer activation differs from the selected certificate transition")
			}
			for _, role := range []string{"api", "worker"} {
				dsn := renewalDSN(t, os.Getenv("CATALOG_TEST_PGBOUNCER_DSN"), trustFile)
				value := renewalSQL(t, ctx, poolerTLSConfig(t, dsn, "catalog_"+role), leaves["pooler"+suffix], "catalog_"+role, true)
				if value.PostmasterStart != owner.PostmasterStart || phase != "baseline" && (role == "api" && value == previous.APIBackend || role == "worker" && value == previous.WorkerBackend) {
					t.Fatal("runtime session did not reach a fresh backend on the verified writer")
				}
				if role == "api" {
					active.APIBackend = value
				} else {
					active.WorkerBackend = value
				}
			}
			apiCfg := cfg
			apiCfg.HTTP.Address = processAddress(ctx, t)
			apiCfg.HTTP.PublicBaseURL = "http://" + apiCfg.HTTP.Address
			workerCfg := apiCfg
			workerCfg.Worker.OperationsAddress = processAddress(ctx, t)
			stopAPI := qualified.start(ctx, "api", renewalRoleDSN(t, trustFile, "catalog_api"), qualified.configuration("tls-"+phase+"-api", apiCfg), apiCfg.HTTP.Address)
			stopWorker := qualified.start(ctx, "worker", renewalRoleDSN(t, trustFile, "catalog_worker"), qualified.configuration("tls-"+phase+"-worker", workerCfg), workerCfg.Worker.OperationsAddress)
			var namespace catalog.Namespace
			decodeProcessJSON(t, qualified.request(ctx, apiCfg.HTTP.Address, "GET", "/api/v1/namespaces/"+namespaceID, "local-admin", "", 200), &namespace)
			if namespace.ID != namespaceID {
				t.Fatal("renewed process lost its useful namespace read")
			}
			active.APIInstance = renewalInstance(t, qualified.request(ctx, apiCfg.HTTP.Address, "GET", "/api/v1/service-information", "local-admin", "", 200))
			active.WorkerInstance = renewalInstance(t, qualified.request(ctx, workerCfg.Worker.OperationsAddress, "GET", "/status", "", "", 200))
			if sourceID == "" {
				var inspection federation.Inspection
				decodeProcessJSON(t, qualified.request(ctx, apiCfg.HTTP.Address, "POST", "/api/v1/federation/source-inspections", "local-admin", `{"url":"`+peerURL+`/tls-renewal","networkProfile":"process-settings"}`, 201, map[string]string{"Idempotency-Key": "tls-renewal-inspection"}), &inspection)
				var source federation.Source
				decodeProcessJSON(t, qualified.request(ctx, apiCfg.HTTP.Address, "POST", "/api/v1/federation/sources", "local-admin", `{"inspectionId":"`+inspection.ID+`","displayName":"TLS renewal","retrievalMode":"indexed","updateMode":"manual_review","scope":{"kinds":["agent"]},"networkProfile":"process-settings","reason":"Owned database TLS qualification"}`, 201, map[string]string{"Idempotency-Key": "tls-renewal-source"}), &source)
				sourceID = source.ID
			}
			active.JobID, active.JobState = renewalJob(t, ctx, qualified, apiCfg.HTTP.Address, sourceID, phase, ownerDSN)
			if phase == "successor" {
				assertRenewalPeer(t, ctx, renewalConfig(t, os.Getenv("CATALOG_TEST_PGBOUNCER_OLD_POOLER_DSN"), trustFile, "catalog_api"), leaves["pooler"])
				assertRenewalPeer(t, ctx, renewalConfig(t, os.Getenv("CATALOG_TEST_PGBOUNCER_OLD_POSTGRES_DSN"), trustFile, "catalog_api"), leaves["postgres"])
				renewalOldUpstream(t, ctx, trustFile, false)
				proofs["overlapOldClientAccepted"], proofs["overlapOldUpstreamAccepted"] = true, true
			}
			if phase == "retired" {
				for _, role := range []string{"pooler", "postgres"} {
					dsn := os.Getenv("CATALOG_TEST_PGBOUNCER_OLD_" + strings.ToUpper(role) + "_DSN")
					// An isolated old-root control proves the peer stays alive and
					// still serves its valid old leaf after consumer trust retires.
					assertRenewalPeer(t, ctx, poolerTLSConfig(t, dsn, "catalog_api"), leaves[role])
					_, err := renewalTLS(ctx, renewalConfig(t, dsn, trustFile, "catalog_api"))
					var unknown x509.UnknownAuthorityError
					if !errors.As(err, &unknown) || unknown.Cert == nil || !bytes.Equal(unknown.Cert.Raw, leaves[role].Raw) {
						t.Fatal("retired consumer did not reject the exact valid old server chain")
					}
				}
				renewalOldUpstream(t, ctx, trustFile, true)
				proofs["retiredClientRefused"], proofs["retiredUpstreamRefused"] = true, true
			}
			stopAPI()
			stopWorker()
			phases = append(phases, active)
			previous, previousLeaf = active, suffix
		}) {
			t.Fatal("database TLS phase did not complete")
		}
	}
	transition(t, "restore")
	assertRenewalPeer(t, ctx, poolerTLSConfig(t, os.Getenv("CATALOG_TEST_PGBOUNCER_DSN"), "catalog_api"), leaves["pooler"])
	assertRenewalPeer(t, ctx, poolerTLSConfig(t, os.Getenv("CATALOG_TEST_PGBOUNCER_PROMOTED_OWNER_DSN"), ""), leaves["postgres"])
	qualifyPoolerTLSNeighbor(t, ctx, leaves["pooler"])
	restored = true
	proofs["originalInputsRestored"] = true
	proofs["phases"] = phases
	raw, err := json.Marshal(proofs)
	if err != nil || os.WriteFile(os.Getenv("CATALOG_TEST_PGBOUNCER_TLS_EVIDENCE"), raw, 0600) != nil { // #nosec G703 -- exact invocation-owned evidence file, no request input.
		t.Fatal("write bounded TLS renewal evidence")
	}
}

func renewalDSN(t *testing.T, dsn, trust string) string {
	t.Helper()
	value, err := url.Parse(dsn)
	if err != nil || value.Hostname() != "127.0.0.1" {
		t.Fatal("renewal requires the owned loopback database route")
	}
	query := value.Query()
	query.Set("sslrootcert", trust)
	query.Set("sslmode", "verify-full")
	value.RawQuery = query.Encode()
	return value.String()
}

func renewalRoleDSN(t *testing.T, trust, role string) string {
	t.Helper()
	cfg := poolerTLSConfig(t, renewalDSN(t, os.Getenv("CATALOG_TEST_PGBOUNCER_DSN"), trust), role)
	value, err := url.Parse(cfg.ConnString())
	if err != nil {
		t.Fatal("parse owned role route")
	}
	value.User = url.UserPassword(role, strings.Replace(role, "_", "-", 1))
	return value.String()
}

func renewalConfig(t *testing.T, dsn, trust, role string) *pgx.ConnConfig {
	t.Helper()
	return poolerTLSConfig(t, renewalDSN(t, dsn, trust), role)
}

func renewalTLS(ctx context.Context, cfg *pgx.ConnConfig) (tls.ConnectionState, error) {
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port))))
	if err != nil {
		return tls.ConnectionState{}, err
	}
	defer func() { _ = connection.Close() }()
	deadline := time.Now().Add(3 * time.Second)
	if caller, ok := ctx.Deadline(); ok && caller.Before(deadline) {
		deadline = caller
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return tls.ConnectionState{}, err
	}
	packet, err := (&pgproto3.SSLRequest{}).Encode(nil)
	if err != nil {
		return tls.ConnectionState{}, err
	}
	if _, err := connection.Write(packet); err != nil {
		return tls.ConnectionState{}, err
	}
	var reply [1]byte
	if _, err := io.ReadFull(connection, reply[:]); err != nil || reply[0] != 'S' {
		return tls.ConnectionState{}, errors.New("owned endpoint did not accept PostgreSQL TLS")
	}
	client := tls.Client(connection, cfg.TLSConfig.Clone())
	err = client.HandshakeContext(ctx)
	return client.ConnectionState(), err
}

func assertRenewalPeer(t *testing.T, ctx context.Context, cfg *pgx.ConnConfig, expected *x509.Certificate) string {
	t.Helper()
	state, err := renewalTLS(ctx, cfg)
	if err != nil || len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 || !bytes.Equal(state.PeerCertificates[0].Raw, expected.Raw) {
		t.Fatal("fresh TLS peer does not serve the selected verified leaf")
	}
	value := sha256.Sum256(expected.Raw)
	return "sha256:" + hex.EncodeToString(value[:])
}

func renewalSQL(t *testing.T, ctx context.Context, cfg *pgx.ConnConfig, expected *x509.Certificate, role string, pooled bool) poolerBackendIdentity {
	t.Helper()
	cfg.TLSConfig.VerifyConnection = func(state tls.ConnectionState) error {
		if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 || !bytes.Equal(state.PeerCertificates[0].Raw, expected.Raw) {
			return errors.New("unexpected verified database identity")
		}
		return nil
	}
	connection, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal("connect exact renewal SQL role")
	}
	defer func() { _ = connection.Close(context.WithoutCancel(ctx)) }()
	var result poolerBackendIdentity
	var backend, postmaster time.Time
	var recovery bool
	err = connection.QueryRow(ctx, `SELECT current_user,pg_backend_pid(),a.backend_start,pg_postmaster_start_time(),s.ssl,pg_is_in_recovery() FROM pg_stat_activity a JOIN pg_stat_ssl s USING(pid) WHERE a.pid=pg_backend_pid()`).Scan(&result.Role, &result.PID, &backend, &postmaster, &result.TLS, &recovery)
	mode := connection.PgConn().ParameterStatus("pgbouncer.pool_mode")
	if err != nil || result.Role != role || !result.TLS || recovery || pooled && mode != "session" || !pooled && mode != "" {
		t.Fatal("renewal SQL lost exact role, writer, TLS or routing")
	}
	result.StartedAt, result.PostmasterStart = backend.UTC().Format(time.RFC3339Nano), postmaster.UTC().Format(time.RFC3339Nano)
	return result
}

func poolerContainerID(t *testing.T, ctx context.Context) string {
	t.Helper()
	command := exec.CommandContext(ctx, "docker", "compose", "--env-file", "/dev/null", "-f", os.Getenv("CATALOG_TEST_PGBOUNCER_COMPOSE"), "-p", os.Getenv("CATALOG_TEST_PGBOUNCER_PROJECT"), "ps", "--all", "-q", "pooler") // #nosec G204,G702 -- explicit selected project and fixed service.
	raw, err := command.Output()
	if err != nil || len(bytes.TrimSpace(raw)) != 64 {
		t.Fatal("read exact owned pooler identity")
	}
	return strings.TrimSpace(string(raw))
}

func awaitRenewalDrain(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	connection, err := pgx.ConnectConfig(ctx, poolerTLSConfig(t, dsn, ""))
	if err != nil {
		t.Fatal("open direct drain observer")
	}
	defer func() { _ = connection.Close(context.WithoutCancel(ctx)) }()
	deadline, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	for {
		var count int
		if err := connection.QueryRow(deadline, "SELECT count(*) FROM pg_stat_activity WHERE usename IN ('catalog_api','catalog_worker')").Scan(&count); err != nil {
			t.Fatal("observe old runtime backend drain")
		}
		if count == 0 {
			return
		}
		select {
		case <-deadline.Done():
			t.Fatal("old pooler backends did not drain")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func renewalInstance(t *testing.T, raw []byte) string {
	t.Helper()
	var value struct {
		ID string `json:"instanceId"`
	}
	decodeProcessJSON(t, raw, &value)
	if value.ID == "" {
		t.Fatal("missing renewed process identity")
	}
	return value.ID
}

func renewalJob(t *testing.T, ctx context.Context, f processFixture, address, sourceID, phase, ownerDSN string) (string, jobs.State) {
	t.Helper()
	var source federation.Source
	decodeProcessJSON(t, f.request(ctx, address, "GET", "/api/v1/federation/sources/"+sourceID, "local-admin", "", 200), &source)
	var accepted struct {
		Job jobs.Job `json:"job"`
	}
	decodeProcessJSON(t, f.request(ctx, address, "POST", "/api/v1/federation/sources/"+sourceID+":sync", "local-admin", `{}`, 202, map[string]string{"Idempotency-Key": "tls-renewal-sync-" + phase, "If-Match": `"rev-` + strconv.FormatInt(source.Revision, 10) + `"`}), &accepted)
	deadline, stop := context.WithTimeout(ctx, 15*time.Second)
	defer stop()
	for {
		var current jobs.Job
		decodeProcessJSON(t, f.request(deadline, address, "GET", "/api/v1/jobs/"+accepted.Job.ID, "local-admin", "", 200), &current)
		if current.ID == "" || current.ID != accepted.Job.ID {
			t.Fatal("worker progress readback changed the accepted Job identity")
		}
		if current.State == jobs.StateSucceeded {
			connection, err := pgx.ConnectConfig(deadline, poolerTLSConfig(t, ownerDSN, ""))
			if err != nil {
				t.Fatal("open direct owner Job observer")
			}
			var retained string
			err = connection.QueryRow(deadline, "SELECT state FROM jobs WHERE tenant_id=$1 AND id=$2", f.tenantID, current.ID).Scan(&retained)
			_ = connection.Close(context.WithoutCancel(ctx))
			if err != nil || retained != string(jobs.StateSucceeded) {
				t.Fatal("completed worker Job differs from its retained state")
			}
			return current.ID, current.State
		}
		if current.State != jobs.StateQueued && current.State != jobs.StateRunning {
			t.Fatal("renewal Job ended without completing useful worker work")
		}
		select {
		case <-deadline.Done():
			t.Fatal("renewal worker progress deadline expired")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func renewalOldUpstream(t *testing.T, ctx context.Context, trust string, refused bool) {
	t.Helper()
	value, err := url.Parse(renewalRoleDSN(t, trust, "catalog_api"))
	if err != nil {
		t.Fatal("parse old-chain control route")
	}
	value.Path = "/oldchain"
	started := time.Now()
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	connection, err := pgx.Connect(probe, value.String())
	if err == nil {
		var answer int
		err = connection.QueryRow(probe, "SELECT 1").Scan(&answer)
		_ = connection.Close(context.WithoutCancel(ctx))
		if err == nil && answer != 1 {
			t.Fatal("old-chain neighbor returned an unexpected value")
		}
	}
	if !refused {
		if err != nil {
			t.Fatal("overlap did not admit a fresh old-chain backend")
		}
		return
	}
	var postgres *pgconn.PgError
	if !errors.As(err, &postgres) || postgres.Message != "query_wait_timeout" || probe.Err() != nil {
		observed := time.Now()
		adjustment := observed.UnixMilli() - started.UnixMilli() - observed.Sub(started).Milliseconds()
		t.Fatalf("retired upstream trust refusal: queueError=%t deadline=%t clockAdjustmentMillis=%d", postgres != nil && postgres.Message == "query_wait_timeout", probe.Err() != nil, adjustment)
	}
	logs := poolerLogsSince(t, ctx, "pooler", started.UTC().Format(time.RFC3339Nano))
	if !strings.Contains(logs, "TLS handshake error") || !strings.Contains(logs, "certificate verify failed") {
		t.Fatal("retired upstream refusal lacks fresh TLS verification evidence")
	}
}
