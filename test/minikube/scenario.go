//go:build minikube

// Scenario orchestration verifies packaged startup, roles, recovery and API behavior.

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/open-agentic-catalog/open-agentic-catalog/internal/identity"
)

type httpResult struct {
	Status int
	Header http.Header
	Body   []byte
}

type podIdentity struct {
	Name           string
	UID            string
	Node           string
	ServiceAccount string
	Image          string
	ImageID        string
	Ready          bool
	Restarts       int
}

// execute creates the owned cluster, installs and seeds Catalog, then runs the selected scenario's checks; the caller owns cleanup and diagnostics.
func (h *harness) execute() error {
	if err := h.preflight(); err != nil {
		return err
	}
	if err := h.buildImages(); err != nil {
		return err
	}
	if err := h.startCluster(); err != nil {
		return err
	}
	if err := h.interopProgress("cluster-ready", nil, nil); err != nil {
		return err
	}
	if err := h.loadImages(); err != nil {
		return err
	}
	if h.options.scenario == "peers" {
		if err := h.prepareCatalogPeerTLS(); err != nil {
			return err
		}
	}
	if err := h.prepareDependencies(); err != nil {
		return err
	}
	if h.options.vaultCleanupTest != "" {
		return h.injectVaultCleanupFault()
	}
	if h.options.ansCapture {
		return h.captureANSTrust()
	}
	if err := h.installCatalog(); err != nil {
		return err
	}
	if err := h.provisionIdentity("provision-primary", primaryTenantID, "tenant-one", "oidc-admin", "tenant_admin,contributor,reviewer,publisher,reader,source_admin,security_reviewer"); err != nil {
		return err
	}
	if err := h.provisionIdentity("provision-other", otherTenantID, "tenant-two", "oidc-other-admin", "tenant_admin"); err != nil {
		return err
	}
	if err := h.issueTokens(); err != nil {
		return err
	}
	address, err := h.startPortForward("service/catalog", 8080)
	if err != nil {
		return err
	}
	baseURL := "http://" + address
	if err := h.waitHTTP(baseURL + "/readyz"); err != nil {
		return err
	}
	if err := h.bootstrapSmokeIdentities(baseURL); err != nil {
		return err
	}
	if h.options.scenario == "peers" {
		return h.verifyCatalogPeers(baseURL)
	}
	runID := "minikube-" + h.suffix
	initialSmoke, err := h.runSmoke(baseURL, runID)
	if err != nil {
		return err
	}
	if err := h.verifyServedContract(baseURL); err != nil {
		return err
	}
	if h.vault != nil {
		return h.verifyVault(baseURL, initialSmoke)
	}
	if h.options.scenario == "autoscaling" {
		return h.verifyAutoscaling(runID, initialSmoke)
	}
	if h.options.scenario == "cert-manager" {
		return h.verifyCertificateRenewal()
	}
	if h.options.scenario == "backup-restore" {
		return h.verifyBackupRestore(baseURL, initialSmoke)
	}
	if h.options.scenario == "ans" {
		return h.verifyANS(baseURL)
	}
	if h.options.scenario == "gateway" {
		return h.verifyGateway(baseURL)
	}
	if h.options.scenario == "agntcy" {
		return h.verifyAGNTCY(baseURL)
	}
	if h.options.scenario == "pgbouncer" {
		return h.verifyPgBouncer(baseURL, runID, initialSmoke)
	}
	if h.options.scenario == "monitoring" {
		return h.verifyMonitoring(baseURL)
	}
	if h.options.scenario == "semantic" {
		return h.verifySemantic()
	}
	if h.options.scenario == "tracing" {
		return h.verifyTracing()
	}
	if h.options.scenario == "ui" {
		return h.verifyUI()
	}
	if h.options.scenario == "replicas" {
		if err := h.verifyUI(); err != nil {
			return err
		}
		address, err := h.startPortForward("service/catalog", 8080)
		if err != nil {
			return err
		}
		fixtureAddress, err := h.startPortForward("service/federation-fixture", 8443)
		if err != nil {
			return err
		}
		caPath := filepath.Join(h.temporary, "fixture", "trust", "functional-ca.pem")
		sourceID, err := h.runFederationWorkflow("http://"+address, "https://"+fixtureAddress, caPath)
		if err != nil {
			return err
		}
		if err := h.verifyWorkerLeaseRecovery("http://"+address, "https://"+fixtureAddress, caPath, sourceID); err != nil {
			return err
		}
		return h.verifyReconfiguration(runID, initialSmoke)
	}
	if h.options.scenario == "basic" || h.options.scenario == "topology" {
		if err := h.verifyCoordinatedUpgrade(runID, initialSmoke); err != nil {
			return err
		}
		if h.options.upgradeOnly {
			h.report.Checks = append(h.report.Checks, "governed-http-smoke-and-retained-state")
			return nil
		}
		// The original Service port-forward targeted a Pod drained by the upgrade.
		address, err = h.startPortForward("service/catalog", 8080)
		if err != nil {
			return err
		}
		baseURL = "http://" + address
		if err := h.waitHTTP(baseURL + "/readyz"); err != nil {
			return err
		}
	}
	if h.options.scenario == "basic" {
		if err := h.verifyInstrumentation("initial"); err != nil {
			return err
		}
	}
	if h.options.scenario == "topology" {
		if err := h.runTopology(); err != nil {
			return err
		}
	} else if err := h.recreateRuntimePods(); err != nil {
		return err
	}
	if err := h.verifyReconfiguration(runID, initialSmoke); err != nil {
		return err
	}
	if h.options.scenario == "basic" {
		if err := h.verifyInstrumentation("reconfigured"); err != nil {
			return err
		}
	}
	// End the topology scenario with node loss. Restoring Minikube itself is an
	// infrastructure operation; the service contract is that hard spread leaves
	// one ready replica and traffic remains available until the owned cluster is
	// deleted during cleanup.
	if h.options.scenario == "topology" {
		if err := h.verifyWorkerNodeLoss(); err != nil {
			return err
		}
	}
	h.report.Checks = append(h.report.Checks, "governed-http-smoke-and-retained-state")
	if h.options.scenario == "basic" {
		if err := h.verifyWebhooks(); err != nil {
			return err
		}
		return h.verifyMCP()
	}
	return nil
}

