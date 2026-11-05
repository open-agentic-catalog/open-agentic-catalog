// catalog-ui serves the optional browser client and its fixed authentication proxy.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/catalog-ui/internal/web"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/buildinfo"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/diagnostics"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
	_ "golang.org/x/crypto/x509roots/fallback"
)

var (
	version     = "dev"
	commit      = "unknown"
	sourceState = "unknown"
)

// main selects the loopback probe or configured host and reports sanitized failures.
func main() {
	diagnostics.ProtectStandardLog("catalog-ui")
	if err := runArgs(os.Args[1:]); err != nil && (!errors.Is(err, flag.ErrHelp) || errors.Is(err, safeerr.ErrPanic)) {
		diagnostics.Report(os.Stderr, "catalog-ui", "INTERNAL", err, true)
		os.Exit(1)
	}
}

// runArgs contains entry-point panics after normal deferred cleanup.
func runArgs(arguments []string) (err error) {
	defer safeerr.Recover(&err)
	if len(arguments) == 1 && (arguments[0] == "version" || arguments[0] == "--version") {
		return json.NewEncoder(os.Stdout).Encode(buildinfo.New(version, commit, sourceState))
	}
	flags := diagnostics.Flags("catalog-ui")
	filename := flags.String("config", "", "UI JSON configuration file")
	probe := flags.Bool("probe", false, "Check UI readiness on loopback port 8080")
	if err := diagnostics.Parse(flags, arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return safeerr.New("unexpected arguments; use catalog-ui --help")
	}
	if *probe {
		return web.ProbeReadiness(context.Background())
	}
	return run(*filename)
}

// run validates the host before listening and drains admitted work on termination.
func run(filename string) error {
	return runHost(filename, func(server *http.Server, drain func()) error {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return serve(ctx, server, drain)
	})
}

// runHost validates local inputs and owns host cleanup across the listener handoff.
func runHost(filename string, listen func(*http.Server, func()) error) (err error) {
	configuration, err := web.LoadConfig(filename)
	if err != nil {
		return safeerr.At("configuration", err)
	}
	process := diagnostics.Process{Service: "catalog-ui", Role: "ui", Environment: configuration.Environment, Version: version}
	defer func() { err = diagnostics.WithFormat(err, configuration.LogFormat, process) }()
	defer safeerr.Recover(&err)
	logger := process.Logger(diagnostics.NewLogger(os.Stdout, configuration.LogLevel, configuration.LogFormat))
	app, err := web.New(configuration, logger, buildinfo.New(version, commit, sourceState))
	if err != nil {
		return safeerr.At("host_initialization", err)
	}
	defer app.Close()
	diagnostics.Startup(logger, commit, configuration.LogLevel, configuration.LogFormat, false)
	server := &http.Server{Addr: configuration.Listen, Handler: diagnostics.RecoverHTTP(logger, app), ErrorLog: diagnostics.HTTPErrorLog(logger), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: time.Duration(configuration.TimeoutSeconds) * time.Second, WriteTimeout: time.Duration(configuration.TimeoutSeconds+5) * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	err = listen(server, app.Drain)
	if err == nil {
		logger.Info("UI host stopped", "event", "process.stopped", "component", "ui")
	}
	return safeerr.At("listener", err)
}

// uiListener is the command's HTTP lifecycle boundary, implemented by http.Server.
type uiListener interface {
	ListenAndServe() error
	Shutdown(context.Context) error
	Close() error
}

// serve marks the host unready before bounded shutdown and hides listener causes.
func serve(ctx context.Context, server uiListener, drain func()) error {
	result := make(chan error, 1)
	go func() {
		var err error
		defer func() { result <- err }()
		defer safeerr.Recover(&err)
		err = server.ListenAndServe()
	}()
	select {
	case err := <-result:
		if errors.Is(err, safeerr.ErrPanic) {
			drain()
			shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			defer cancel()
			defer func() { _ = server.Close() }()
			return errors.Join(err, server.Shutdown(shutdown))
		}
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return safeerr.New("UI listener failed")
	case <-ctx.Done():
		drain()
		// Shutdown must outlive the canceled signal context.
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		var shutdownErr error
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			shutdownErr = safeerr.New("UI shutdown timed out")
		}
		select {
		case err := <-result:
			if errors.Is(err, safeerr.ErrPanic) {
				return errors.Join(err, shutdownErr)
			}
			return shutdownErr
		case <-shutdown.Done():
			_ = server.Close()
			return safeerr.New("UI shutdown timed out")
		}
	}
}
