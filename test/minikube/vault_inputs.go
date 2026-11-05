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

//go:embed fixtures/vault/pins.json
var vaultInputPins []byte

type vaultFile struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}
type vaultImage struct {
	Reference string `json:"reference"`
}
type vaultInputs struct {
	Charts  map[string]vaultFile  `json:"charts"`
	Sources map[string]vaultFile  `json:"sources"`
	Images  map[string]vaultImage `json:"images"`
}

func readVaultPins() (vaultInputs, error) {
	var p vaultInputs
	err := json.Unmarshal(vaultInputPins, &p)
	return p, err
}
func (h *harness) vaultTool(name string) string {
	return filepath.Join(h.options.repositoryRoot, ".tools", "minikube-vault", name)
}
func (h *harness) checkVaultInputs() error {
	p, err := readVaultPins()
	if err != nil {
		return err
	}
	var receipt struct {
		PinsSHA256 string            `json:"pinsSha256"`
		Images     map[string]string `json:"images"`
	}
	raw, err := os.ReadFile(h.vaultTool("receipt.json")) // #nosec G304 -- Fixed ignored acquisition receipt.
	if err != nil || json.Unmarshal(raw, &receipt) != nil || receipt.PinsSHA256 != fmt.Sprintf("%x", sha256.Sum256(vaultInputPins)) {
		return errors.New("vault inputs missing or changed; run make vault-tools-install")
	}
	for _, set := range []map[string]vaultFile{p.Charts, p.Sources} {
		for _, file := range set {
			raw, err := os.ReadFile(h.vaultTool(file.File)) // #nosec G304 -- Embedded checksummed artifact inventory.
			if err != nil || fmt.Sprintf("%x", sha256.Sum256(raw)) != file.SHA256 {
				return errors.New("vault artifact missing or changed; run make vault-tools-install")
			}
		}
	}
	for name, pin := range p.Images {
		raw, err := h.run("inspect pinned Vault input "+name, h.options.docker, "image", "inspect", "--format", "{{.Os}}/{{.Architecture}} {{.Id}}", pin.Reference)
		fields := strings.Fields(string(raw))
		if err != nil || len(fields) != 2 || fields[0] != "linux/amd64" || fields[1] != receipt.Images[name] {
			return errors.New("vault image does not match the installed acquisition receipt")
		}
		alias := "open-agentic-catalog:minikube-vault-" + name + "-" + h.suffix
		if _, err := h.run("alias pinned Vault input "+name, h.options.docker, "image", "tag", pin.Reference, alias); err != nil {
			return err
		}
		h.vault.images[name] = alias
		h.report.Images["vault-"+name] = fields[1]
	}
	return nil
}