// installCatalog installs the application chart and verifies its workloads, first exercising a failed migration hook in the basic scenario.
func (h *harness) installCatalog() error {
	if err := h.preparePeerTrust(); err != nil {
		return err
	}
	if h.options.scenario == "basic" {
		invalidValues, err := h.writeHelmValues("initial", "catalog_api_missing")
		if err != nil {
			return err
		}
		installErr := h.deployCatalog(invalidValues, "2m")
		if installErr == nil {
			return errors.New("initial install unexpectedly accepted a missing runtime role")
		}
		if err := h.verifyFailedInstall(); err != nil {
			return fmt.Errorf("expected migration-stage refusal (%w), verification: %w", installErr, err)
		}
	}
	values, err := h.writeHelmValues("initial", "catalog_api")
	if err != nil {
		return err
	}
	command := "install"
	arguments := []string{releaseName, h.options.repositoryRoot + "/deployments/helm/open-agentic-catalog", "--namespace", h.catalogNamespace(), "--values", values, "--wait", "--wait-for-jobs", "--timeout=4m"}
	if h.options.scenario == "basic" {
		command = "upgrade"
		arguments = append([]string{"--install"}, arguments...)
	}
	switch {
	case h.managedBuild() != nil:
		var raw []byte
		raw, err = os.ReadFile(values) // #nosec G304 -- writeHelmValues created this path under the invocation-owned temporary directory.
		if err != nil {
			return err
		}
		h.setManagedCatalogValues(fmt.Sprintf("%x", sha256.Sum256([]byte(h.redactDiagnostic(string(raw))))))
		err = h.installManagedCatalog(append([]string{command}, arguments...))
	case h.options.scenario == "basic" || h.options.scenario == "pgbouncer" || h.options.scenario == "peers" || h.options.scenario == "backup-restore" || h.options.scenario == "vault":
		err = h.deployCatalog(values, "4m")
	default:
		_, err = h.helm("install Catalog through Helm", append([]string{command}, arguments...)...)
	}
	if err != nil {
		return err
	}
	if err := h.verifyInstalledWorkloads(); err != nil {
		return err
	}
	h.report.Checks = append(h.report.Checks, "helm-install-and-hooks")
	return nil
}

