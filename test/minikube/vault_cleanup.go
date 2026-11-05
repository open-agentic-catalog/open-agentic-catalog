//go:build minikube

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"
)

var errVaultCleanupFault = errors.New("injected owned Vault cleanup failure")

func validateVaultCleanupTest(scenario, mode string) error {
	if mode == "" {
		return nil
	}
	if scenario != "vault" || (mode != "failure" && mode != "interrupt") {
		return errors.New("vault cleanup test requires scenario vault and mode failure or interrupt")
	}
	return nil
}
func (h *harness) injectVaultCleanupFault() error {
	if h.vault == nil || !h.vault.syncReady {
		return errors.New("cleanup fault requires complete Vault delivery")
	}
	h.vault.cleanupFaultReached = true
	if h.options.vaultCleanupTest == "failure" {
		return errVaultCleanupFault
	}
	if h.options.vaultCleanupTest != "interrupt" {
		return errors.New("invalid Vault cleanup fault")
	}
	fmt.Println("[vault] send owned process SIGTERM after complete secret delivery")
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		return err
	}
	<-h.ctx.Done()
	return h.ctx.Err()
}
func (h *harness) expectedVaultCleanupFault(err error) bool {
	if h.vault == nil || !h.vault.cleanupFaultReached {
		return false
	}
	switch h.options.vaultCleanupTest {
	case "failure":
		return errors.Is(err, errVaultCleanupFault)
	case "interrupt":
		return errors.Is(err, context.Canceled) && errors.Is(h.ctx.Err(), context.Canceled)
	default:
		return false
	}
}

