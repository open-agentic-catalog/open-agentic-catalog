//go:build minikube

package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed fixtures/cert-manager/pins.json
var certificateInputPins []byte

type certificateInputFile struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}

type certificateInputs struct {
	Charts map[string]certificateInputFile `json:"charts"`
	Images map[string]struct {
		Reference string `json:"reference"`
	} `json:"images"`
	Sources map[string]certificateInputFile `json:"sources"`
	CMCTL   struct {
		certificateInputFile
		SBOMSHA256 string `json:"sbomSha256"`
	} `json:"cmctl"`
}

func readCertificatePins() (certificateInputs, error) {
	var pins certificateInputs
	err := json.Unmarshal(certificateInputPins, &pins)
	return pins, err
}

func (h *harness) certificateTool(name string) string {
	return filepath.Join(h.options.repositoryRoot, ".tools", "minikube-cert-manager", name)
}

func (h *harness) checkCertificateInputs() error {
	pins, err := readCertificatePins()
	if err != nil {
		return err
	}
	files := []certificateInputFile{pins.CMCTL.certificateInputFile, {File: pins.CMCTL.File + ".spdx.sbom", SHA256: pins.CMCTL.SBOMSHA256}}
	for _, file := range pins.Charts {
		files = append(files, file)
	}
	for _, file := range pins.Sources {
		files = append(files, file)
	}
	for _, file := range files {
		raw, err := os.ReadFile(h.certificateTool(file.File)) // #nosec G304 -- Fixed embedded input inventory under the ignored local cache.
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(raw)) != file.SHA256 {
			return errors.New("cert-manager input missing or changed; run make cert-manager-tools-install")
		}
	}
	for name, pin := range pins.Images {
		raw, err := h.run("inspect pinned "+name, h.options.docker, "image", "inspect", "--format", "{{.Os}}/{{.Architecture}} {{.Id}}", pin.Reference)
		parts := strings.Fields(string(raw))
		if err != nil || len(parts) != 2 || parts[0] != "linux/amd64" {
			return errors.New("pinned cert-manager image is missing or has the wrong platform")
		}
		h.report.Images["cert-manager-input-"+name] = parts[1]
	}
	return nil
}

func (h *harness) prepareCertificateImages() error {
	pins, err := readCertificatePins()
	if err != nil {
		return err
	}
	h.certificateImages = make(map[string]string)
	for name, pin := range pins.Images {
		alias := "open-agentic-catalog:minikube-cert-" + name + "-" + h.suffix
		if _, err := h.run("alias pinned "+name, h.options.docker, "image", "tag", pin.Reference, alias); err != nil {
			return err
		}
		h.certificateImages[name] = alias
	}
	return nil
}
