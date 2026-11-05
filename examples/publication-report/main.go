// publication-report accepts signed Catalog notifications and saves current-state observations.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	sdk "github.com/open-agentic-catalog/open-agentic-catalog/sdk/go"
)

type options struct {
	command, origin, tenant, host, db, token, signer, cert, key, ca, listen, output string
	capacity                                                                        int
	retention, poll, reconciliation                                                 time.Duration
	loopback                                                                        bool
}

func parseOptions(args []string) (options, error) {
	var o options
	if len(args) == 0 {
		return o, errors.New("select_init_serve_reconcile_or_render")
	}
	o.command = args[0]
	if o.command != "init" && o.command != "serve" && o.command != "reconcile" && o.command != "render" {
		return o, errors.New("invalid_command")
	}
	f := flag.NewFlagSet("publication-report", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&o.origin, "origin", "", "Catalog API origin")
	f.StringVar(&o.tenant, "tenant", "", "Tenant UUID")
	f.StringVar(&o.host, "host", "", "exact publication host")
	f.StringVar(&o.db, "database-file", "", "private receiver DSN file")
	f.StringVar(&o.token, "token-file", "", "private API token file")
	f.StringVar(&o.signer, "signer-file", "", "private JSON secret/version file")
	f.StringVar(&o.cert, "tls-cert", "", "receiver certificate chain")
	f.StringVar(&o.key, "tls-key", "", "private receiver TLS key")
	f.StringVar(&o.ca, "ca-file", "", "Catalog private CA PEM")
	f.StringVar(&o.listen, "listen", "127.0.0.1:9443", "receiver TLS address")
	f.StringVar(&o.output, "output", "publication-report.json", "local report file")
	f.IntVar(&o.capacity, "capacity", 1000, "maximum retained receipts")
	f.DurationVar(&o.retention, "retention", 24*time.Hour, "completed receipt retention")
	f.DurationVar(&o.poll, "poll-interval", 5*time.Second, "one-work-item cadence")
	f.DurationVar(&o.reconciliation, "reconcile-interval", time.Minute, "unconditional current-state reconciliation cadence")
	f.BoolVar(&o.loopback, "allow-loopback-http", false, "explicit local API HTTP")
	if f.Parse(args[1:]) != nil || f.NArg() != 0 || !uuidPattern.MatchString(o.tenant) || o.tenant != strings.ToLower(o.tenant) || !validHost(o.host) || o.db == "" || o.capacity < 1 || o.capacity > 10000 || o.retention < time.Minute || o.retention > 30*24*time.Hour || o.poll < time.Second || o.poll > time.Minute || o.reconciliation < 5*time.Second || o.reconciliation > 24*time.Hour {
		return o, errors.New("invalid_inputs")
	}
	if _, _, err := net.SplitHostPort(o.listen); err != nil {
		return o, errors.New("invalid_listener")
	}
	if o.command == "serve" && (o.signer == "" || o.cert == "" || o.key == "") {
		return o, errors.New("receiver_TLS_and_signer_required")
	}
	if (o.command == "serve" || o.command == "reconcile") && o.token == "" {
		return o, errors.New("API_token_file_required")
	}
	client, err := sdk.New(sdk.Config{Origin: o.origin, AllowLoopbackHTTP: o.loopback})
	if err != nil {
		return o, errors.New("invalid_Catalog_inputs")
	}
	client.Close()
	return o, nil
}

func validHost(host string) bool {
	if host == "" || len(host) > 253 || host != strings.ToLower(host) {
		return false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.String() == host
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func privateFile(path string, limit int64) ([]byte, error) {
	// #nosec G703 -- The operator explicitly selects this local credential file, never an HTTP input.
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("private_input_unavailable")
	}
	// #nosec G304 G703 -- Read only the operator-selected private credential file checked above and below.
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("private_input_unavailable")
	}
	defer func() { _ = f.Close() }()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("private_input_unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || len(raw) == 0 || int64(len(raw)) > limit {
		return nil, errors.New("private_input_unavailable")
	}
	return raw, nil
}

func readToken(ctx context.Context, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	raw, err := privateFile(path, 16384)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "", errors.New("empty_token")
	}
	return value, nil
}