// verifyFailedInstall requires retained role-validation failure evidence, no runtime Deployments and no Catalog migration table after the deliberate failed install.
func (h *harness) verifyFailedInstall() error {
	raw, err := h.kubectl("inspect retained failed migration hook", "-n", h.catalogNamespace(), "get", "job/catalog-migrate", "-o", "json")
	if err != nil {
		return err
	}
	pods, err := h.kubectl("inspect retained failed migration Pod", "-n", h.catalogNamespace(), "get", "pods", "-l", "job-name=catalog-migrate", "-o", "json")
	if err != nil {
		return err
	}
	if err := verifyFailedMigrationResources(raw, pods); err != nil {
		return err
	}
	logs, err := h.kubectl("verify failed migration diagnostic", "-n", h.catalogNamespace(), "logs", "job/catalog-migrate", "--all-containers=true")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(logs)) != "catalogctl: migrate: INTERNAL: operation failed; check configuration and dependency availability" {
		return fmt.Errorf("failed migration hook did not emit exactly the safe command diagnostic: %s", logs)
	}
	for _, kindName := range []string{"deployment/catalog-api", "deployment/catalog-worker"} {
		if _, err := h.kubectl("verify runtime was not installed", "-n", h.catalogNamespace(), "get", kindName); err == nil {
			return fmt.Errorf("%s exists after a failed pre-install migration", kindName)
		}
	}
	// Establish the role fault from database state, not a private dependency
	// message. The subsequent install changes only the selected role and must pass.
	raw, err = h.kubectl("verify missing-role fixture and unchanged schema", "-n", h.catalogNamespace(), "exec", "postgres-0", "--", "psql", "--no-psqlrc", "-U", "catalog_owner", "-d", "catalog", "-At", "-c", "SELECT to_regclass('public.catalog_schema_migrations') IS NULL AND NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='catalog_api_missing') AND (SELECT count(*) FROM pg_roles WHERE rolname IN ('catalog_api','catalog_worker'))=2")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(raw)) != "t" {
		return errors.New("failed pre-install did not preserve the missing-role fixture and untouched Catalog schema")
	}
	// Helm can clean successful prerequisite hooks after a later Job fails.
	// Its retained Job, owned Pod and database state establish this refusal;
	// the subsequent successful retry must recreate any removed prerequisites.
	h.report.Checks = append(h.report.Checks, "failed-hook-safe-retry-boundary")
	return nil
}

