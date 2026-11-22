//go:build minikube

package main

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

//go:embed fixtures/flux/pins.json
var fluxPins []byte

const fluxTargetVersion = "minikube-flux-target"

type fluxInputs struct {
	ControllerManifestSHA256 string            `json:"controllerManifestSha256"`
	Images                   map[string]string `json:"images"`
}

func (h *harness) checkFluxInputs() error {
	var pins fluxInputs
	if err := json.Unmarshal(fluxPins, &pins); err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(h.options.repositoryRoot, ".tools/minikube-flux/controllers.yaml")) // #nosec G304 -- fixed local test cache.
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(raw)) != pins.ControllerManifestSHA256 {
		return errors.New("run make flux-tools-install to prepare the pinned controller manifest")
	}
	for name, ref := range pins.Images {
		info, err := h.run("check cached "+name, h.options.docker, "image", "inspect", "--format", "{{.Os}}/{{.Architecture}} {{.Id}}", ref)
		fields := strings.Fields(string(info))
		if err != nil || len(fields) != 2 || fields[0] != "linux/amd64" {
			return fmt.Errorf("run make flux-tools-install to cache the pinned Linux AMD64 image for %s", name)
		}
		h.report.Images[name+"-source"] = ref
		h.report.Images[name+"-config"] = fields[1]
	}
	return nil
}

func (h *harness) prepareFluxImages() error {
	var pins fluxInputs
	if err := json.Unmarshal(fluxPins, &pins); err != nil {
		return err
	}
	h.gitopsImages = map[string]string{}
	for name, ref := range pins.Images {
		alias := "open-agentic-catalog:minikube-flux-" + name + "-" + h.suffix
		if _, err := h.run("alias pinned "+name, h.options.docker, "image", "tag", ref, alias); err != nil {
			return err
		}
		h.gitopsImages[name] = alias
	}
	return nil
}

// fluxPhase runs native Helm and Kubernetes operations against the harness's private context.
func (h *harness) fluxPhase(phase, values string) error {
	inputs, err := json.Marshal(map[string]map[string]string{"aliases": h.gitopsImages, "ids": h.report.Images})
	if err != nil {
		return err
	}
	args := []string{"-B", "test/minikube/fixtures/flux/handoff.py", phase, h.profile, h.catalogNamespace(), h.temporary, h.options.helm, h.options.kubectl, values, string(inputs)}
	raw, err := h.run("Flux "+phase, "python3", args...)
	if err != nil {
		return err
	}
	if phase == "result" {
		if !json.Valid(bytes.TrimSpace(raw)) {
			return errors.New("invalid Flux handoff evidence")
		}
		h.report.Flux = bytes.TrimSpace(raw)
	}
	return nil
}

type fluxCatalogReadback struct {
	release  [sha256.Size]byte
	manifest [sha256.Size]byte
}

// readFluxCatalog opens a fresh Service port-forward for the build, public snapshot and reader-authorized release.
func (h *harness) readFluxCatalog(releaseID, snapshotETag, version string) (fluxCatalogReadback, error) {
	var readback fluxCatalogReadback
	address, err := h.startPortForward("service/catalog", 8080)
	if err != nil {
		return readback, err
	}
	baseURL := "http://" + address
	if err := h.verifyServedBuild(baseURL, map[string]string{"version": version, "commit": "local"}); err != nil {
		return readback, err
	}
	manifest, err := h.request(http.MethodGet, baseURL+"/.well-known/ard.json", "", "", "", nil, nil)
	if err != nil {
		return readback, err
	}
	if manifest.Status != http.StatusOK || manifest.Header.Get("ETag") != snapshotETag || !json.Valid(manifest.Body) {
		return readback, errors.New("served ARD manifest does not match the original active snapshot")
	}
	readback.manifest = sha256.Sum256(manifest.Body)
	readback.release, err = h.readRetainedReleaseAt(baseURL, releaseID)
	return readback, err
}

func (h *harness) readRetainedReleaseAt(baseURL, releaseID string) ([sha256.Size]byte, error) {
	result, err := h.request(http.MethodGet, baseURL+"/api/v1/catalog/resources/"+releaseID, h.tokens["reader"], "", "", nil, nil)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	var record struct {
		Release struct {
			ID string `json:"id"`
		} `json:"release"`
	}
	if result.Status != http.StatusOK || json.Unmarshal(result.Body, &record) != nil || record.Release.ID != releaseID {
		return [sha256.Size]byte{}, errors.New("reader could not reopen the original governed release")
	}
	return sha256.Sum256(result.Body), nil
}

func (h *harness) verifyFlux(runID string, initial []byte) error {
	var smoke struct {
		SnapshotETag string `json:"snapshotEtag"`
		Resources    []struct {
			ReleaseID string `json:"releaseId"`
		} `json:"resources"`
	}
	if json.Unmarshal(initial, &smoke) != nil || smoke.SnapshotETag == "" || len(smoke.Resources) == 0 || smoke.Resources[0].ReleaseID == "" {
		return errors.New("missing publication ETag or governed release for the Flux handoff")
	}
	releaseID := smoke.Resources[0].ReleaseID
	before, err := h.readFluxCatalog(releaseID, smoke.SnapshotETag, "minikube")
	if err != nil {
		return err
	}
	checkRetained := func() error {
		after, err := h.readFluxCatalog(releaseID, smoke.SnapshotETag, fluxTargetVersion)
		if err != nil {
			return err
		}
		if after.manifest != before.manifest {
			return errors.New("served ARD manifest changed during the Flux handoff")
		}
		if after.release != before.release {
			return errors.New("retained release response changed during the Flux handoff")
		}
		return nil
	}
	h.coordinatorChart = filepath.Join(h.temporary, "charts", "open-agentic-catalog-0.1.0-flux.2.tgz")
	target, err := h.upgradeValues("flux-target", "catalog_api", 2, false)
	if err != nil {
		return err
	}
	if err = h.fluxPhase("pause", target); err != nil {
		return err
	}
	// The first external upgrade crosses the observed owner barrier from the
	// original image. Every forward recovery uses the same frozen Flux inputs.
	if err = h.verifyUpgradeBarrier(target); err != nil {
		return err
	}
	if err = h.verifyUpgradeReadback(runID, initial); err != nil {
		return err
	}
	if err = h.verifyUpgradeFaults(runID, initial, target); err != nil {
		return err
	}
	if err = checkRetained(); err != nil {
		return err
	}
	for _, phase := range []string{"resume", "restore-hooks"} {
		if err = h.fluxPhase(phase, target); err != nil {
			return err
		}
		if err = checkRetained(); err != nil {
			return err
		}
	}
	if err = h.fluxPhase("pause-control", target); err != nil {
		return err
	}
	if err = h.deployCatalog(target, "4m"); err != nil {
		return err
	}
	if err = checkRetained(); err != nil {
		return err
	}
	for _, phase := range []string{"hooks-control", "stop-controller", "restart-controller", "recover-handback"} {
		if err = h.fluxPhase(phase, target); err != nil {
			return err
		}
		if err = checkRetained(); err != nil {
			return err
		}
	}
	h.report.Checks = append(h.report.Checks, "reader-gets-identical-retained-release-before-and-after-flux-handoff",
		"flux-target-build-and-original-ard-snapshot-served-through-controller-restart",
		"flux-recovers-refused-handback-without-replacing-target-pods")
	return h.fluxPhase("result", target)
}
