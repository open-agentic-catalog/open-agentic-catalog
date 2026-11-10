//go:build minikube

// The Minikube supervisor owns process deadlines, cluster setup, diagnostics and cleanup.

package main

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const maximumCommandOutput = 256 << 10

type harness struct {
	vault              *vaultState
	deploymentTool     string
	catalogScope       string
	peerTLS            map[string]string
	options            options
	ctx                context.Context //nolint:containedctx // One bounded context owns the complete disposable invocation.
	temporary          string
	minikubeHome       string
	kubeconfig         string
	profile            string
	network            string
	suffix             string
	application        string
	fixture            string
	postgres           string
	pgbouncerDirect    bool
	uiImage            string
	metricsImage       string
	certificateImages  map[string]string
	certificateRoot    []byte
	semanticImage      string
	semantic           map[string]any
	clusterReady       bool
	networkReady       bool
	forceStart         bool
	portForwards       []*exec.Cmd
	forwardOutput      []*boundedBuffer
	cleanupOnce        sync.Once
	report             runReport
	secrets            fixtureSecrets
	signingKey         *rsa.PrivateKey
	tokens             map[string]string
	disruptionEvidence replicaEvidence
	webhooks           bool
	tracing            bool
	agntcy             *agntcyState
	gateway            *gatewayState
	ans                *ansState
	peerTrustConfig    map[string]any
}

type fixtureSecrets struct {
	ownerPassword      string
	apiPassword        string
	workerPassword     string
	cursorKey          string
	monitoringPassword string
}

type runReport struct {
	Vault             *vaultReport                    `json:"vault,omitempty"`
	DatabaseTLS       *databaseRenewalReport          `json:"databaseTls,omitempty"`
	Certificates      *certificateReport              `json:"certificates,omitempty"`
	Autoscaling       *autoscalingReport              `json:"autoscaling,omitempty"`
	PeerTrust         map[string]any                  `json:"peerTrust,omitempty"`
	Scenario          string                          `json:"scenario"`
	UpgradeOnly       bool                            `json:"upgradeOnly,omitempty"`
	Profile           string                          `json:"profile"`
	KubernetesVersion string                          `json:"kubernetesVersion"`
	MinikubeVersion   string                          `json:"minikubeVersion"`
	KubectlVersion    string                          `json:"kubectlVersion"`
	HelmVersion       string                          `json:"helmVersion"`
	Driver            string                          `json:"driver"`
	ContainerRuntime  string                          `json:"containerRuntime"`
	CNI               string                          `json:"cni"`
	Nodes             []nodeReport                    `json:"nodes"`
	Images            map[string]string               `json:"images"`
	StorageClass      string                          `json:"storageClass"`
	ServiceCIDR       string                          `json:"serviceCidr"`
	Placements        map[string][]podPlacementReport `json:"placements"`
	Checks            []string                        `json:"checks"`
	ReplicaBatches    []replicaBatchReport            `json:"replicaBatches,omitempty"`
	ReplicaResources  []replicaResourceReport         `json:"replicaResources,omitempty"`
	ServiceDisruption *serviceDisruptionReport        `json:"serviceDisruption,omitempty"`
	ServiceRoute      *serviceRouteReport             `json:"serviceRoute,omitempty"`
	ReplicaPressure   *replicaPressureReport          `json:"replicaPressure,omitempty"`
	Peers             map[string]any                  `json:"peers,omitempty"`
	ANS               *ansReport                      `json:"ans,omitempty"`
	Gateway           *gatewayReport                  `json:"gateway,omitempty"`
	Directory         *agntcyReport                   `json:"directory,omitempty"`
	BackupRestore     *backupRestoreReport            `json:"backupRestore,omitempty"`
}

type podPlacementReport struct {
	Pod            string `json:"pod"`
	UID            string `json:"uid"`
	Node           string `json:"node"`
	Ready          bool   `json:"ready"`
	ServiceAccount string `json:"serviceAccount"`
	ImageID        string `json:"imageId"`
}

type nodeReport struct {
	Name         string `json:"name"`
	Architecture string `json:"architecture"`
	Runtime      string `json:"runtime"`
	ControlPlane bool   `json:"controlPlane"`
	Ready        bool   `json:"ready"`
	PodCIDR      string `json:"podCidr"`
	TestWorker   bool   `json:"testWorker"`
}

type boundedBuffer struct {
	mu        sync.Mutex
	bytes     []byte
	limit     int
	truncated bool
}

// Write retains only the bounded prefix and records truncation under a mutex.
// It acknowledges all bytes so a verbose child does not fail with a short write.
func (buffer *boundedBuffer) Write(raw []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if buffer.limit <= 0 {
		buffer.limit = maximumCommandOutput
	}
	remaining := buffer.limit - len(buffer.bytes)
	if remaining > 0 {
		if len(raw) < remaining {
			remaining = len(raw)
		}
		buffer.bytes = append(buffer.bytes, raw[:remaining]...)
	}
	if remaining < len(raw) {
		buffer.truncated = true
	}
	return len(raw), nil
}

// String returns a consistent retained snapshot with an explicit truncation marker.
func (buffer *boundedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	result := string(buffer.bytes)
	if buffer.truncated {
		result += "\n[output truncated]\n"
	}
	return result
}

