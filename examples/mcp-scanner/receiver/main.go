// mcp-scan-receiver accepts signed notifications and scans explicitly mapped MCP definitions.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	sdk "github.com/open-agentic-catalog/open-agentic-catalog/sdk/go"
)

type options struct {
	command, origin, tenant, config, db, token, ca, signer, cert, key, listen, output, python, runner string
	capacity                                                                                          int
	retention, poll, reconcile                                                                        time.Duration
	loopback                                                                                          bool
}

func parseOptions(args []string) (options, error) {
	var o options
	if len(args) == 0 {
		return o, errors.New("select_receiver_command")
	}
	o.command = args[0]
	switch o.command {
	case "init", "serve", "reconcile", "process", "render":
	default:
		return o, errors.New("invalid_receiver_command")
	}
	f := flag.NewFlagSet("mcp-scan-receiver", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&o.origin, "origin", "", "Catalog API origin")
	f.StringVar(&o.tenant, "tenant", "", "tenant UUID")
	f.StringVar(&o.config, "config-file", "", "private endpoint mapping JSON file")
	f.StringVar(&o.db, "database-file", "", "private receiver database DSN file")
	f.StringVar(&o.token, "token-file", "", "private Catalog token file")
	f.StringVar(&o.ca, "catalog-ca-file", "", "Catalog private CA file")
	f.StringVar(&o.signer, "signer-file", "", "private webhook signer JSON file")
	f.StringVar(&o.cert, "tls-cert", "", "receiver certificate file")
	f.StringVar(&o.key, "tls-key", "", "private receiver key file")
	f.StringVar(&o.listen, "listen", "127.0.0.1:9444", "receiver HTTPS address")
	f.StringVar(&o.output, "reports-dir", "", "private derived report directory")
	f.StringVar(&o.python, "scanner-python", "", "absolute Python executable path")
	f.StringVar(&o.runner, "scanner-runner", "", "absolute offline runner path")
	f.IntVar(&o.capacity, "capacity", 1000, "maximum retained receipts and scans, separately")
	f.DurationVar(&o.retention, "retention", 24*time.Hour, "completed receipt and unused scan retention")
	f.DurationVar(&o.poll, "poll-interval", 5*time.Second, "pending-work cadence")
	f.DurationVar(&o.reconcile, "reconcile-interval", time.Minute, "periodic Catalog traversal")
	f.BoolVar(&o.loopback, "allow-loopback-http", false, "explicit local Catalog API HTTP, never MCP")
	if f.Parse(args[1:]) != nil || f.NArg() != 0 || !uuidPattern.MatchString(o.tenant) || o.config == "" || o.db == "" || o.capacity < 1 || o.capacity > 10000 || o.retention < time.Minute || o.retention > 30*24*time.Hour || o.poll < time.Second || o.poll > time.Minute || o.reconcile < 5*time.Second || o.reconcile > 24*time.Hour {
		return o, errors.New("invalid_receiver_inputs")
	}
	if _, _, err := net.SplitHostPort(o.listen); err != nil {
		return o, errors.New("invalid_listener")
	}
	if o.command != "init" && o.output == "" {
		return o, errors.New("reports_directory_required")
	}
	if o.command != "init" && o.command != "render" && o.token == "" {
		return o, errors.New("catalog_token_required")
	}
	if o.command == "serve" && (o.signer == "" || o.cert == "" || o.key == "") {
		return o, errors.New("receiver_tls_and_signer_required")
	}
	if o.command == "serve" || o.command == "process" {
		if !filepath.IsAbs(o.python) || !filepath.IsAbs(o.runner) {
			return o, errors.New("explicit_scanner_paths_required")
		}
	}
	c, err := sdk.New(sdk.Config{Origin: o.origin, AllowLoopbackHTTP: o.loopback})
	if err != nil {
		return o, errors.New("invalid_catalog_origin")
	}
	c.Close()
	return o, nil
}

