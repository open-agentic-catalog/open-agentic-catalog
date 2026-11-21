//go:build minikube

package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed fixtures/argocd/pins.json
var argoPins []byte

func (h *harness) checkArgoInputs() error {
	existing, err := h.run("verify private node name is unused", h.options.docker, "container", "ls", "-a", "--filter", "name=^/"+h.profile+"$", "--format", "{{.Names}}")
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(existing))) != 0 {
		return errors.New("refusing existing Argo CD profile container")
	}

	preload, err := h.run("verify prepared Argo CD assets and local image closure", environment("CATALOG_ARGOCD_PYTHON", "python3"), "-B", "test/minikube/fixtures/argocd/install.py", "--check")
	if err != nil {
		return err
	}
	h.options.preloadArchive = strings.TrimSpace(string(preload))
	raw, err := h.run("resolve Catalog build identity", "python3", "-B", "test/packaging/versioning.py", "metadata")
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, &h.argoBuild)
}

func (h *harness) prepareArgoImages() error {
	var pins struct {
		Images map[string]string `json:"images"`
	}
	if err := json.Unmarshal(argoPins, &pins); err != nil {
		return err
	}
	h.gitopsImages = map[string]string{}
	for name, ref := range pins.Images {
		alias := "open-agentic-catalog:minikube-argocd-" + name + "-" + h.suffix
		h.gitopsImages[name] = alias
		if _, err := h.run("tag verified "+name, h.options.docker, "tag", ref, alias); err != nil {
			return err
		}
		h.report.Images[name+"-source"] = ref
	}
	target := "open-agentic-catalog:minikube-argocd-target-" + h.suffix
	h.gitopsImages["target"] = target
	stamp := h.argoTargetBuild()
	if _, err := h.run("build same-source Catalog target", h.options.docker, agntcyBuildArguments(target, stamp)...); err != nil {
		return err
	}
	for name, alias := range h.gitopsImages {
		raw, err := h.run("record "+name+" image", h.options.docker, "image", "inspect", "--format", "{{.Id}}", alias)
		if err != nil {
			return err
		}
		h.report.Images[name+"-config"] = strings.TrimSpace(string(raw))
	}
	return nil
}

func (h *harness) argoTargetBuild() map[string]string {
	return map[string]string{"version": h.argoBuild["version"] + ".argocd", "commit": h.argoBuild["commit"], "sourceState": h.argoBuild["sourceState"]}
}

func (h *harness) argoPhase(phase, values string) error {
	images, err := json.Marshal(map[string]map[string]string{"aliases": h.gitopsImages, "ids": h.report.Images})
	if err != nil {
		return err
	}
	_, err = h.run("Argo CD "+phase, environment("CATALOG_ARGOCD_PYTHON", "python3"), "-B", "test/minikube/fixtures/argocd/campaign.py", phase, h.profile, h.catalogNamespace(), h.temporary, h.options.kubectl, values, string(images), h.argoAddress)
	return err
}

func (h *harness) installArgo(values string) error {
	if err := h.applyNetworkPolicies(); err != nil {
		return err
	}
	tls, err := serviceTLSMaterial(time.Now(), "OAC Argo fixture", "argocd", []string{"argocd-server", "catalog-git"}, net.ParseIP("127.0.0.1"))
	if err != nil {
		return err
	}
	for name, data := range tls {
		if err = os.WriteFile(filepath.Join(h.temporary, name), []byte(data), 0o600); err != nil {
			return err
		}
	}
	if err = h.argoPhase("bootstrap", values); err != nil {
		return err
	}
	h.argoAddress, err = h.startPortForwardIn("argocd", "service/argocd-server", 443)
	if err != nil {
		return err
	}
	return h.argoPhase("install", values)
}

