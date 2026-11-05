//go:build minikube

// The Minikube harness owns one disposable local cluster and validates the
// packaged Helm deployment without inspecting or changing any caller context.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// main exits with the isolated Minikube runner's status.
func main() {
	os.Exit(run())
}

// run validates scenario inputs and owns the signal-bounded harness lifecycle, including failure diagnostics, cleanup and successful evidence output.
func run() int {
	scenario := flag.String("scenario", "basic", "basic, topology, monitoring, tracing, ui, replicas, semantic, pgbouncer, agntcy, gateway, ans, peers, backup-restore, autoscaling, cert-manager or vault")
	upgradeOnly := flag.Bool("upgrade-only", false, "qualify coordinated upgrades only in the topology scenario")
	ansCapture := flag.Bool("ans-capture", false, "capture authentic ANS V1/V2 public verification fixtures; requires --scenario ans")
	ansTrust := flag.Bool("ans-trust", false, "qualify explicit ANS assessment Jobs; requires --scenario ans")
	vaultCleanupTest := flag.String("vault-cleanup-test", "", "test cleanup after failure or interrupt; requires --scenario vault")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "the Minikube harness does not accept positional arguments")
		return 2
	}
	configured, err := loadOptions(*scenario)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if *upgradeOnly && *scenario != "topology" {
		fmt.Fprintln(os.Stderr, "--upgrade-only requires --scenario topology")
		return 2
	}
	if err := validateVaultCleanupTest(*scenario, *vaultCleanupTest); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	configured.vaultCleanupTest = *vaultCleanupTest
	configured.upgradeOnly = *upgradeOnly
	if *ansCapture && *scenario != "ans" {
		fmt.Fprintln(os.Stderr, "--ans-capture requires --scenario ans")
		return 2
	}
	configured.ansCapture = *ansCapture
	if *ansTrust && (*scenario != "ans" || *ansCapture) {
		fmt.Fprintln(os.Stderr, "--ans-trust requires --scenario ans and excludes --ans-capture")
		return 2
	}
	configured.ansTrust = *ansTrust
	parent, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(parent, configured.overallTimeout)
	defer cancel()
	runner, err := newHarness(ctx, configured)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	runErr := runner.interopProgress("setup", nil, nil)
	if runErr == nil {
		runErr = runner.execute()
	}
	expectedVaultFault := runner.expectedVaultCleanupFault(runErr)
	if runErr != nil && !expectedVaultFault {
		runner.diagnostics()
		runner.agntcyDiagnostics()
		runner.gatewayDiagnostics()
		runner.ansDiagnostics()
		runner.vaultDiagnostics()
	}
	cleanupErr := runner.cleanup()
	if expectedVaultFault && cleanupErr == nil {
		runErr = nil
		runner.report.Checks = append(runner.report.Checks, "vault-expected-"+configured.vaultCleanupTest+"-cleanup-complete")
	}
	runErr = errors.Join(runErr, runner.interopProgress("finished", runErr, cleanupErr))
	if runErr == nil {
		runErr = runner.printReport()
	}
	if err := errors.Join(runErr, cleanupErr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