// verifyFailedMigrationResources refuses a missing, successful or unrelated
// hook. The fixture sets backoffLimit=0, so it must retain one failed Pod.
func verifyFailedMigrationResources(jobRaw, podsRaw []byte) error {
	var job struct {
		Metadata struct {
			Name string `json:"name"`
			UID  string `json:"uid"`
		} `json:"metadata"`
		Spec struct {
			Template struct {
				Spec struct {
					ServiceAccount string `json:"serviceAccountName"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
		Status struct {
			Failed int `json:"failed"`
		} `json:"status"`
	}
	if err := json.Unmarshal(jobRaw, &job); err != nil || job.Metadata.Name != "catalog-migrate" || job.Metadata.UID == "" || job.Spec.Template.Spec.ServiceAccount != "catalog-migration" || job.Status.Failed != 1 {
		return errors.New("failed migration Job identity or failure state is invalid")
	}
	var pods struct {
		Items []struct {
			Metadata struct {
				Owners []struct {
					Name       string `json:"name"`
					Kind       string `json:"kind"`
					UID        string `json:"uid"`
					Controller bool   `json:"controller"`
				} `json:"ownerReferences"`
			} `json:"metadata"`
			Spec struct {
				ServiceAccount string `json:"serviceAccountName"`
			} `json:"spec"`
			Status struct {
				Phase      string `json:"phase"`
				Containers []struct {
					Name  string `json:"name"`
					State struct {
						Terminated *struct {
							ExitCode int `json:"exitCode"`
						} `json:"terminated"`
					} `json:"state"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(podsRaw, &pods); err != nil || len(pods.Items) != 1 {
		return errors.New("failed migration must retain exactly one Pod")
	}
	pod := pods.Items[0]
	owned := false
	for _, owner := range pod.Metadata.Owners {
		if owner.Controller && owner.Kind == "Job" && owner.Name == job.Metadata.Name && owner.UID == job.Metadata.UID {
			owned = true
		}
	}
	if !owned || pod.Spec.ServiceAccount != "catalog-migration" || pod.Status.Phase != "Failed" || len(pod.Status.Containers) != 1 {
		return errors.New("failed migration Pod is not bound to its Job and ServiceAccount")
	}
	container := pod.Status.Containers[0]
	if container.Name != "migrate" || container.State.Terminated == nil || container.State.Terminated.ExitCode != 1 {
		return errors.New("failed migration container has no command refusal evidence")
	}
	return nil
}

// verifyInstalledWorkloads checks hook cleanup, image and role identities, expected replica placement, and absence of application Kubernetes authority.
func (h *harness) verifyInstalledWorkloads() error {
	for _, deployment := range []string{"catalog-api", "catalog-worker"} {
		if _, err := h.kubectl("wait for "+deployment, "-n", h.catalogNamespace(), "rollout", "status", "deployment/"+deployment, "--timeout=3m"); err != nil {
			return err
		}
	}
	for _, resource := range []string{"serviceaccount/catalog-migration", "configmap/catalog-migration", "job/catalog-migrate"} {
		if _, err := h.kubectl("verify successful hook cleanup", "-n", h.catalogNamespace(), "get", resource); err == nil {
			return fmt.Errorf("successful Helm hook resource %s was retained", resource)
		}
	}
	serviceRaw, err := h.kubectl("verify product Service ports", "-n", h.catalogNamespace(), "get", "service/catalog", "-o", "json")
	if err != nil {
		return err
	}
	var service struct {
		Spec struct {
			Ports []struct {
				Name string `json:"name"`
				Port int    `json:"port"`
			} `json:"ports"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(serviceRaw, &service); err != nil || len(service.Spec.Ports) != 1 || service.Spec.Ports[0].Name != "http" || service.Spec.Ports[0].Port != 8080 {
		return fmt.Errorf("catalog Service exposes an unexpected port set: %s", serviceRaw)
	}
	for role, account := range map[string]string{"api": "catalog-api", "worker": "catalog-worker"} {
		pods, err := h.pods(role)
		if err != nil {
			return err
		}
		want := h.options.runtimeReplicas()
		if len(pods) != want {
			return fmt.Errorf("%s has %d pods, want %d", role, len(pods), want)
		}
		nodes := make(map[string]struct{})
		for _, pod := range pods {
			if !pod.Ready || pod.ServiceAccount != account || pod.Image != h.application || pod.ImageID != h.report.Images["application"] {
				return fmt.Errorf("%s pod identity is incomplete: %#v", role, pod)
			}
			nodes[pod.Node] = struct{}{}
		}
		if h.options.scenario == "topology" && len(nodes) != 2 {
			return fmt.Errorf("%s replicas are not spread across both test workers: %#v", role, pods)
		}
		h.recordPlacement("installed-"+role, pods)
	}
	podsRaw, err := h.kubectl("verify admitted tokenless Pod specifications", "-n", h.catalogNamespace(), "get", "pods", "-l", "app.kubernetes.io/instance="+releaseName, "-o", "json")
	if err != nil {
		return err
	}
	var pods struct {
		Items []struct {
			Spec struct {
				Automount *bool `json:"automountServiceAccountToken"`
				Volumes   []struct {
					Projected *struct {
						Sources []struct {
							ServiceAccountToken any `json:"serviceAccountToken"`
						} `json:"sources"`
					} `json:"projected"`
				} `json:"volumes"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(podsRaw, &pods); err != nil {
		return err
	}
	for _, pod := range pods.Items {
		if pod.Spec.Automount == nil || *pod.Spec.Automount {
			return errors.New("admitted application Pod does not explicitly disable token automount")
		}
		for _, volume := range pod.Spec.Volumes {
			if volume.Projected == nil {
				continue
			}
			for _, source := range volume.Projected.Sources {
				if source.ServiceAccountToken != nil {
					return errors.New("admitted application Pod contains a service-account token projection")
				}
			}
		}
	}
	for _, kind := range []string{"role", "rolebinding"} {
		raw, err := h.kubectl("verify chart-created "+kind+" absence", "-n", h.catalogNamespace(), "get", kind, "-l", "app.kubernetes.io/instance="+releaseName, "-o", "name")
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(raw)) != "" {
			return fmt.Errorf("chart created unexpected Kubernetes authority: %s", raw)
		}
	}
	return nil
}

// pods returns name-sorted, running, non-terminating Pods for one release role, retaining readiness and single-container image evidence.
func (h *harness) pods(role string) ([]podIdentity, error) {
	return h.podsContext(h.ctx, role)
}

func (h *harness) podsContext(ctx context.Context, role string) ([]podIdentity, error) {
	raw, err := h.topologyCommand(ctx, "inspect "+role+" pods", nil, "-n", h.catalogNamespace(), "get", "pods", "-l", "app.kubernetes.io/instance="+releaseName+",app.kubernetes.io/component="+role, "-o", "json")
	if err != nil {
		return nil, err
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name              string `json:"name"`
				UID               string `json:"uid"`
				DeletionTimestamp string `json:"deletionTimestamp"`
			} `json:"metadata"`
			Spec struct {
				NodeName           string `json:"nodeName"`
				ServiceAccountName string `json:"serviceAccountName"`
				Containers         []struct {
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"spec"`
			Status struct {
				Phase      string `json:"phase"`
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
				ContainerStatuses []struct {
					ImageID      string `json:"imageID"`
					RestartCount int    `json:"restartCount"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	result := make([]podIdentity, 0, len(list.Items))
	for _, item := range list.Items {
		if item.Metadata.DeletionTimestamp != "" || item.Status.Phase != "Running" {
			continue
		}
		pod := podIdentity{Name: item.Metadata.Name, UID: item.Metadata.UID, Node: item.Spec.NodeName, ServiceAccount: item.Spec.ServiceAccountName}
		if len(item.Spec.Containers) == 1 {
			pod.Image = item.Spec.Containers[0].Image
		}
		if len(item.Status.ContainerStatuses) == 1 {
			pod.ImageID = item.Status.ContainerStatuses[0].ImageID
			pod.Restarts = item.Status.ContainerStatuses[0].RestartCount
		}
		for _, condition := range item.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" {
				pod.Ready = true
			}
		}
		result = append(result, pod)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

// bootstrapSmokeIdentities grants the fixed smoke subjects their Catalog-owned roles through authenticated idempotent API commands.
func (h *harness) bootstrapSmokeIdentities(baseURL string) error {
	if err := bootstrapSmokePolicy(func(_ int, method, path, key, match string, body any) (httpResult, error) {
		return h.request(method, baseURL+path, h.tokens["admin"], key, match, body, nil)
	}); err != nil {
		return err
	}
	h.report.Checks = append(h.report.Checks, "catalog-owned-oidc-memberships")
	return nil
}

// bootstrapSmokePolicy assigns explicit workflow companions without teaching
// ordinary role grants a hierarchy. The isolated fixture has thirteen built-ins.
func bootstrapSmokePolicy(request replicaRequest) error {
	result, err := request(0, http.MethodGet, "/api/v1/access/roles?limit=100", "", "", nil)
	var page struct {
		Items      []identity.RoleDefinition `json:"items"`
		NextCursor string                    `json:"nextCursor"`
	}
	if err != nil || result.Status != http.StatusOK || json.Unmarshal(result.Body, &page) != nil || page.NextCursor != "" {
		return errors.New("bootstrap role catalog unavailable or incomplete")
	}
	definitions := make(map[string]string)
	for _, role := range page.Items {
		if role.Key != "discovery_reader" && role.Key != "publisher_creator" && role.Key != "publication_admin" {
			continue
		}
		if role.ID == "" || role.TenantID != primaryTenantID || !role.Builtin || role.State != identity.DefinitionActive || definitions[role.Key] != "" {
			return errors.New("bootstrap companion definition is invalid")
		}
		definitions[role.Key] = role.ID
	}
	if len(definitions) != 3 {
		return errors.New("bootstrap companion definition is missing")
	}
	for _, actor := range []struct {
		subject           string
		roles, companions []string
	}{
		{"oidc-contributor", []string{"contributor", "reader"}, []string{"discovery_reader", "publisher_creator"}},
		{"oidc-reviewer", []string{"reviewer", "reader"}, []string{"discovery_reader"}},
		{"oidc-publisher", []string{"publisher", "reader"}, []string{"discovery_reader", "publication_admin"}},
		{"oidc-reader", []string{"reader"}, []string{"discovery_reader"}},
	} {
		membership := ""
		for _, role := range actor.roles {
			body := map[string]any{"subject": actor.subject, "actorType": "human", "role": role, "reason": "Prepare deterministic Minikube smoke identity"}
			result, err := request(0, http.MethodPost, "/api/v1/role-assignments", "minikube-bootstrap-"+actor.subject+"-"+role, "", body)
			var assignment identity.RoleAssignment
			if err != nil || result.Status != http.StatusCreated || json.Unmarshal(result.Body, &assignment) != nil || assignment.ID == "" || assignment.TenantID != primaryTenantID || assignment.Subject != actor.subject || assignment.Role != identity.Role(role) || assignment.State != identity.AssignmentActive || assignment.Revision != 1 || assignment.NamespaceID != "" || assignment.MembershipID == "" || membership != "" && membership != assignment.MembershipID {
				return errors.New("bootstrap workflow assignment is invalid")
			}
			membership = assignment.MembershipID
		}
		for _, role := range actor.companions {
			body := map[string]string{"membershipId": membership, "roleDefinitionId": definitions[role], "reason": "Explicit tenant workflow companion for Minikube smoke"}
			result, err := request(0, http.MethodPost, "/api/v1/access/assignments", "minikube-bootstrap-"+actor.subject+"-"+role, "", body)
			var assignment identity.AccessAssignment
			if err != nil || result.Status != http.StatusCreated || result.Header.Get("ETag") != `"rev-1"` || json.Unmarshal(result.Body, &assignment) != nil || assignment.ID == "" || assignment.TenantID != primaryTenantID || assignment.MembershipID != membership || assignment.GroupID != "" || assignment.NamespaceID != "" || assignment.RoleDefinitionID != definitions[role] || assignment.State != identity.AssignmentActive || assignment.Revision != 1 {
				return errors.New("bootstrap companion assignment is invalid")
			}
		}
	}
	return nil
}

// runSmoke runs the governed catalogctl workflow with environment-injected fixture tokens and returns a bounded, successful JSON report.
func (h *harness) runSmoke(baseURL, runID string) ([]byte, error) {
	fmt.Printf("[%s] run governed HTTP smoke workflow\n", h.options.scenario)
	command := exec.CommandContext(h.ctx, "go", "run", "./cmd/catalogctl", "smoke", "--base-url", baseURL, "--run-id", runID, "--timeout", "4m") // #nosec G204 -- every argument is generated by the invocation-owned harness.
	command.Dir = h.options.repositoryRoot
	command.Env = append(h.environment(),
		"CATALOG_SMOKE_ADMIN_TOKEN="+h.tokens["admin"],
		"CATALOG_SMOKE_CONTRIBUTOR_TOKEN="+h.tokens["contributor"],
		"CATALOG_SMOKE_REVIEWER_TOKEN="+h.tokens["reviewer"],
		"CATALOG_SMOKE_PUBLISHER_TOKEN="+h.tokens["publisher"],
		"CATALOG_SMOKE_READER_TOKEN="+h.tokens["reader"],
		"CATALOG_SMOKE_OTHER_TENANT_TOKEN="+h.tokens["other"],
	)
	buffer := &boundedBuffer{limit: maximumCommandOutput}
	command.Stdout, command.Stderr = buffer, buffer
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("governed HTTP smoke: %w\n%s", err, h.redactDiagnostic(buffer.String()))
	}
	raw := append([]byte(nil), buffer.bytes...)
	var report map[string]any
	if err := json.Unmarshal(raw, &report); err != nil || report["status"] != "passed" {
		return nil, fmt.Errorf("invalid smoke report: %w", err)
	}
	return raw, nil
}

// verifyServedContract matches the test build identity and served OpenAPI bytes against the local packaged inputs.
func (h *harness) verifyServedContract(baseURL string) error {
	version, err := h.request(http.MethodGet, baseURL+"/version", "", "", "", nil, nil)
	if err != nil || version.Status != http.StatusOK {
		return fmt.Errorf("read served version: status %d: %w", version.Status, err)
	}
	var identity map[string]any
	expected := map[string]string{"version": "minikube", "commit": "local"}
	if stamp := h.managedBuild(); stamp != nil {
		expected = stamp
	}
	if err := json.Unmarshal(version.Body, &identity); err != nil || !matchesBuildIdentity(identity, expected) {
		return fmt.Errorf("unexpected served build identity: %s", version.Body)
	}
	contract, err := h.request(http.MethodGet, baseURL+"/openapi/control-v1.yaml", "", "", "", nil, nil)
	if err != nil || contract.Status != http.StatusOK {
		return fmt.Errorf("read served OpenAPI: status %d: %w", contract.Status, err)
	}
	want, err := os.ReadFile(filepath.Join(h.options.repositoryRoot, "api", "openapi", "control-v1.yaml"))
	if err != nil {
		return err
	}
	if !bytes.Equal(contract.Body, want) {
		return errors.New("served OpenAPI differs from the checked-in contract")
	}
	h.report.Checks = append(h.report.Checks, "served-build-and-openapi-identity")
	return nil
}

// matchesBuildIdentity preserves existing fixture labels and checks every selected managed field.
func matchesBuildIdentity(actual map[string]any, expected map[string]string) bool {
	if actual["service"] != "open-agentic-catalog" {
		return false
	}
	return matchesManagedFields(actual, expected)
}

func matchesManagedFields(actual map[string]any, expected map[string]string) bool {
	for key, value := range expected {
		if actual[key] != value {
			return false
		}
	}
	return true
}

// recreateRuntimePods deletes one owned API and worker Pod and requires the same replica count with neither deleted UID remaining.
func (h *harness) recreateRuntimePods() error {
	before := make(map[string][]podIdentity)
	for _, role := range []string{"api", "worker"} {
		pods, err := h.pods(role)
		if err != nil {
			return err
		}
		before[role] = pods
		if len(pods) == 0 {
			return fmt.Errorf("no %s Pod to replace", role)
		}
		if _, err := h.kubectl("delete one "+role+" Pod", "-n", h.catalogNamespace(), "delete", "pod", pods[0].Name, "--wait=true", "--timeout=2m"); err != nil {
			return err
		}
	}
	for _, role := range []string{"api", "worker"} {
		if _, err := h.kubectl("wait for "+role+" Pod recreation", "-n", h.catalogNamespace(), "rollout", "status", "deployment/catalog-"+role, "--timeout=2m"); err != nil {
			return err
		}
		current, err := h.pods(role)
		if err != nil {
			return err
		}
		if len(current) != len(before[role]) {
			return fmt.Errorf("%s Pod was not recreated", role)
		}
		for _, pod := range current {
			if pod.UID == before[role][0].UID {
				return fmt.Errorf("deleted %s Pod still present", role)
			}
		}
	}
	h.report.Checks = append(h.report.Checks, "api-and-worker-pod-recreation")
	return nil
}

// verifyReconfiguration checks an intentional rollout, a no-op Helm reapplication and smoke-state replay after process replacement.
func (h *harness) verifyReconfiguration(runID string, initialSmoke []byte) error {
	before := make(map[string][]podIdentity)
	for _, role := range []string{"api", "worker"} {
		pods, err := h.pods(role)
		if err != nil {
			return err
		}
		before[role] = pods
	}
	values, err := h.writeHelmValues("rollout-"+h.suffix, "catalog_api")
	if err != nil {
		return err
	}
	if _, err := h.helm("apply harmless checksum rollout", "upgrade", releaseName, h.options.repositoryRoot+"/deployments/helm/open-agentic-catalog", "--namespace", h.catalogNamespace(), "--values", values, "--wait", "--wait-for-jobs", "--timeout=4m"); err != nil {
		return err
	}
	rolled := make(map[string][]podIdentity)
	for _, role := range []string{"api", "worker"} {
		current, err := h.pods(role)
		if err != nil {
			return err
		}
		if h.options.scenario == "topology" && (len(current) != 2 || current[0].Node == current[1].Node) {
			return fmt.Errorf("%s replicas did not retain their two-node spread after reconfiguration: %#v", role, current)
		}
		rolled[role] = current
		if sameUIDs(before[role], current) {
			return fmt.Errorf("%s Pods did not roll after the deliberate annotation change", role)
		}
		h.recordPlacement("reconfigured-"+role, current)
	}
	if _, err := h.helm("reapply identical Helm values", "upgrade", releaseName, h.options.repositoryRoot+"/deployments/helm/open-agentic-catalog", "--namespace", h.catalogNamespace(), "--values", values, "--wait", "--wait-for-jobs", "--timeout=4m"); err != nil {
		return err
	}
	for _, role := range []string{"api", "worker"} {
		current, err := h.pods(role)
		if err != nil {
			return err
		}
		if !sameUIDs(rolled[role], current) {
			return fmt.Errorf("repeat-safe no-op Helm reapplication rolled %s Pods", role)
		}
	}
	// A kubectl Service port-forward is pinned to one selected Pod and exits
	// when a rollout replaces that Pod. Open a fresh client tunnel only after
	// the rollout and no-op reapply checks have completed.
	address, err := h.startPortForward("service/catalog", 8080)
	if err != nil {
		return err
	}
	baseURL := "http://" + address
	if err := h.waitHTTP(baseURL + "/readyz"); err != nil {
		return err
	}
	replayed, err := h.runSmoke(baseURL, runID)
	if err != nil {
		return err
	}
	if !sameSmokeState(initialSmoke, replayed) {
		return errors.New("governed smoke state changed after Pod recreation and Helm reconfiguration")
	}
	h.report.Checks = append(h.report.Checks, "checksum-rollout-and-no-op-reapply")
	return nil
}

// recordPlacement copies non-secret Pod placement and image evidence into the report for one scenario stage.
func (h *harness) recordPlacement(stage string, pods []podIdentity) {
	placements := make([]podPlacementReport, 0, len(pods))
	for _, pod := range pods {
		placements = append(placements, podPlacementReport{Pod: pod.Name, UID: pod.UID, Node: pod.Node, Ready: pod.Ready, ServiceAccount: pod.ServiceAccount, ImageID: pod.ImageID})
	}
	h.report.Placements[stage] = placements
}

// sameUIDs compares Pod UID collections independently of ordering without modifying either input.
func sameUIDs(left, right []podIdentity) bool {
	if len(left) != len(right) {
		return false
	}
	leftIDs, rightIDs := make([]string, len(left)), make([]string, len(right))
	for index := range left {
		leftIDs[index] = left[index].UID
	}
	for index := range right {
		rightIDs[index] = right[index].UID
	}
	sort.Strings(leftIDs)
	sort.Strings(rightIDs)
	return slices.Equal(leftIDs, rightIDs)
}

// sameSmokeState compares decoded smoke reports while ignoring check descriptions and the replaceable port-forward base URL.
func sameSmokeState(left, right []byte) bool {
	var leftReport, rightReport map[string]any
	if json.Unmarshal(left, &leftReport) != nil || json.Unmarshal(right, &rightReport) != nil {
		return false
	}
	delete(leftReport, "checks")
	delete(rightReport, "checks")
	delete(leftReport, "baseUrl")
	delete(rightReport, "baseUrl")
	leftRaw, err := json.Marshal(leftReport)
	if err != nil {
		return false
	}
	rightRaw, err := json.Marshal(rightReport)
	if err != nil {
		return false
	}
	return bytes.Equal(leftRaw, rightRaw)
}

// waitHTTP polls an owned endpoint for HTTP 200 within the harness context and the fixed readiness test deadline.
func (h *harness) waitHTTP(endpoint string) error {
	deadline := time.Now().Add(45 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		result, err := h.request(http.MethodGet, endpoint, "", "", "", nil, nil)
		if err == nil && result.Status == http.StatusOK {
			return nil
		}
		if err != nil {
			last = err
		} else {
			last = fmt.Errorf("HTTP status %d", result.Status)
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-h.ctx.Done():
			timer.Stop()
			return h.ctx.Err()
		case <-timer.C:
		}
	}
	return fmt.Errorf("timed out waiting for %s: %w", endpoint, last)
}

// request sends a fixture JSON request with optional command headers and bounds the response to 8 MiB; a supplied client owns transport policy.
func (h *harness) request(method, endpoint, token, idempotencyKey, ifMatch string, body any, client *http.Client) (httpResult, error) {
	return h.requestContext(h.ctx, method, endpoint, token, idempotencyKey, ifMatch, body, client)
}

// requestContext bounds independently cancellable fault-test requests.
func (h *harness) requestContext(ctx context.Context, method, endpoint, token, idempotencyKey, ifMatch string, body any, client *http.Client) (httpResult, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return httpResult{}, err
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return httpResult{}, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(request)
	if err != nil {
		return httpResult{}, err
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<20+1))
	if err != nil {
		return httpResult{}, err
	}
	if len(raw) > 8<<20 {
		return httpResult{}, errors.New("HTTP response exceeded 8 MiB")
	}
	return httpResult{Status: response.StatusCode, Header: response.Header.Clone(), Body: raw}, nil
}

// fixtureHTTPClient trusts only the supplied fixture CA, requires TLS 1.2 or later and refuses redirects.
func fixtureHTTPClient(certificate []byte) (*http.Client, error) {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certificate) {
		return nil, errors.New("fixture CA is invalid")
	}
	return &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

// runConcurrent releases all checks through one start barrier, waits for every result and joins failures in input order.
func runConcurrent(functions ...func() error) error {
	var wait sync.WaitGroup
	errorsByIndex := make([]error, len(functions))
	barrier := make(chan struct{})
	for index, function := range functions {
		wait.Add(1)
		go func(index int, function func() error) {
			defer wait.Done()
			<-barrier
			errorsByIndex[index] = function()
		}(index, function)
	}
	close(barrier)
	wait.Wait()
	return errors.Join(errorsByIndex...)
}

// printReport writes the accumulated scenario evidence as indented JSON.
func (h *harness) printReport() error {
	raw, err := json.MarshalIndent(h.report, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(raw))
	return nil
}
