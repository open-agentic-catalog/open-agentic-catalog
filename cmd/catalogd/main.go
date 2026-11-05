// Package main implements the catalogd API, worker, and development roles.
// The command owns process signals and dependency supervision. It becomes
// unready before draining HTTP work and background loops, then closes shared
// resources. Production API and worker roles use separate database identities.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/catalog"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/buildinfo"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/config"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/diagnostics"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/observability"
	platformruntime "github.com/open-agentic-catalog/open-agentic-catalog/internal/platform/runtime"
	"github.com/open-agentic-catalog/open-agentic-catalog/internal/safeerr"
	_ "golang.org/x/crypto/x509roots/fallback"
)

var (
	version     = "dev"
	commit      = "unknown"
	sourceState = "unknown"
)

// main reports a terminal process failure to stderr and sets a nonzero exit code.
func main() {
	diagnostics.ProtectStandardLog("catalogd")
	if err := run(); err != nil && (!errors.Is(err, flag.ErrHelp) || errors.Is(err, safeerr.ErrPanic)) {
		diagnostics.Report(os.Stderr, "catalogd", string(catalog.ErrorCodeOf(err)), err, true)
		os.Exit(1)
	}
}

// run passes process arguments into the testable role dispatcher.
func run() (err error) {
	defer safeerr.Recover(&err)
	return runArgs(os.Args[1:])
}

// runArgs composes the selected process role, supervises its independent loops,
// flips readiness before shutdown, and drains within the configured deadline.
func runArgs(arguments []string) error {
	return runArgsServing(arguments, serveHTTP)
}