// newHarness reserves private local state, unique names and random credentials.
// It does not start a cluster; the caller must arrange cleanup of the returned harness.
func newHarness(ctx context.Context, configured options) (*harness, error) {
	suffix, err := randomHex(6)
	if err != nil {
		return nil, err
	}
	ownerPassword, err := randomHex(24)
	if err != nil {
		return nil, err
	}
	apiPassword, err := randomHex(24)
	if err != nil {
		return nil, err
	}
	workerPassword, err := randomHex(24)
	if err != nil {
		return nil, err
	}
	cursorKey, err := randomBase64(32)
	if err != nil {
		return nil, err
	}
	temporary, err := os.MkdirTemp("", "open-agentic-catalog-minikube-"+configured.scenario+".")
	if err != nil {
		return nil, err
	}
	prefix := "open-agentic-catalog-" + configured.scenario + "-"
	profile := prefix + suffix
	instance := &harness{
		options:      configured,
		ctx:          ctx,
		temporary:    temporary,
		minikubeHome: filepath.Join(temporary, "minikube"),
		kubeconfig:   filepath.Join(temporary, "kubeconfig"),
		profile:      profile,
		network:      profile,
		suffix:       suffix,
		application:  "open-agentic-catalog:minikube-" + suffix,
		fixture:      "open-agentic-catalog:minikube-fixture-" + suffix,
		postgres:     "open-agentic-catalog:minikube-postgres-" + suffix,
		report: runReport{
			Scenario: configured.scenario, UpgradeOnly: configured.upgradeOnly, Profile: profile, Driver: configured.driver,
			ContainerRuntime: configured.runtime, CNI: configured.clusterCNI(), Images: make(map[string]string), Placements: make(map[string][]podPlacementReport),
		},
		secrets: fixtureSecrets{ownerPassword: ownerPassword, apiPassword: apiPassword, workerPassword: workerPassword, cursorKey: cursorKey},
	}
	if configured.scenario == "vault" {
		instance.vault = &vaultState{images: make(map[string]string)}
	}
	return instance, nil
}