func run(ctx context.Context, args []string) error {
	o, err := parseOptions(args)
	if err != nil {
		return err
	}
	config, digest, err := loadConfiguration(o.config)
	if err != nil || len(config.Resources) > o.capacity {
		return errors.New("invalid_mapping")
	}
	dsn, err := privateFile(o.db, 16384)
	if err != nil {
		return err
	}
	pg, err := pgxpool.ParseConfig(strings.TrimSpace(string(dsn)))
	if err != nil {
		return errors.New("invalid_database_input")
	}
	pg.MaxConns = 4
	pg.ConnConfig.ConnectTimeout = 5 * time.Second
	pool, err := pgxpool.NewWithConfig(ctx, pg)
	if err != nil {
		return errors.New("receiver_database_unavailable")
	}
	defer pool.Close()
	s := &postgresStore{pool: pool, capacity: o.capacity, retention: o.retention}
	setup, cancel := context.WithTimeout(ctx, 10*time.Second)
	if o.command == "init" {
		defer cancel()
		if s.init(setup, o.tenant, o.origin, digest) != nil {
			return errors.New("receiver_database_init_failed")
		}
		return nil
	}
	err = bindSettings(setup, pool, o.tenant, o.origin, digest)
	cancel()
	if err != nil {
		return errors.New("receiver_binding_failed")
	}
	if o.command == "render" {
		work, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		return s.render(work, config.Resources, o.output)
	}
	ca, err := roots(o.ca)
	if err != nil {
		return err
	}
	client, err := sdk.New(sdk.Config{Origin: o.origin, AllowLoopbackHTTP: o.loopback, RootCAs: ca, Timeout: 5 * time.Second, ResponseBytes: 1 << 20, TokenProvider: func(ctx context.Context) (string, error) { return readToken(ctx, o.token) }})
	if err != nil {
		return errors.New("catalog_client_unavailable")
	}
	defer client.Close()
	lockCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	guard, unlock, err := s.processor(lockCtx)
	stop()
	if err != nil {
		return errors.New("processor_unavailable")
	}
	defer unlock()
	a := &application{store: s, catalog: client, collector: &mcpCollector{catalogTokenFile: o.token, now: time.Now}, scanner: &pythonScanner{python: o.python, runner: o.runner}, tenant: o.tenant, mappings: config.Resources, signerFile: o.signer, now: time.Now, guard: guard, slots: make(chan struct{}, 16)}
	if o.command == "serve" || o.command == "process" {
		probe, done := context.WithTimeout(ctx, 35*time.Second)
		_, err = a.scanner.Identity(probe)
		done()
		if err != nil {
			return err
		}
	}
	if o.command != "serve" {
		budget := 30 * time.Second
		if o.command == "process" {
			budget = 3 * time.Minute
		}
		work, done := context.WithTimeout(ctx, budget)
		defer done()
		if o.command == "reconcile" {
			err = a.reconcile(work)
		} else {
			err = a.process(work)
		}
		if err != nil {
			return err
		}
		return s.render(work, config.Resources, o.output)
	}
	if _, err = loadSigner(o.signer); err != nil {
		return err
	}
	if _, err = privateFile(o.key, 16384); err != nil {
		return err
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", o.listen)
	if err != nil {
		return errors.New("receiver_listener_unavailable")
	}
	server := &http.Server{Handler: a, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	served := make(chan error, 1)
	go func() {
		served <- server.ServeTLS(&boundedListener{Listener: listener, slots: make(chan struct{}, 64)}, o.cert, o.key)
	}()
	defer func() {
		drain, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer stop()
		if server.Shutdown(drain) != nil {
			_ = server.Close()
		}
	}()
	ticker := time.NewTicker(o.poll)
	defer ticker.Stop()
	nextReconciliation := time.Time{}
	for {
		if !time.Now().Before(nextReconciliation) {
			work, done := context.WithTimeout(ctx, 30*time.Second)
			err = a.reconcile(work)
			done()
			nextReconciliation = time.Now().Add(o.reconcile)
			if errorCode(err) == "processor_lock_lost" {
				return err
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, errorCode(err))
			}
		}
		work, done := context.WithTimeout(ctx, 3*time.Minute)
		err = a.process(work)
		done()
		if errorCode(err) == "processor_lock_lost" {
			return err
		}
		if err != nil && ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, errorCode(err))
		}
		work, done = context.WithTimeout(ctx, 5*time.Second)
		err = s.render(work, config.Resources, o.output)
		done()
		if err != nil && ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, "report_render_failed")
		}
		select {
		case <-ctx.Done():
			return nil
		case err := <-served:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				return errors.New("receiver_listener_failed")
			}
			return nil
		case <-ticker.C:
		}
	}
}

type boundedListener struct {
	net.Listener
	slots chan struct{}
}
type boundedConn struct {
	net.Conn
	once    sync.Once
	release func()
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

func mainCode() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "MCP receiver command failed.")
		return 1
	}
	return 0
}

func main() { os.Exit(mainCode()) }