// cleanupVault uses a fresh context; workload cancellation must not prevent revocation.
// Partial initial setup still uses the outer harness's complete owned-profile teardown.
func (h *harness) cleanupVault() error {
	if h.vault == nil || !h.clusterReady || !h.vault.syncReady {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if err := h.removeVaultBindings(ctx); err != nil {
		return fmt.Errorf("remove owned Vault bindings: %w", err)
	}
	h.report.Vault.CleanupVerified = true
	h.report.Checks = append(h.report.Checks, "vault-destination-deletion-without-recreation", "vault-owned-authentication-and-token-revocation", "vault-cleanup-preserves-unrelated-bootstrap-secret")
	return nil
}
func (h *harness) vaultApplyContext(ctx context.Context, label string, object kubernetesObject) error {
	raw, err := json.Marshal(object)
	if err != nil {
		return err
	}
	_, err = h.topologyCommand(ctx, label, raw, "apply", "--server-side", "--field-manager=open-agentic-catalog-minikube", "-f", "-")
	return err
}
func (h *harness) vaultDestinationsAbsent(ctx context.Context, names []string) error {
	args := make([]string, 0, 8+len(names))
	args = append(args, "--request-timeout=3s", "-n", namespace, "get", "secrets")
	args = append(args, names...)
	args = append(args, "--ignore-not-found", "-o", "name")
	return waitTopology(ctx, func() (bool, error) {
		raw, err := h.topologyCommand(ctx, "verify removed destinations stay absent", nil, args...)
		return strings.TrimSpace(string(raw)) == "", err
	})
}
func (h *harness) vaultDeleteSync(ctx context.Context, names []string) error {
	args := make([]string, 0, 7+len(names))
	args = append(args, "-n", namespace, "delete", "vaultstaticsecret")
	args = append(args, names...)
	args = append(args, "--ignore-not-found", "--wait=true", "--timeout=90s")
	_, err := h.topologyCommand(ctx, "stop exact sync resources and complete finalizers", nil, args...)
	return err
}
func (h *harness) removeVaultBindings(ctx context.Context) error {
	// A failure can leave Vault sealed or its controller stopped. Recover only these owned processes.
	if _, err := h.topologyCommand(ctx, "restore owned cleanup bootstrap access", []byte(h.vault.rootToken), "-n", vaultNamespace, "exec", "-i", vaultRelease+"-0", "--", "sh", "-ec", "umask 077; cat > /tmp/catalog-vault-bootstrap"); err != nil {
		return err
	}
	if _, err := h.vaultExecContext(ctx, "unseal owned server for cleanup", map[string]string{"key": h.vault.unsealKey}, "write", "-format=json", "sys/unseal", "-"); err != nil {
		return err
	}
	if _, err := h.topologyCommand(ctx, "allow owned controller to finish finalizers", nil, "-n", vaultNamespace, "scale", "deployment/"+vaultOperatorDeployment, "--replicas=1"); err != nil {
		return err
	}
	if _, err := h.topologyCommand(ctx, "wait for cleanup controller", nil, "-n", vaultNamespace, "rollout", "status", "deployment/"+vaultOperatorDeployment, "--timeout=90s"); err != nil {
		return err
	}
	token, err := h.vaultLoginContext(ctx, namespace, "catalog-vault-runtime", "vault")
	if err != nil {
		return err
	}
	if _, err := h.vaultScopedReadContext(ctx, token, "catalog-cursor-signing"); err != nil {
		return err
	}
	managed := append([]string(nil), vaultStartupInputs...)
	managed = append(managed, vaultSourceSecret)
	all := append(append([]string(nil), managed...), "catalog-vault-conflict")
	if err := h.vaultDeleteSync(ctx, all); err != nil {
		return err
	}
	if err := h.vaultDestinationsAbsent(ctx, managed); err != nil {
		return err
	}
	// Prove the controller still reconciles another resource after target deletion.
	const control = "catalog-vault-cleanup-control"
	object := vaultRuntimeSecret(control, map[string]string{"key": ""})
	spec, ok := object["spec"].(map[string]any)
	if !ok {
		return errors.New("invalid cleanup control spec")
	}
	spec["path"] = "catalog-cursor-signing"
	if err := h.vaultApplyContext(ctx, "reconcile separate owned cleanup control", object); err != nil {
		return err
	}
	expected := map[string]string{"key": h.secrets.cursorKey, "vault-kv-version": fmt.Sprint(h.vault.versions["catalog-cursor-signing"])}
	if err := waitTopology(ctx, func() (bool, error) {
		raw, err := h.vaultRead(ctx, "vaultstaticsecret", control)
		if err != nil {
			return false, err
		}
		uid, ready := currentVaultSync(raw)
		if !ready {
			return false, nil
		}
		raw, err = h.vaultRead(ctx, "secret", control)
		if err != nil {
			return false, err
		}
		return validateVaultDestination(raw, uid, control, expected), nil
	}); err != nil {
		return err
	}
	if err := h.vaultDestinationsAbsent(ctx, managed); err != nil {
		return err
	}
	if err := h.vaultDeleteSync(ctx, []string{control}); err != nil {
		return err
	}
	if err := h.vaultDestinationsAbsent(ctx, []string{control}); err != nil {
		return err
	}
	// Existing bootstrap input is not owned by VSO and must survive this removal.
	raw, err := h.topologyCommand(ctx, "preserve unrelated bootstrap Secret", nil, "-n", namespace, "get", "secret", "postgres-owner", "-o", "name")
	if err != nil || strings.TrimSpace(string(raw)) != "secret/postgres-owner" {
		return errors.New("vault removal changed an unrelated bootstrap Secret")
	}
	if _, err := h.topologyCommand(ctx, "stop owned synchronization controller", nil, "-n", vaultNamespace, "scale", "deployment/"+vaultOperatorDeployment, "--replicas=0"); err != nil {
		return err
	}
	if _, err := h.topologyCommand(ctx, "wait for owned controller removal", nil, "-n", vaultNamespace, "wait", "pod", "-l", "app.kubernetes.io/instance=catalog-vso", "--for=delete", "--timeout=90s"); err != nil {
		return err
	}
	for _, role := range []string{"runtime", "owner"} {
		if _, err := h.vaultExecContext(ctx, "remove owned Vault role "+role, nil, "delete", "auth/kubernetes/role/catalog-"+role); err != nil {
			return err
		}
	}
	if _, err := h.vaultExecContext(ctx, "revoke owned Kubernetes authentication tokens", nil, "delete", "sys/auth/kubernetes"); err != nil {
		return err
	}
	if _, err := h.vaultScopedReadContext(ctx, token, "catalog-cursor-signing"); !vaultPermissionDenied(err) {
		return errors.New("owned sync token survived authentication removal")
	}
	for _, role := range []string{"runtime", "owner"} {
		if _, err := h.vaultExecContext(ctx, "remove owned Vault policy "+role, nil, "delete", "sys/policies/acl/catalog-"+role); err != nil {
			return err
		}
	}
	if _, err := h.vaultExecContext(ctx, "revoke owned bootstrap token", nil, "token", "revoke", "-self"); err != nil {
		return err
	}
	return nil
}