// environment replaces inherited cluster selectors with the invocation's private
// kubeconfig, profile and Minikube home while preserving other host tool settings.
func (h *harness) environment() []string {
	environment := make([]string, 0, len(os.Environ())+5)
	for _, entry := range os.Environ() {
		name := strings.SplitN(entry, "=", 2)[0]
		if (h.options.scenario == "agntcy" || h.options.scenario == "gateway" || h.options.scenario == "ans" || h.options.scenario == "peers") && !slices.Contains([]string{"PATH", "HOME", "LANG", "LC_ALL", "TMPDIR", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG"}, name) {
			continue
		}
		if slices.Contains([]string{"KUBECONFIG", "MINIKUBE_HOME", "MINIKUBE_PROFILE", "MINIKUBE_ACTIVE_DOCKERD"}, name) {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment,
		"KUBECONFIG="+h.kubeconfig,
		"MINIKUBE_HOME="+h.minikubeHome,
		"MINIKUBE_PROFILE="+h.profile,
		"MINIKUBE_WANTUPDATENOTIFICATION=false",
		"MINIKUBE_WANTREPORTERRORPROMPT=false",
	)
}

// run executes an owned harness command under the overall scenario context.
func (h *harness) run(label, executable string, arguments ...string) ([]byte, error) {
	return h.runWithInput(h.ctx, label, nil, executable, arguments...)
}

// runWithInput supplies private stdin and bounds combined command output. Failures
// redact known fixture secrets; callers must select reviewed tools and arguments.
func (h *harness) runWithInput(ctx context.Context, label string, input []byte, executable string, arguments ...string) ([]byte, error) {
	fmt.Printf("[%s] %s\n", h.options.scenario, label)
	command := exec.CommandContext(ctx, executable, arguments...) // #nosec G204 -- executable is a preflight-validated local tool selected by the developer.
	command.Dir = h.options.repositoryRoot
	command.Env = h.environment()
	command.Stdin = bytes.NewReader(input)
	buffer := &boundedBuffer{limit: maximumCommandOutput}
	command.Stdout = buffer
	command.Stderr = buffer
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("%s: %w\n%s", label, err, h.redactDiagnostic(buffer.String()))
	}
	return append([]byte(nil), buffer.bytes...), nil
}

// minikube pins every invocation to this harness's profile rather than the active one.
func (h *harness) minikube(label string, arguments ...string) ([]byte, error) {
	return h.run(label, h.options.minikube, append([]string{"--profile", h.profile}, arguments...)...)
}

// kubectl selects the private kubeconfig and exact owned context explicitly.
func (h *harness) kubectl(label string, arguments ...string) ([]byte, error) {
	commandArguments := make([]string, 0, 4+len(arguments))
	commandArguments = append(commandArguments, "--kubeconfig", h.kubeconfig, "--context", h.profile)
	commandArguments = append(commandArguments, arguments...)
	return h.run(label, h.options.kubectl, commandArguments...)
}

// kubectlInput passes manifests through stdin to the explicitly owned cluster context.
func (h *harness) kubectlInput(label string, input []byte, arguments ...string) ([]byte, error) {
	commandArguments := make([]string, 0, 4+len(arguments))
	commandArguments = append(commandArguments, "--kubeconfig", h.kubeconfig, "--context", h.profile)
	commandArguments = append(commandArguments, arguments...)
	return h.runWithInput(h.ctx, label, input, h.options.kubectl, commandArguments...)
}

// helm applies the same private kubeconfig/context boundary as kubectl commands.
func (h *harness) helm(label string, arguments ...string) ([]byte, error) {
	prefix := []string{"--kubeconfig", h.kubeconfig, "--kube-context", h.profile}
	return h.run(label, h.options.helm, append(arguments, prefix...)...)
}

// preflight checks local tools, version skew, host capacity and cached image pins
// before cluster creation. These are fixture prerequisites, not deployment requirements.
func (h *harness) preflight() error {
	for name, executable := range map[string]string{"minikube": h.options.minikube, "kubectl": h.options.kubectl, "helm": h.options.helm, "docker": h.options.docker} {
		if _, err := exec.LookPath(executable); err != nil {
			return fmt.Errorf("required %s executable %q is unavailable", name, executable)
		}
	}
	minikubeRaw, err := h.run("check Minikube version", h.options.minikube, "version", "--short")
	if err != nil {
		return err
	}
	minikubeVersion := strings.TrimSpace(string(minikubeRaw))
	if minikubeVersion != h.options.requiredMinikube {
		return fmt.Errorf("minikube %s is required, got %s", h.options.requiredMinikube, minikubeVersion)
	}
	h.report.MinikubeVersion = minikubeVersion

	kubectlRaw, err := h.run("check kubectl version", h.options.kubectl, "version", "--client=true", "-o", "json")
	if err != nil {
		return err
	}
	var kubectlVersion struct {
		Client struct {
			GitVersion string `json:"gitVersion"`
		} `json:"clientVersion"`
	}
	if err := json.Unmarshal(kubectlRaw, &kubectlVersion); err != nil {
		return fmt.Errorf("decode kubectl version: %w", err)
	}
	clientVersion, err := parseVersion(kubectlVersion.Client.GitVersion)
	if err != nil {
		return err
	}
	serverVersion, err := parseVersion(h.options.kubernetes)
	if err != nil {
		return err
	}
	if clientVersion.major != serverVersion.major || clientVersion.minor < serverVersion.minor-1 || clientVersion.minor > serverVersion.minor+1 {
		return fmt.Errorf("kubectl %s is outside the one-minor skew for Kubernetes %s", kubectlVersion.Client.GitVersion, h.options.kubernetes)
	}
	h.report.KubectlVersion = kubectlVersion.Client.GitVersion

	helmRaw, err := h.run("check Helm version", h.options.helm, "version", "--template", "{{.Version}}")
	if err != nil {
		return err
	}
	helmVersion := strings.TrimSpace(string(helmRaw))
	parsedHelm, err := parseVersion(helmVersion)
	if err != nil || !supportedHelmVersion(parsedHelm) {
		return fmt.Errorf("helm 3.15+ or Helm 4 is required, got %s", helmVersion)
	}
	h.report.HelmVersion = helmVersion

	dockerRaw, err := h.run("inspect Docker capacity", h.options.docker, "info", "--format", `{"os":{{json .OSType}},"architecture":{{json .Architecture}},"cpus":{{.NCPU}},"memory":{{.MemTotal}}}`)
	if err != nil {
		return err
	}
	var capacity struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		CPUs         int    `json:"cpus"`
		Memory       int64  `json:"memory"`
	}
	if err := json.Unmarshal(dockerRaw, &capacity); err != nil {
		return fmt.Errorf("decode Docker capacity: %w", err)
	}
	minimumCPUs, minimumMemory := 4, int64(6<<30)
	minimumDisk := uint64(8 << 30)
	if h.options.scenario == "topology" || h.options.scenario == "semantic" || h.options.scenario == "pgbouncer" || h.options.scenario == "agntcy" || h.options.scenario == "gateway" || h.options.scenario == "ans" || h.options.scenario == "peers" || h.options.scenario == "vault" {
		minimumCPUs, minimumMemory, minimumDisk = 8, 12<<30, 12<<30
	}
	if capacity.OS != "linux" || capacity.Architecture != "x86_64" && capacity.Architecture != "amd64" || capacity.CPUs < minimumCPUs || capacity.Memory < minimumMemory {
		return fmt.Errorf("docker host lacks the maintained %s capacity: os=%s arch=%s cpus=%d memory=%d", h.options.scenario, capacity.OS, capacity.Architecture, capacity.CPUs, capacity.Memory)
	}
	var filesystem syscall.Statfs_t
	if err := syscall.Statfs(h.options.repositoryRoot, &filesystem); err != nil {
		return fmt.Errorf("inspect free disk: %w", err)
	}
	if filesystem.Bsize <= 0 {
		return fmt.Errorf("filesystem reported invalid block size %d", filesystem.Bsize)
	}
	blockSize := uint64(filesystem.Bsize) // #nosec G115 -- positivity is checked immediately above.
	if filesystem.Bavail > ^uint64(0)/blockSize || filesystem.Blocks > ^uint64(0)/blockSize {
		return errors.New("filesystem capacity exceeds the supported integer range")
	}
	available := filesystem.Bavail * blockSize
	total := filesystem.Blocks * blockSize
	if available < minimumDisk {
		return fmt.Errorf("%s requires at least %d GiB free disk; found %.1f GiB", h.options.scenario, minimumDisk>>30, float64(available)/(1<<30))
	}
	// Minikube rejects a filesystem at 95 percent utilization even when the
	// remaining bytes exceed this harness's bounded requirement. In that one
	// case, --force bypasses the percentage heuristic after our absolute-byte
	// check; every other driver, platform, version, and capacity check remains
	// explicit above.
	h.forceStart = total > 0 && (total-available)*100/total >= 95
	for label, reference := range map[string]string{"Minikube base image": h.options.baseImage, "PostgreSQL image": h.options.postgresImage} {
		if _, err := h.run("verify local "+label, h.options.docker, "image", "inspect", reference); err != nil {
			return fmt.Errorf("%s must already exist locally: %w", label, err)
		}
	}
	profiles, err := h.run("verify isolated profile name", h.options.minikube, "profile", "list", "-o", "json")
	if err == nil && bytes.Contains(profiles, []byte(`"Name":"`+h.profile+`"`)) {
		return fmt.Errorf("refusing pre-existing Minikube profile %s", h.profile)
	}
	if h.options.scenario == "ans" {
		if err := h.ansPreflight(); err != nil {
			return err
		}
	}
	if h.options.scenario == "gateway" {
		if err := h.gatewayPreflight(); err != nil {
			return err
		}
	}
	if h.options.scenario == "agntcy" {
		if err := h.agntcyPreflight(); err != nil {
			return err
		}
	}
	if h.options.scenario == "autoscaling" {
		if _, err := h.run("verify cached Metrics Server image", h.options.docker, "image", "inspect", metricsServerImage); err != nil {
			return fmt.Errorf("cache the selected Metrics Server image before running: %w", err)
		}
	}
	if h.options.scenario == "cert-manager" {
		if err := h.checkCertificateInputs(); err != nil {
			return err
		}
	}
	if h.vault != nil {
		if err := h.checkVaultInputs(); err != nil {
			return err
		}
	}
	h.report.Checks = append(h.report.Checks, "preflight")
	return nil
}