// runArgsServing lets lifecycle tests observe real OS-assigned listeners without
// reserving and releasing their ports before startup.
func runArgsServing(arguments []string, serve func(*http.Server) error) (componentError error) {
	if len(arguments) < 1 {
		return safeerr.New("usage: catalogd <api|worker|all|version> [--config <path>]")
	}
	roleName := arguments[0]
	if roleName == "help" || roleName == "-h" || roleName == "--help" {
		_, err := fmt.Fprintln(os.Stdout, "usage: catalogd <api|worker|all|version> [--config <path>]")
		return err
	}
	if roleName == "version" || roleName == "--version" {
		identity := buildinfo.New(version, commit, sourceState)
		_, err := fmt.Fprintf(os.Stdout, "open-agentic-catalog %s (%s) [%s]\n", identity.Version, identity.Commit, identity.SourceState)
		return err
	}
	role := config.Role(roleName)
	if role != config.RoleAPI && role != config.RoleWorker && role != config.RoleAll {
		return safeerr.Wrap(fmt.Errorf("unknown catalogd role %q", roleName), "unknown role; use catalogd --help")
	}
	flags := diagnostics.Flags("catalogd " + roleName)
	configPath := flags.String("config", "", "optional path to a service JSON configuration baseline")
	if err := diagnostics.Parse(flags, arguments[1:]); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runtime, err := platformruntime.Build(ctx, platformruntime.BuildOptions{StartTracing: true, ConfigPath: *configPath, Version: version, Commit: commit, SourceState: sourceState, Role: role})
	if err != nil {
		return safeerr.At("startup", fmt.Errorf("initialize service: %w", err))
	}
	logger := runtime.Logger()
	defer func() {
		componentError = diagnostics.WithFormat(componentError, runtime.Config.LogFormat, diagnostics.Process{Service: runtime.Config.ServiceName, Role: string(role), Environment: runtime.Config.Environment, Version: version})
	}()
	defer safeerr.Recover(&componentError)
	defer runtime.Close()
	if runtime.Tracing != nil {
		ctx = catalog.WithTracer(ctx, runtime.Tracing)
	}
	var server *http.Server
	var operationsServer *http.Server
	if role == config.RoleAPI || role == config.RoleAll {
		server = apiHTTPServer(runtime.Config.HTTP, runtime.HTTP.Handler(), logger)
	}
	if role == config.RoleWorker || role == config.RoleAll {
		operationsServer = operationsHTTPServer(runtime.Config.Worker.OperationsAddress, runtime.Operations.Handler(), logger)
	}
	componentCount := 0
	if role == config.RoleAPI || role == config.RoleAll {
		componentCount++
	}
	if role == config.RoleWorker || role == config.RoleAll {
		componentCount += 4
	}
	if runtime.SemanticMaintainer != nil {
		componentCount++
	}
	errorsChannel := make(chan error, componentCount)
	operationsErrors := make(chan error, 1)
	remainingComponents, operationsExited := componentCount, false
	defer func() {
		if recover() != nil {
			componentError = errors.Join(componentError, safeerr.ErrPanic)
		}
		stop()
		runtime.Observability.SetLifecycle(observability.LifecycleDraining)
		if runtime.HTTP != nil {
			runtime.HTTP.SetReady(false)
		}
		shutdownContext, cancel := context.WithTimeout(context.Background(), runtime.Config.HTTP.ShutdownTimeout.Duration)
		defer cancel()
		if role == config.RoleAPI || role == config.RoleAll {
			if err := server.Shutdown(shutdownContext); err != nil {
				componentError = errors.Join(componentError, fmt.Errorf("shut down API: %w", err))
				_ = server.Close()
			}
		}
		componentError = drainComponents(shutdownContext, errorsChannel, remainingComponents, componentError)
		if operationsServer != nil && !operationsExited {
			componentError = drainOperations(shutdownContext, operationsServer.Shutdown, operationsErrors, componentError)
			if shutdownContext.Err() != nil {
				_ = operationsServer.Close()
			}
		}
		if err := runtime.Tracing.Shutdown(shutdownContext); err != nil {
			logger.Warn("trace shutdown incomplete; telemetry may be lost", "event", "trace.shutdown_incomplete", "component", "tracing")
		}
		runtime.Observability.SetLifecycle(observability.LifecycleStopped)
	}()

	if role == config.RoleAPI || role == config.RoleAll {
		go componentResult(errorsChannel, func() error {
			runtime.Observability.SetComponent(observability.ComponentAPI, observability.ComponentRunning)
			defer runtime.Observability.SetComponent(observability.ComponentAPI, observability.ComponentStopped)
			logger.Info("catalog API starting", "event", "component.starting", "component", "api")
			return serve(server)
		})
	}
	if role == config.RoleWorker || role == config.RoleAll {
		go componentResult(operationsErrors, func() error {
			runtime.Observability.SetComponent(observability.ComponentOperations, observability.ComponentRunning)
			defer runtime.Observability.SetComponent(observability.ComponentOperations, observability.ComponentStopped)
			logger.Info("catalog worker operations server starting", "event", "component.starting", "component", "operations_server")
			return serve(operationsServer)
		})
		go componentResult(errorsChannel, func() error {
			runtime.Observability.SetComponent(observability.ComponentWorker, observability.ComponentRunning)
			defer runtime.Observability.SetComponent(observability.ComponentWorker, observability.ComponentStopped)
			logger.Info("catalog worker starting", "event", "component.starting", "component", "job_worker", "concurrency", runtime.Config.Worker.Concurrency)
			err := superviseDependency(ctx, logger, "job_worker", runtime.Config.Worker, rand.Reader, func(runContext context.Context) error {
				return runtime.JobWorker.RunConcurrent(runContext, runtime.Config.Worker.Concurrency)
			}, componentStateObserver(runtime.Observability, observability.ComponentWorker))
			if errors.Is(err, context.Canceled) && !errors.Is(err, safeerr.ErrPanic) {
				err = nil
			}
			return err
		})
		go componentResult(errorsChannel, func() error {
			component := "catalog outbox drainer"
			if runtime.Config.Webhooks.Enabled {
				component = "catalog webhook dispatcher"
			}
			runtime.Observability.SetComponent(observability.ComponentOutbox, observability.ComponentRunning)
			defer runtime.Observability.SetComponent(observability.ComponentOutbox, observability.ComponentStopped)
			logger.Info(component+" starting", "event", "component.starting", "component", "outbox_processor")
			err := superviseDependency(ctx, logger, "outbox_processor", runtime.Config.Worker, rand.Reader, runtime.OutboxProcessor.Run, componentStateObserver(runtime.Observability, observability.ComponentOutbox))
			if errors.Is(err, context.Canceled) && !errors.Is(err, safeerr.ErrPanic) {
				err = nil
			}
			return err
		})
		go componentResult(errorsChannel, func() error {
			runtime.Observability.SetComponent(observability.ComponentSourceMaintenance, observability.ComponentRunning)
			defer runtime.Observability.SetComponent(observability.ComponentSourceMaintenance, observability.ComponentStopped)
			logger.Info("catalog source maintainer starting", "event", "component.starting", "component", "source_maintainer", "minimum_interval_ms", runtime.Config.Worker.SourceMaintenanceInterval.Milliseconds())
			err := superviseDependency(ctx, logger, "source_maintainer", runtime.Config.Worker, rand.Reader, runtime.SourceMaintainer.Run, componentStateObserver(runtime.Observability, observability.ComponentSourceMaintenance))
			if errors.Is(err, context.Canceled) && !errors.Is(err, safeerr.ErrPanic) {
				err = nil
			}
			return err
		})
		go componentResult(errorsChannel, func() error {
			runtime.Observability.SetComponent(observability.ComponentHousekeeping, observability.ComponentRunning)
			defer runtime.Observability.SetComponent(observability.ComponentHousekeeping, observability.ComponentStopped)
			logger.Info("catalog housekeeper starting", "event", "component.starting", "component", "housekeeper", "interval_ms", runtime.Config.Worker.HousekeepingInterval.Milliseconds())
			err := superviseDependency(ctx, logger, "housekeeper", runtime.Config.Worker, rand.Reader, runtime.Housekeeper.Run, componentStateObserver(runtime.Observability, observability.ComponentHousekeeping))
			if errors.Is(err, context.Canceled) && !errors.Is(err, safeerr.ErrPanic) {
				err = nil
			}
			return err
		})
	}
	if runtime.SemanticMaintainer != nil {
		go componentResult(errorsChannel, func() error {
			runtime.Observability.SetComponent(observability.ComponentSemanticMaintenance, observability.ComponentRunning)
			defer runtime.Observability.SetComponent(observability.ComponentSemanticMaintenance, observability.ComponentStopped)
			err := superviseDependency(ctx, logger, "semantic_maintainer", runtime.Config.Worker, rand.Reader, runtime.SemanticMaintainer.Run, componentStateObserver(runtime.Observability, observability.ComponentSemanticMaintenance))
			if errors.Is(err, context.Canceled) && !errors.Is(err, safeerr.ErrPanic) {
				err = nil
			}
			return err
		})
	}
	runtime.Observability.SetLifecycle(observability.LifecycleRunning)
	remainingComponents, operationsExited, componentError = waitForComponentExit(ctx, stop, logger, errorsChannel, operationsErrors, componentCount)
	return componentError
}