func (h *harness) verifyArgo(smoke []byte, baseURL string) error {
	var evidence struct {
		Resources []struct {
			ReleaseID string `json:"releaseId"`
		} `json:"resources"`
	}
	if json.Unmarshal(smoke, &evidence) != nil || len(evidence.Resources) == 0 || evidence.Resources[0].ReleaseID == "" {
		return errors.New("missing governed Catalog release")
	}
	release := evidence.Resources[0].ReleaseID
	before, err := h.readRetainedReleaseAt(baseURL, release)
	if err != nil {
		return err
	}
	if h.argoLab {
		return h.argoLabReady(baseURL)
	}
	for _, phase := range []string{"upgrade", "faults", "restore"} {
		if err = h.argoPhase(phase, ""); err != nil {
			return err
		}
		address, err := h.startPortForward("service/catalog", 8080)
		if err != nil {
			return err
		}
		targetURL := "http://" + address
		after, err := h.readRetainedReleaseAt(targetURL, release)
		if err != nil {
			return err
		}
		if after != before {
			return errors.New("governed release changed across Argo CD " + phase)
		}
		version, err := h.request(http.MethodGet, targetURL+"/version", "", "", "", nil, nil)
		if err != nil {
			return err
		}
		var stamp map[string]any
		if version.Status != http.StatusOK || json.Unmarshal(version.Body, &stamp) != nil || !matchesBuildIdentity(stamp, h.argoTargetBuild()) {
			return errors.New("unexpected embedded Catalog build identity after Argo CD sync")
		}
		h.report.Checks = append(h.report.Checks, "argocd-"+phase+"-retained-release-sha256-"+fmt.Sprintf("%x", before))
	}
	if err = h.argoPhase("restore-hpa", ""); err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(h.temporary, "argo-evidence.json")) // #nosec G304 -- invocation-owned phase report.
	if err != nil || !json.Valid(raw) {
		return errors.New("missing Argo CD phase evidence")
	}
	h.report.ArgoCD = raw
	return nil
}

func (h *harness) argoLabReady(baseURL string) error {
	dir := filepath.Join(h.options.repositoryRoot, "output", "argocd-lab")
	uid, err := h.kubectl("identify lab cluster", "get", "namespace/kube-system", "-o", "jsonpath={.metadata.uid}")
	if err != nil {
		return err
	}
	data, err := json.Marshal(map[string]string{"profile": h.profile, "work": h.temporary, "uid": string(uid), "url": "https://" + h.argoAddress, "catalog": baseURL, "pins": fmt.Sprintf("%x", sha256.Sum256(argoPins))})
	if err != nil {
		return err
	}
	return writeLabState(filepath.Join(dir, "ready.json"), data)
}

func (h *harness) recordArgoCleanup(runErr, cleanupErr error) error {
	data, err := json.Marshal(map[string]any{"assertions": runErr == nil, "cleanup": cleanupErr == nil, "pid": os.Getpid(), "profile": h.profile})
	if err != nil {
		return err
	}
	return writeLabState(filepath.Join(h.options.repositoryRoot, "output", "argocd-lab", "finished.json"), data)
}

// The lifecycle command can poll the supervisor while it publishes readiness or cleanup.
func writeLabState(path string, data []byte) error {
	if err := os.WriteFile(path+".next", data, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".next", path)
}

// captureArgoProgress retains only fixture-owned, secret-free phase observations before profile teardown.
func (h *harness) captureArgoProgress() {
	raw, err := os.ReadFile(filepath.Join(h.temporary, "argo-evidence.json")) // #nosec G304 -- fixed invocation-private report.
	if err == nil && json.Valid(raw) {
		h.report.ArgoCD = raw
	}
}

func (h *harness) writeArgoReport(runErr, cleanupErr error) error {
	dir := filepath.Join(h.options.repositoryRoot, "output", "argocd")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	result := map[string]any{
		"profile": h.profile, "assertionsPassed": runErr == nil, "cleanupCompleted": cleanupErr == nil,
		"source": h.argoBuild, "images": h.report.Images, "checks": h.report.Checks,
		"nativeCampaignStarted": len(h.report.ArgoCD) > 0, "phases": h.report.ArgoCD,
		"platform": map[string]string{
			"minikube": h.report.MinikubeVersion, "kubernetes": h.report.KubernetesVersion, "helm": h.report.HelmVersion,
			"driver": h.report.Driver, "runtime": h.report.ContainerRuntime, "cni": h.report.CNI,
		},
	}
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, h.profile+".json")
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		return err
	}
	fmt.Println("Argo CD qualification report:", path)
	return nil
}