// buildImages creates unique local application/fixture tags, aliases the pinned
// database image and records resolved identities without publishing any artifact.
func (h *harness) buildImages() error {
	if h.options.scenario == "cert-manager" {
		if err := h.prepareCertificateImages(); err != nil {
			return err
		}
	}
	if h.options.scenario == "autoscaling" {
		h.metricsImage = "open-agentic-catalog:minikube-metrics-" + h.suffix
		if _, err := h.run("alias pinned Metrics Server for offline loading", h.options.docker, "image", "tag", metricsServerImage, h.metricsImage); err != nil {
			return err
		}
		raw, err := h.run("record Metrics Server image config", h.options.docker, "image", "inspect", "--format", "{{.Id}}", h.metricsImage)
		if err != nil {
			return err
		}
		h.report.Images["metrics-server-config"] = strings.TrimSpace(string(raw))
	}

	if h.options.scenario == "basic" || h.options.scenario == "topology" || h.options.scenario == "pgbouncer" || h.options.scenario == "peers" || h.options.scenario == "backup-restore" || h.options.scenario == "autoscaling" || h.options.scenario == "cert-manager" || h.options.scenario == "vault" {
		if _, err := h.run("build deployment coordinator", "go", "build", "-o", filepath.Join(h.temporary, "catalog-deploy"), "./test/deployment"); err != nil {
			return err
		}
	}
	buildArgs := []string{"build", "--pull=false", "--target", "runtime", "--build-arg", "VERSION=minikube", "--build-arg", "COMMIT=local", "--tag", h.application, "--file", "Containerfile", "."}
	if stamp := h.managedBuild(); stamp != nil {
		buildArgs = agntcyBuildArguments(h.application, stamp)
	}
	if _, err := h.run("build unique Catalog image", h.options.docker, buildArgs...); err != nil {
		return err
	}
	if _, err := h.run("build unique local fixture image", h.options.docker, "build", "--pull=false", "--tag", h.fixture, "--file", "test/functional/Containerfile", "."); err != nil {
		return err
	}
	// containerd does not consistently retain a Docker digest reference loaded
	// through Minikube. Alias the already verified digest locally so the Pod can
	// use imagePullPolicy Never while provenance remains tied to the pinned
	// source bytes.
	if _, err := h.run("alias pinned PostgreSQL image for offline loading", h.options.docker, "image", "tag", h.options.postgresImage, h.postgres); err != nil {
		return err
	}
	for label, reference := range map[string]string{"application": h.application, "fixture": h.fixture, "postgres": h.postgres} {
		raw, err := h.run("record "+label+" image", h.options.docker, "image", "inspect", "--format", "{{.Id}}", reference)
		if err != nil {
			return err
		}
		h.report.Images[label] = strings.TrimSpace(string(raw))
	}
	h.report.Images["postgres-source"] = h.options.postgresImage
	h.report.Checks = append(h.report.Checks, "unique-images-built")
	if h.ans != nil {
		return h.buildANSRouter()
	}
	if h.agntcy != nil {
		return h.buildAGNTCYRouter()
	}
	return nil
}

// MINIKUBE_HOME names the parent of Minikube's .minikube directory.
// Copy the archive before Docker can create a root-owned directory at its path.
func (h *harness) preparePreload() error {
	if err := os.MkdirAll(filepath.Join(h.minikubeHome, ".minikube", "cache", "preloaded-tarball"), 0o700); err != nil {
		return err
	}
	if h.options.preloadArchive != "" {
		destination := filepath.Join(h.minikubeHome, ".minikube", "cache", "preloaded-tarball", filepath.Base(h.options.preloadArchive))
		if err := copyFile(h.options.preloadArchive, destination, 0o600); err != nil {
			return fmt.Errorf("copy Minikube preload archive: %w", err)
		}
	}
	return nil
}