// waitForComponentExit starts cancellation on the first component completion
// and records whether that completion has already been consumed for draining.
func waitForComponentExit(ctx context.Context, stop context.CancelFunc, logger *slog.Logger, errorsChannel, operationsErrors <-chan error, componentCount int) (int, bool, error) {
	remainingComponents := componentCount
	var componentError error
	operationsExited := false
	select {
	case <-ctx.Done():
		logger.Info("catalog service draining", "event", "process.draining", "component", "process")
	case err := <-errorsChannel:
		remainingComponents--
		if err != nil {
			componentError = err
		}
		stop()
	case err := <-operationsErrors:
		operationsExited = true
		if err != nil {
			componentError = fmt.Errorf("worker operations server: %w", err)
		}
		stop()
	}
	return remainingComponents, operationsExited, componentError
}

// drainOperations shuts down the worker listener within the shared budget and
// retains both shutdown and listener-completion failures alongside earlier errors.
func drainOperations(ctx context.Context, shutdown func(context.Context) error, results <-chan error, componentError error) error {
	if err := shutdown(ctx); err != nil {
		componentError = errors.Join(componentError, fmt.Errorf("shut down worker operations server: %w", err))
	}
	select {
	case err := <-results:
		componentError = errors.Join(componentError, err)
	case <-ctx.Done():
		componentError = errors.Join(componentError, fmt.Errorf("drain worker operations server: %w", ctx.Err()))
	}
	return componentError
}

// drainComponents collects exactly the outstanding completions within the shared
// shutdown budget, retaining the original failure and every observed later cause.
func drainComponents(ctx context.Context, results <-chan error, remaining int, componentError error) error {
	for remaining > 0 {
		select {
		case err := <-results:
			remaining--
			componentError = errors.Join(componentError, err)
		case <-ctx.Done():
			componentError = errors.Join(componentError, fmt.Errorf("drain service components: %w", ctx.Err()))
			remaining = 0
		}
	}
	return componentError
}