func run(ctx context.Context, args []string) error {
	o, err := parseOptions(args)
	if err != nil {
		return err
	}
	dsn, err := privateFile(o.db, 16384)
	if err != nil {
		return err
	}
	cfg, err := pgxpool.ParseConfig(strings.TrimSpace(string(dsn)))
	if err != nil {
		return errors.New("invalid_database_input")
	}
	cfg.MaxConns = 4
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return errors.New("database_unavailable")
	}
	defer pool.Close()
	s := &postgresStore{pool: pool, capacity: o.capacity, retention: o.retention}
	setup, cancel := context.WithTimeout(ctx, 10*time.Second)
	if o.command == "init" {
		defer cancel()
		if s.init(setup, o.tenant, o.host, o.origin) != nil {
			return errors.New("receiver_database_init_failed")
		}
		return nil
	}
	err = s.bind(setup, o.tenant, o.host, o.origin)
	cancel()
	if err != nil {
		return errors.New("receiver_database_binding_failed")
	}
	a := &application{store: s, tenant: o.tenant, host: o.host, signerFile: o.signer, output: o.output, now: time.Now, slots: make(chan struct{}, 16)}
	if o.command == "render" {
		work, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		return a.render(work)
	}
	var roots *x509.CertPool
	if o.ca != "" {
		pem, err := os.ReadFile(o.ca)
		if err != nil {
			return errors.New("invalid_CA_file")
		}
		roots, err = x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return errors.New("invalid_CA_file")
		}
	}
	client, err := sdk.New(sdk.Config{Origin: o.origin, AllowLoopbackHTTP: o.loopback, RootCAs: roots, Timeout: 5 * time.Second, ResponseBytes: 65536, TokenProvider: func(ctx context.Context) (string, error) {
		return readToken(ctx, o.token)
	}})
	if err != nil {
		return errors.New("invalid_Catalog_inputs")
	}
	defer client.Close()
	a.catalog = client
	lockCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	unlock, err := s.processor(lockCtx)
	stop()
	if err != nil {
		return errors.New("processor_unavailable")
	}
	defer unlock()
	if o.command == "reconcile" {
		work, stop := context.WithTimeout(ctx, 12*time.Second)
		defer stop()
		err := a.reconcile(work)
		renderErr := a.render(work)
		if err != nil {
			return err
		}
		return renderErr
	}
	if _, err := loadSigner(o.signer); err != nil {
		return err
	}
	if _, err := privateFile(o.key, 16384); err != nil {
		return err
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", o.listen)
	if err != nil {
		return errors.New("listener_unavailable")
	}
	server := &http.Server{Handler: a, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	served := make(chan error, 1)
	go func() {
		served <- server.ServeTLS(&boundedListener{Listener: listener, slots: make(chan struct{}, 64)}, o.cert, o.key)
	}()
	tick := time.NewTicker(o.poll)
	defer tick.Stop()
	nextReconcile := time.Time{}
	defer func() {
		drain, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer stop()
		if err := server.Shutdown(drain); err != nil {
			_ = server.Close()
		}
	}()
	for {
		if !time.Now().Before(nextReconcile) {
			work, stop := context.WithTimeout(ctx, 12*time.Second)
			err = a.reconcile(work)
			stop()
			nextReconcile = time.Now().Add(o.reconciliation)
			if err != nil {
				fmt.Fprintln(os.Stderr, err.Error())
			}
		}
		work, stop := context.WithTimeout(ctx, 12*time.Second)
		if err := a.process(work); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
		}
		stop()
		work, stop = context.WithTimeout(ctx, 5*time.Second)
		if err = a.render(work); err != nil {
			fmt.Fprintln(os.Stderr, "report_render_failed")
		}
		stop()
		select {
		case <-ctx.Done():
			return nil
		case err := <-served:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				return errors.New("receiver_listener_failed")
			}
			return nil
		case <-tick.C:
		}
	}
}

type boundedListener struct {
	net.Listener
	slots chan struct{}
}
type boundedConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *boundedConn) Close() error { err := c.Conn.Close(); c.once.Do(c.release); return err }
func (l *boundedListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.slots <- struct{}{}:
			return &boundedConn{Conn: c, release: func() { <-l.slots }}, nil
		default:
			_ = c.Close()
		}
	}
}

func main() {
	os.Exit(mainCode())
}

func mainCode() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	return 0
}