// startCluster creates an invocation-owned network/profile and waits for nodes.
// Ownership flags are set as resources appear so partial setup can be cleaned up.
func (h *harness) startCluster() error {
	if err := h.preparePreload(); err != nil {
		return err
	}
	if _, err := h.run("verify invocation network is absent", h.options.docker, "network", "inspect", h.network); err == nil {
		return fmt.Errorf("refusing pre-existing Docker network %s", h.network)
	}
	if _, err := h.run("create invocation network", h.options.docker, "network", "create", "--driver", "bridge", h.network); err != nil {
		return err
	}
	h.networkReady = true
	nodes, cpus, memory, cni := "1", "4", "6144mb", h.options.clusterCNI()
	if h.options.scenario == "topology" {
		nodes, cpus, memory, cni = "3", "2", "3072mb", "calico"
	}
	if h.options.scenario == "pgbouncer" {
		cni = "calico"
	}
	if h.options.scenario == "semantic" {
		cpus, memory, cni = "8", "12288mb", "calico"
	}
	if h.options.scenario == "agntcy" || h.options.scenario == "gateway" || h.options.scenario == "ans" || h.options.scenario == "vault" {
		cpus, memory, cni = "8", "12288mb", "calico"
	}
	if h.options.scenario == "peers" {
		cpus, memory = "8", "12288mb"
	}
	arguments := []string{
		"start", "--driver", h.options.driver, "--container-runtime", h.options.runtime,
		"--kubernetes-version", h.options.kubernetes, "--base-image", h.options.baseImage,
		"--network", h.network, "--nodes", nodes, "--cpus", cpus, "--memory", memory,
		"--disk-size", "8g", "--cni", cni, "--service-cluster-ip-range", localServiceCIDR, "--embed-certs", "--keep-context=false",
		"--delete-on-failure", "--disable-metrics", "--wait=all", "--wait-timeout=7m",
	}
	if h.forceStart {
		arguments = append(arguments, "--force")
		h.report.Checks = append(h.report.Checks, "absolute-free-disk-preflight")
	}
	if _, err := h.minikube("create isolated Minikube cluster", arguments...); err != nil {
		return err
	}
	h.clusterReady = true
	if _, err := h.kubectl("wait for every node", "wait", "--for=condition=Ready", "node", "--all", "--timeout=3m"); err != nil {
		return err
	}
	return h.inspectCluster()
}