// serveHTTP treats coordinated server closure as success and propagates other
// listener failures to the process supervisor.
func serveHTTP(server *http.Server) error {
	err := server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// superviseDependency retries only explicitly classified dependency outages.
// Configuration, policy, and invariant failures remain terminal.
// policy comes from validated worker configuration; entropy is process-owned.
func superviseDependency(ctx context.Context, logger *slog.Logger, component string, policy config.Worker, entropy io.Reader, run func(context.Context) error, observers ...func(bool)) (result error) {
	defer safeerr.Recover(&result)
	initial, ceiling := policy.DependencyRetryInitialDelay.Duration, policy.DependencyRetryCeiling.Duration
	window := initial
	var warning diagnostics.Warning
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, observe := range observers {
			observe(false)
		}
		started := time.Now()
		err := run(ctx)
		if errors.Is(err, safeerr.ErrPanic) {
			return err
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		if err == nil || !retryableDependencyFailure(err) {
			return err
		}
		if time.Since(started) >= time.Minute {
			window = initial
		}
		for _, observe := range observers {
			observe(true)
		}
		retryInterval := dependencyRetryDelay(window, entropy)
		warning.Warn(time.Now(), logger, component+" dependency unavailable; retrying", "event", "dependency.unavailable", "component", component, "error_code", catalog.ErrorCodeOf(err), "error_type", fmt.Sprintf("%T", err), "retry_delay_ms", retryInterval.Milliseconds())
		timer := time.NewTimer(retryInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
		// Saturate before addition: the window never exceeds its configured cap.
		window += min(window, ceiling-window)
	}
}

// dependencyRetryDelay samples the inclusive upper half of a bounded window.
// Sampling failure uses that interval's midpoint, never a zero-delay retry.
func dependencyRetryDelay(window time.Duration, entropy io.Reader) time.Duration {
	lower := window / 2
	span := window - lower
	value, err := rand.Int(entropy, big.NewInt(int64(span)+1))
	if err != nil {
		return lower + span/2
	}
	return lower + time.Duration(value.Int64())
}

// retryableDependencyFailure treats a joined cycle as retryable only when
// every constituent failure has an explicit dependency classification. This
// prevents one outage from concealing a simultaneous invariant failure.
func retryableDependencyFailure(err error) bool {
	if errors.Is(err, safeerr.ErrPanic) {
		return false
	}
	if err == nil {
		return false
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !retryableDependencyFailure(cause) {
				return false
			}
		}
		return true
	}
	// A domain error deliberately classifies its cause. Ordinary wrappers only
	// add context and must preserve the joined-cycle rule beneath them.
	if classified, ok := err.(*catalog.DomainError); ok { //nolint:errorlint // Inspect this node only; errors.As would skip a wrapped join and hide terminal siblings.
		return classified.Code == catalog.CodeDependencyUnavailable
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return retryableDependencyFailure(wrapped.Unwrap())
	}
	return catalog.ErrorCodeOf(err) == catalog.CodeDependencyUnavailable
}

// componentStateObserver projects supervisor retry state onto one fixed metric
// component without retaining the dependency error or its potentially private text.
func componentStateObserver(registry *observability.Registry, component observability.Component) func(bool) {
	return func(degraded bool) {
		state := observability.ComponentRunning
		if degraded {
			state = observability.ComponentDegraded
		}
		registry.SetComponent(component, state)
	}
}

// apiHTTPServer keeps configured socket bounds at the process edge.
func apiHTTPServer(c config.HTTP, handler http.Handler, loggers ...*slog.Logger) *http.Server {
	logger := listenerLogger(loggers)
	return &http.Server{Addr: c.Address, Handler: diagnostics.RecoverHTTP(logger, handler), ErrorLog: diagnostics.HTTPErrorLog(logger), ReadTimeout: c.ReadTimeout.Duration, ReadHeaderTimeout: c.ReadTimeout.Duration, WriteTimeout: c.WriteTimeout.Duration, IdleTimeout: c.IdleTimeout.Duration, MaxHeaderBytes: 32 << 10}
}

// operationsHTTPServer preserves the worker listener's fixed operational bounds.
func operationsHTTPServer(address string, handler http.Handler, loggers ...*slog.Logger) *http.Server {
	logger := listenerLogger(loggers)
	return &http.Server{Addr: address, Handler: diagnostics.RecoverHTTP(logger, handler), ErrorLog: diagnostics.HTTPErrorLog(logger), ReadTimeout: 5 * time.Second, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
}

// componentResult always reports one completion, including a panic, so the
// supervisor can cancel siblings and use its normal bounded drain.
func componentResult(results chan<- error, run func() error) {
	var result error
	defer func() { results <- safeerr.At("component", result) }()
	defer safeerr.Recover(&result)
	result = run()
}

// listenerLogger preserves explicit process ownership and a safe default for
// constructors used independently of process composition.
func listenerLogger(loggers []*slog.Logger) *slog.Logger {
	if len(loggers) > 0 && loggers[0] != nil {
		return loggers[0]
	}
	return slog.New(slog.NewTextHandler(os.Stdout, nil))
}