// inspectCluster validates the resolved node inventory, runtime and readiness
// against this scenario's topology before workload qualification continues.
func (h *harness) inspectCluster() error {
	raw, err := h.kubectl("inspect resolved cluster identity", "get", "nodes", "-o", "json")
	if err != nil {
		return err
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec struct {
				PodCIDR string `json:"podCIDR"`
			} `json:"spec"`
			Status struct {
				NodeInfo struct {
					Architecture     string `json:"architecture"`
					ContainerRuntime string `json:"containerRuntimeVersion"`
					KubeletVersion   string `json:"kubeletVersion"`
				} `json:"nodeInfo"`
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return fmt.Errorf("decode node inventory: %w", err)
	}
	wantNodes := 1
	if h.options.scenario == "topology" {
		wantNodes = 3
	}
	if len(list.Items) != wantNodes {
		return fmt.Errorf("cluster has %d nodes, want %d", len(list.Items), wantNodes)
	}
	h.report.Nodes = nil
	for _, item := range list.Items {
		node := nodeReport{Name: item.Metadata.Name, Architecture: item.Status.NodeInfo.Architecture, Runtime: item.Status.NodeInfo.ContainerRuntime, PodCIDR: item.Spec.PodCIDR}
		_, node.ControlPlane = item.Metadata.Labels["node-role.kubernetes.io/control-plane"]
		_, node.TestWorker = item.Metadata.Labels["open-agentic-catalog.github.io/test-worker"]
		for _, condition := range item.Status.Conditions {
			if condition.Type == "Ready" && condition.Status == "True" {
				node.Ready = true
			}
		}
		if item.Status.NodeInfo.KubeletVersion != h.options.kubernetes || node.Architecture != "amd64" || !strings.HasPrefix(node.Runtime, h.options.runtime+"://") || !node.Ready || node.PodCIDR == "" {
			return fmt.Errorf("node %s does not match the maintained identity: %#v, kubelet=%s", node.Name, node, item.Status.NodeInfo.KubeletVersion)
		}
		h.report.Nodes = append(h.report.Nodes, node)
	}
	versionRaw, err := h.kubectl("verify Kubernetes server version", "version", "-o", "json")
	if err != nil {
		return err
	}
	var versions struct {
		Server struct {
			GitVersion string `json:"gitVersion"`
		} `json:"serverVersion"`
	}
	if err := json.Unmarshal(versionRaw, &versions); err != nil || versions.Server.GitVersion != h.options.kubernetes {
		return fmt.Errorf("kubernetes server version = %q, want %q", versions.Server.GitVersion, h.options.kubernetes)
	}
	h.report.KubernetesVersion = versions.Server.GitVersion
	storageRaw, err := h.kubectl("record default storage class", "get", "storageclass", "-o", `jsonpath={range .items[?(@.metadata.annotations.storageclass\.kubernetes\.io/is-default-class=="true")]}{.metadata.name}{end}`)
	if err != nil {
		return err
	}
	h.report.StorageClass = strings.TrimSpace(string(storageRaw))
	if h.report.StorageClass == "" {
		return errors.New("cluster has no default storage class")
	}
	serviceRaw, err := h.kubectl("verify pinned Service CIDR", "-n", "default", "get", "service/kubernetes", "-o", "jsonpath={.spec.clusterIP}")
	if err != nil {
		return err
	}
	serviceAddress, err := netip.ParseAddr(strings.TrimSpace(string(serviceRaw)))
	servicePrefix := netip.MustParsePrefix(localServiceCIDR)
	if err != nil || !servicePrefix.Contains(serviceAddress) {
		return fmt.Errorf("kubernetes Service address %q is outside pinned range %s", strings.TrimSpace(string(serviceRaw)), localServiceCIDR)
	}
	h.report.ServiceCIDR = localServiceCIDR
	if h.options.clusterCNI() == "calico" {
		if _, err := h.kubectl("wait for Calico", "-n", "kube-system", "rollout", "status", "daemonset/calico-node", "--timeout=4m"); err != nil {
			return err
		}
		// Node readiness does not imply the separate controller has started.
		// Wait for its image status before checking the exact resolved image IDs.
		if _, err := h.kubectl("wait for Calico controllers", "-n", "kube-system", "rollout", "status", "deployment/calico-kube-controllers", "--timeout=4m"); err != nil {
			return err
		}
		if err := h.verifyCalicoImages(); err != nil {
			return err
		}
		if h.options.scenario == "topology" {
			if err := h.labelTestWorkers(); err != nil {
				return err
			}
		}
	}
	h.report.Checks = append(h.report.Checks, "cluster-identity-verified")
	return nil
}

// verifyCalicoImages checks actual CNI container identities and Pod counts rather
// than accepting requested tags as evidence of the resolved network-policy fixture.
func (h *harness) verifyCalicoImages() error {
	raw, err := h.kubectl("verify resolved Calico images", "-n", "kube-system", "get", "pods", "-l", "k8s-app in (calico-node,calico-kube-controllers)", "-o", "json")
	if err != nil {
		return err
	}
	images, err := verifyCalicoImageInventory(raw, h.report.Nodes)
	if err != nil {
		return err
	}
	for key, imageID := range images {
		h.report.Images[key] = imageID
	}
	h.report.Checks = append(h.report.Checks, "pinned-calico-images-verified")
	return nil
}

// verifyCalicoImageInventory accepts only exact reviewed identities on amd64
// nodes. No tag, repository alias or other platform manifest establishes identity.
func verifyCalicoImageInventory(raw []byte, nodes []nodeReport) (map[string]string, error) {
	if len(nodes) == 0 {
		return nil, errors.New("calico image verification requires the node inventory")
	}
	for _, node := range nodes {
		if node.Architecture != "amd64" {
			return nil, errors.New("calico image verification requires every node to use amd64")
		}
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec struct {
				InitContainers []struct {
					Name  string `json:"name"`
					Image string `json:"image"`
				} `json:"initContainers"`
				Containers []struct {
					Name  string `json:"name"`
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"spec"`
			Status struct {
				InitContainerStatuses []struct {
					Name    string `json:"name"`
					ImageID string `json:"imageID"`
				} `json:"initContainerStatuses"`
				ContainerStatuses []struct {
					Name    string `json:"name"`
					ImageID string `json:"imageID"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("decode Calico image inventory: %w", err)
	}
	seen := make(map[string]int)
	observed := make(map[string]string)
	nodePods, controllerPods := 0, 0
	for _, pod := range list.Items {
		switch pod.Metadata.Labels["k8s-app"] {
		case "calico-node":
			nodePods++
		case "calico-kube-controllers":
			controllerPods++
		default:
			return nil, fmt.Errorf("unexpected Pod in Calico inventory: %v", pod.Metadata.Labels)
		}
		statuses := make(map[string]string)
		for _, status := range append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...) {
			statuses[status.Name] = status.ImageID
		}
		for _, container := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
			expected, ok := expectedCalicoImages[container.Image]
			actual := statuses[container.Name]
			if !ok || actual != expected.indexID && actual != expected.amd64ID {
				return nil, fmt.Errorf("calico container %s resolved %q as %q", container.Name, container.Image, actual)
			}
			seen[container.Image]++
			observed[container.Image] = actual
		}
	}
	if nodePods != len(nodes) || controllerPods != 1 {
		return nil, fmt.Errorf("calico Pod inventory has %d node Pods and %d controllers for %d nodes", nodePods, controllerPods, len(nodes))
	}
	images := make(map[string]string)
	for reference, expected := range expectedCalicoImages {
		if seen[reference] == 0 {
			return nil, fmt.Errorf("expected Calico image %s was not resolved", reference)
		}
		name := strings.TrimPrefix(strings.Split(reference, ":")[0], "quay.io/calico/")
		images["calico-"+name] = observed[reference]
		images["calico-"+name+"-index-pin"] = expected.indexID
		images["calico-"+name+"-amd64-pin"] = expected.amd64ID
	}
	return images, nil
}

// labelTestWorkers marks exactly two non-control-plane nodes for topology placement.
func (h *harness) labelTestWorkers() error {
	workers := 0
	for index := range h.report.Nodes {
		if h.report.Nodes[index].ControlPlane {
			continue
		}
		if _, err := h.kubectl("label test worker "+h.report.Nodes[index].Name, "label", "node", h.report.Nodes[index].Name, "open-agentic-catalog.github.io/test-worker=true", "--overwrite"); err != nil {
			return err
		}
		h.report.Nodes[index].TestWorker = true
		workers++
	}
	if workers != 2 {
		return fmt.Errorf("topology cluster has %d test workers, want 2", workers)
	}
	return nil
}

// loadImages imports only the invocation's local image tags into its Minikube profile.
func (h *harness) loadImages() error {
	if h.vault != nil {
		for name, image := range h.vault.images {
			if _, err := h.minikube("load pinned Vault "+name, "image", "load", "--daemon=true", "--overwrite=true", image); err != nil {
				return err
			}
		}
	}
	for name, image := range h.certificateImages {
		if _, err := h.minikube("load pinned "+name, "image", "load", "--daemon=true", "--overwrite=true", image); err != nil {
			return err
		}
	}
	if h.options.scenario == "autoscaling" {
		if _, err := h.minikube("load pinned Metrics Server", "image", "load", "--daemon=true", "--overwrite=true", h.metricsImage); err != nil {
			return err
		}
		h.report.Images["metrics-server-source"] = metricsServerImage
	}
	if h.ans != nil {
		if err := h.loadANSImages(); err != nil {
			return err
		}
	}
	if h.gateway != nil {
		if err := h.loadGatewayImages(); err != nil {
			return err
		}
	}
	if h.agntcy != nil {
		if err := h.loadAGNTCYImages(); err != nil {
			return err
		}
	}
	if h.options.scenario == "pgbouncer" {
		if _, err := h.minikube("load digest-pinned PgBouncer", "image", "load", "--daemon=true", "--overwrite=true", pgbouncerImage); err != nil {
			return err
		}
		h.report.Images["pgbouncer-source"] = pgbouncerImage
	}
	for label, reference := range map[string]string{"Catalog": h.application, "fixture": h.fixture, "PostgreSQL": h.postgres} {
		if _, err := h.minikube("load "+label+" image without pulling", "image", "load", "--daemon=true", "--overwrite=true", reference); err != nil {
			return err
		}
	}
	h.report.Checks = append(h.report.Checks, "images-preloaded")
	return nil
}

// startPortForward starts and tracks an owned loopback-only kubectl forward, then
// waits for its listener. Even a failed startup remains registered for cleanup.
func (h *harness) startPortForward(resource string, remotePort int) (string, error) {
	return h.startPortForwardIn(h.catalogNamespace(), resource, remotePort)
}

// startPortForwardIn keeps namespace selection explicit for owned peer fixtures.
func (h *harness) startPortForwardIn(selectedNamespace, resource string, remotePort int) (string, error) {
	listener, err := (&net.ListenConfig{}).Listen(h.ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	listenerAddress, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return "", fmt.Errorf("loopback listener reported unexpected address type %T", listener.Addr())
	}
	port := listenerAddress.Port
	if err := listener.Close(); err != nil {
		return "", err
	}
	arguments := []string{"--kubeconfig", h.kubeconfig, "--context", h.profile, "-n", selectedNamespace, "port-forward", "--address", "127.0.0.1", resource, strconv.Itoa(port) + ":" + strconv.Itoa(remotePort)}
	command := exec.CommandContext(h.ctx, h.options.kubectl, arguments...) // #nosec G204 -- kubectl is preflight validated and every argument targets the owned context.
	command.Dir = h.options.repositoryRoot
	command.Env = h.environment()
	buffer := &boundedBuffer{limit: 32 << 10}
	command.Stdout, command.Stderr = buffer, buffer
	if err := command.Start(); err != nil {
		return "", err
	}
	h.portForwards = append(h.portForwards, command)
	h.forwardOutput = append(h.forwardOutput, buffer)
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	deadline := time.Now().Add(15 * time.Second)
	dialer := &net.Dialer{Timeout: 250 * time.Millisecond}
	for time.Now().Before(deadline) {
		connection, dialErr := dialer.DialContext(h.ctx, "tcp", address)
		if dialErr == nil {
			_ = connection.Close()
			return address, nil
		}
		if command.ProcessState != nil && command.ProcessState.Exited() {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("port-forward %s did not start: %s", resource, h.redactDiagnostic(buffer.String()))
}

// stopPortForwards interrupts tracked children, escalates to kill after a bound,
// waits for termination and releases the retained command/output references.
func (h *harness) stopPortForwards() error {
	var result error
	for _, command := range h.portForwards {
		if command.Process == nil {
			continue
		}
		_ = command.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func(command *exec.Cmd) {
			_ = command.Wait()
			close(done)
		}(command)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				result = errors.Join(result, fmt.Errorf("kill port-forward process %d: %w", command.Process.Pid, err))
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				result = errors.Join(result, fmt.Errorf("port-forward process %d did not stop", command.Process.Pid))
			}
		}
	}
	h.portForwards = nil
	h.forwardOutput = nil
	return result
}

// diagnostics captures selected cluster state with a separate failure-time budget
// and known-secret redaction; it does nothing before cluster startup succeeds.
func (h *harness) diagnostics() {
	if !h.clusterReady {
		return
	}
	diagnosticContext, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for _, command := range [][]string{
		{"-n", h.catalogNamespace(), "get", "pods,jobs,deployments,statefulsets,services,endpoints,poddisruptionbudgets", "-o", "wide"},
		{"-n", h.catalogNamespace(), "get", "events", "--sort-by=.metadata.creationTimestamp"},
		{"-n", h.catalogNamespace(), "describe", "pods"},
		{"-n", h.catalogNamespace(), "describe", "jobs"},
		{"get", "nodes", "-o", "wide"},
	} {
		prefix := []string{"--kubeconfig", h.kubeconfig, "--context", h.profile}
		output, err := h.runWithInput(diagnosticContext, "capture bounded failure diagnostics", nil, h.options.kubectl, append(prefix, command...)...)
		if len(output) > 0 {
			fmt.Fprintln(os.Stderr, h.redactDiagnostic(string(output)))
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, h.redactDiagnostic(err.Error()))
		}
	}
	components := []string{"postgres", "database-bootstrap", "identity", "api", "worker", "ui", "migration"}
	if h.options.scenario == "backup-restore" {
		components = append(components, restoredPostgres)
	}
	for _, component := range components {
		for _, previous := range []bool{false, true} {
			prefix := []string{"--kubeconfig", h.kubeconfig, "--context", h.profile, "-n", h.catalogNamespace(), "logs", "--tail=100", "--prefix", "-l", "app.kubernetes.io/component=" + component, "--all-containers=true"}
			label := "capture " + component + " logs"
			if previous {
				prefix = append(prefix, "--previous=true")
				label = "capture previous " + component + " logs"
			}
			output, _ := h.runWithInput(diagnosticContext, label, nil, h.options.kubectl, prefix...)
			if len(output) > 0 {
				fmt.Fprintln(os.Stderr, h.redactDiagnostic(string(output)))
			}
		}
	}
}

// redactDiagnostic removes exact known fixture credentials and tokens. It is not
// a general-purpose sanitizer for arbitrary customer data or external responses.
func (h *harness) redactDiagnostic(value string) string {
	if h.vault != nil {
		for _, secret := range h.vault.private {
			if secret != "" {
				value = strings.ReplaceAll(value, secret, "[redacted]")
			}
		}
	}
	if h.ans != nil {
		for _, secret := range h.ans.secrets {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	if h.gateway != nil {
		for _, secret := range h.gateway.secrets {
			if secret != "" {
				value = strings.ReplaceAll(value, secret, "[redacted]")
			}
		}
	}
	if h.agntcy != nil {
		for _, secret := range h.agntcy.secrets {
			if secret != "" {
				value = strings.ReplaceAll(value, secret, "[redacted]")
			}
		}
	}
	for _, secret := range []string{h.secrets.ownerPassword, h.secrets.apiPassword, h.secrets.workerPassword, h.secrets.cursorKey, h.secrets.monitoringPassword} {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[redacted]")
		}
	}
	for _, token := range h.tokens {
		if token != "" {
			value = strings.ReplaceAll(value, token, "[redacted]")
		}
	}
	return strings.TrimSpace(value)
}

// cleanup attempts owned profile/network/image and temporary-file removal once,
// continuing after individual failures. It validates the temporary target before
// recursive removal; the first caller must retain any cleanup error it receives.
func (h *harness) cleanup() error {
	var result error
	h.cleanupOnce.Do(func() {
		result = errors.Join(result, h.cleanupVault())
		result = errors.Join(result, h.stopPortForwards())
		if h.clusterReady || h.networkReady {
			output, err := h.runCleanupCommand(3*time.Minute, h.options.minikube, "--profile", h.profile, "delete", "--purge=false")
			if err != nil && !strings.Contains(string(output), "does not exist") {
				result = errors.Join(result, fmt.Errorf("delete profile %s: %w", h.profile, err))
			}
		}
		if h.networkReady {
			output, err := h.runCleanupCommand(30*time.Second, h.options.docker, "network", "rm", h.network)
			if err != nil && !strings.Contains(string(output), "No such network") {
				result = errors.Join(result, fmt.Errorf("delete network %s: %w", h.network, err))
			}
		}
		ownedImages := []string{h.application, h.fixture, h.postgres}
		if h.vault != nil {
			for _, image := range h.vault.images {
				ownedImages = append(ownedImages, image)
			}
		}
		for _, image := range h.certificateImages {
			ownedImages = append(ownedImages, image)
		}
		if h.metricsImage != "" {
			ownedImages = append(ownedImages, h.metricsImage)
		}
		for _, image := range ownedImages {
			output, err := h.runCleanupCommand(30*time.Second, h.options.docker, "image", "rm", image)
			if err != nil && !strings.Contains(string(output), "No such image") {
				result = errors.Join(result, fmt.Errorf("delete image %s: %w", image, err))
			}
		}
		if h.uiImage != "" {
			_, err := h.runCleanupCommand(30*time.Second, h.options.docker, "image", "rm", h.uiImage)
			result = errors.Join(result, err)
		}
		if h.semanticImage != "" {
			_, err := h.runCleanupCommand(30*time.Second, h.options.docker, "image", "rm", h.semanticImage)
			result = errors.Join(result, err)
		}
		if h.ans != nil {
			if h.options.ansTrust {
				output, err := h.runCleanupCommand(30*time.Second, h.options.docker, "rm", "-f", "ans-command-"+h.suffix)
				if err != nil && !strings.Contains(string(output), "No such container") {
					result = errors.Join(result, err)
				}
			}
			for _, ref := range []string{h.ans.alias.Reference, h.ans.router} {
				output, err := h.runCleanupCommand(30*time.Second, h.options.docker, "image", "rm", ref)
				if err != nil && !strings.Contains(string(output), "No such image") {
					result = errors.Join(result, err)
				}
			}
		}
		if h.gateway != nil {
			for _, image := range h.gateway.aliases {
				output, err := h.runCleanupCommand(30*time.Second, h.options.docker, "image", "rm", image.Reference)
				if err != nil && !strings.Contains(string(output), "No such image") {
					result = errors.Join(result, err)
				}
			}
		}
		if h.agntcy != nil {
			for _, image := range h.agntcy.runtimeImages.Images {
				output, err := h.runCleanupCommand(30*time.Second, h.options.docker, "image", "rm", image.Reference)
				if err != nil && !strings.Contains(string(output), "No such image") {
					result = errors.Join(result, err)
				}
			}
			output, err := h.runCleanupCommand(30*time.Second, h.options.docker, "image", "rm", h.agntcy.router)
			if err != nil && !strings.Contains(string(output), "No such image") {
				result = errors.Join(result, err)
			}
		}
		clean := filepath.Clean(h.temporary)
		if !strings.HasPrefix(filepath.Base(clean), "open-agentic-catalog-minikube-") || filepath.Dir(clean) != os.TempDir() {
			result = errors.Join(result, fmt.Errorf("refusing unsafe temporary cleanup path %s", clean))
		} else if err := os.RemoveAll(clean); err != nil {
			result = errors.Join(result, err)
		}
	})
	return result
}

// runCleanupCommand uses an independent timeout so cancellation of the workload
// cannot prevent cleanup. Callers must supply exact invocation-owned targets.
func (h *harness) runCleanupCommand(timeout time.Duration, executable string, arguments ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, executable, arguments...) // #nosec G204 -- callers provide a preflight-validated tool and exact invocation-owned targets.
	command.Env = h.environment()
	command.Dir = h.options.repositoryRoot
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		err = errors.Join(err, ctx.Err())
	}
	return output, err
}

// copyFile creates an owned destination exclusively and propagates copy/close errors.
// An incomplete destination remains inside the harness directory for final cleanup.
func copyFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source) // #nosec G304 -- the validated operator-selected preload file is copied into the invocation-owned home.
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode) // #nosec G304 -- destination is constructed under the invocation-owned temporary Minikube home.
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}
