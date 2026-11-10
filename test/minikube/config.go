//go:build minikube

// Minikube test options validate pinned local inputs and generate isolated fixture identities.

package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	primaryTenantID  = "00000000-0000-7000-8000-000000000001"
	otherTenantID    = "00000000-0000-7000-8000-000000000002"
	oidcIssuer       = "http://identity-fixture:8081"
	oidcAudience     = "open-agentic-catalog"
	fixtureKeyID     = "open-agentic-catalog-minikube"
	namespace        = "open-agentic-catalog-e2e"
	releaseName      = "catalog"
	localServiceCIDR = "10.96.0.0/16"
	calicoCNIImage   = "quay.io/calico/cni:v3.32.2"
	calicoNodeImage  = "quay.io/calico/node:v3.32.2"
	calicoCtrlImage  = "quay.io/calico/kube-controllers:v3.32.2"
)

// CRI ImageID can identify either the reviewed image index or its amd64 child.
// The index-to-platform relationships are verified when these pins change.
var expectedCalicoImages = map[string]struct{ indexID, amd64ID string }{
	calicoCNIImage: {
		"quay.io/calico/cni@sha256:0ef740bc587f25565905adf1d1f61a7faff0d571c449c6bdd789feed743d3ef7",
		"quay.io/calico/cni@sha256:8de23af2a36514e5027cf3f8577be61c577ae21664d19bd726bb9ccedb9e0d5a",
	},
	calicoNodeImage: {
		"quay.io/calico/node@sha256:99b03fe91e8bfbcb153ae65ef4b701b24ce541ffdd74ff314eb041096008f7fd",
		"quay.io/calico/node@sha256:d7a9b58573b165caa8128d2735a19b788434547064b2b4af2abf68cea24f7ec3",
	},
	calicoCtrlImage: {
		"quay.io/calico/kube-controllers@sha256:7870b67ebb13fabc3005252b44fe6e78b21635649bd3072b80afa1684b6565d0",
		"quay.io/calico/kube-controllers@sha256:f241490840083743e747389af8d5067c961370bd5e2e2af6538db2f7e183f60b",
	},
}

var versionPattern = regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)\.([0-9]+)`) // Tool suffixes are deliberately ignored.

type options struct {
	scenario         string
	vaultCleanupTest string
	upgradeOnly      bool
	ansCapture       bool
	ansTrust         bool
	interopTrust     bool
	repositoryRoot   string
	minikube         string
	kubectl          string
	helm             string
	docker           string
	requiredMinikube string
	kubernetes       string
	driver           string
	runtime          string
	baseImage        string
	postgresImage    string
	preloadArchive   string
	overallTimeout   time.Duration
}

// loadOptions reads explicit harness overrides and validates the maintained local
// test environment. These platform restrictions are not product compatibility limits.
func loadOptions(scenario string) (options, error) {
	root, err := os.Getwd()
	if err != nil {
		return options{}, err
	}
	configured := options{
		scenario:         scenario,
		repositoryRoot:   root,
		minikube:         environment("CATALOG_MINIKUBE_BIN", "minikube"),
		kubectl:          environment("CATALOG_KUBECTL_BIN", "kubectl"),
		helm:             environment("CATALOG_HELM_BIN", "helm"),
		docker:           environment("CATALOG_CONTAINER_ENGINE_BIN", "docker"),
		requiredMinikube: environment("CATALOG_MINIKUBE_VERSION", "v1.39.0"),
		kubernetes:       environment("CATALOG_MINIKUBE_KUBERNETES_VERSION", "v1.37.1"),
		driver:           environment("CATALOG_MINIKUBE_DRIVER", "docker"),
		runtime:          environment("CATALOG_MINIKUBE_CONTAINER_RUNTIME", "containerd"),
		baseImage:        environment("CATALOG_MINIKUBE_BASE_IMAGE", "gcr.io/k8s-minikube/kicbase:v0.0.51@sha256:4a1c825b61479e6c898851ea66f13c620aaeab6002746e95067fc2c4b38a0b24"),
		postgresImage:    environment("CATALOG_MINIKUBE_POSTGRES_IMAGE", "pgvector/pgvector:pg18@sha256:2358fcba361ed2233a5ed81b5fe4ca779ccb304120ce531a3bf51c0ed7e2bc11"),
		preloadArchive:   strings.TrimSpace(os.Getenv("CATALOG_MINIKUBE_PRELOAD_ARCHIVE")),
	}
	switch scenario {
	case "basic":
		configured.overallTimeout = 25 * time.Minute
	case "monitoring", "tracing", "ui", "replicas", "semantic", "pgbouncer", "agntcy", "gateway", "ans", "peers", "backup-restore", "autoscaling", "cert-manager", "vault":
		configured.overallTimeout = 40 * time.Minute
	case "topology":
		configured.overallTimeout = 40 * time.Minute
	default:
		return options{}, errors.New("scenario must be basic, topology, monitoring, tracing, ui, replicas, semantic, pgbouncer, agntcy, gateway, ans, peers, backup-restore, autoscaling, cert-manager or vault")
	}
	if scenario == "gateway" || scenario == "agntcy" {
		value, present := os.LookupEnv("INTEROPERABILITY_TRUST_VERIFICATION")
		if !present {
			value = "off"
		}
		configured.interopTrust, err = peerTrustSelection(value)
		if err != nil {
			return options{}, err
		}
	}
	if raw := strings.TrimSpace(os.Getenv("CATALOG_MINIKUBE_TIMEOUT")); raw != "" {
		configured.overallTimeout, err = time.ParseDuration(raw)
		if err != nil || configured.overallTimeout < 5*time.Minute || configured.overallTimeout > time.Hour {
			return options{}, errors.New("CATALOG_MINIKUBE_TIMEOUT must be between 5m and 1h")
		}
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return options{}, fmt.Errorf("the maintained local Minikube harness requires linux/amd64, got %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if configured.driver != "docker" || configured.runtime != "containerd" {
		return options{}, errors.New("the maintained Minikube harness requires the docker driver and containerd runtime")
	}
	for name, reference := range map[string]string{"base image": configured.baseImage, "PostgreSQL image": configured.postgresImage} {
		if err := validateDigestReference(reference); err != nil {
			return options{}, fmt.Errorf("%s: %w", name, err)
		}
	}
	if configured.preloadArchive != "" {
		absolute, err := filepath.Abs(configured.preloadArchive)
		if err != nil {
			return options{}, err
		}
		info, err := os.Stat(absolute)
		if err != nil || !info.Mode().IsRegular() {
			return options{}, fmt.Errorf("CATALOG_MINIKUBE_PRELOAD_ARCHIVE must identify a regular file: %s", absolute)
		}
		configured.preloadArchive = absolute
	}
	return configured, nil
}

// runtimeReplicas keeps generated values and admitted-Pod checks in agreement.
func (o options) runtimeReplicas() int {
	switch o.scenario {
	case "replicas", "semantic":
		return 3
	case "topology", "tracing", "pgbouncer", "autoscaling":
		return 2
	default:
		return 1
	}
}

// environment uses a nonblank trimmed override, otherwise the documented fixture default.
func environment(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

// validateDigestReference requires a lowercase SHA-256 pin and refuses latest tags;
// it checks input syntax without pulling or inspecting an image.
func validateDigestReference(reference string) error {
	if strings.Contains(reference, ":latest") || !strings.Contains(reference, "@sha256:") {
		return fmt.Errorf("reference must be digest-bound and may not use latest: %q", reference)
	}
	parts := strings.Split(reference, "@sha256:")
	if len(parts) != 2 || parts[0] == "" || len(parts[1]) != 64 {
		return fmt.Errorf("invalid sha256 image reference %q", reference)
	}
	if _, err := strconv.ParseUint(parts[1][:16], 16, 64); err != nil {
		return fmt.Errorf("invalid sha256 image reference %q", reference)
	}
	for _, character := range parts[1] {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return fmt.Errorf("invalid sha256 image reference %q", reference)
		}
	}
	return nil
}

type semanticVersion struct{ major, minor, patch int }

func supportedHelmVersion(version semanticVersion) bool {
	return version.major == 4 || version.major == 3 && version.minor >= 15
}

// parseVersion extracts the numeric release prefix; tool-specific suffixes are ignored.
func parseVersion(value string) (semanticVersion, error) {
	matches := versionPattern.FindStringSubmatch(strings.TrimSpace(value))
	if len(matches) != 4 {
		return semanticVersion{}, fmt.Errorf("cannot parse version %q", value)
	}
	parts := make([]int, 3)
	for index := range parts {
		parsed, err := strconv.Atoi(matches[index+1])
		if err != nil {
			return semanticVersion{}, err
		}
		parts[index] = parsed
	}
	return semanticVersion{major: parts[0], minor: parts[1], patch: parts[2]}, nil
}

// privateClusterPrefix requires all reported IPv4 Pod ranges to fit one private
// /16, the widest prefix admitted by the fixture's federation policy.
func privateClusterPrefix(prefixes []string) (string, error) {
	if len(prefixes) == 0 {
		return "", errors.New("cluster reported no Pod CIDRs")
	}
	var selected netip.Addr
	for _, raw := range prefixes {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil || !prefix.Addr().Is4() || !prefix.Addr().IsPrivate() || prefix.Bits() < 16 {
			return "", fmt.Errorf("cluster reported unsafe Pod CIDR %q", raw)
		}
		if !selected.IsValid() {
			selected = prefix.Addr()
		}
	}
	// Federation policy intentionally refuses prefixes broader than /16.
	candidate := netip.PrefixFrom(selected, 16).Masked()
	for _, raw := range prefixes {
		prefix, _ := netip.ParsePrefix(raw)
		if !candidate.Contains(prefix.Addr()) {
			return "", fmt.Errorf("pod CIDRs do not share one bounded private /16: %v", prefixes)
		}
	}
	return candidate.String(), nil
}

// randomHex creates cryptographically random fixture names or credentials in hex.
func randomHex(bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", raw), nil
}

// randomBase64 encodes random fixture key bytes in standard padded base64.
func randomBase64(bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// newSigningKey creates an invocation-only RSA key and its public fixture JWK set.
func newSigningKey() (*rsa.PrivateKey, []byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	exponent := make([]byte, 0, 4)
	for value := key.PublicKey.E; value > 0; value >>= 8 {
		exponent = append([]byte{byte(value)}, exponent...)
	}
	set := map[string]any{"keys": []any{map[string]any{
		"kty": "RSA", "kid": fixtureKeyID, "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(exponent),
	}}}
	raw, err := json.Marshal(set)
	return key, raw, err
}

// signToken issues a short-lived fixture access token with fixed issuer/audience.
// Catalog membership provisioning, not token claims alone, grants test roles.
func signToken(key *rsa.PrivateKey, subject, organization string, now time.Time) (string, error) {
	header, err := json.Marshal(map[string]any{"alg": "RS256", "kid": fixtureKeyID, "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{
		"iss": oidcIssuer, "sub": subject, "aud": oidcAudience,
		"iat": now.Unix(), "nbf": now.Add(-time.Minute).Unix(), "exp": now.Add(45 * time.Minute).Unix(),
		"organization_id": organization,
	})
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(encoded))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return encoded + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// clusterCNI binds startup and image qualification to the selected scenario.
func (o options) clusterCNI() string {
	switch o.scenario {
	case "topology", "semantic", "pgbouncer", "agntcy", "gateway", "ans", "vault":
		return "calico"
	default:
		return "bridge"
	}
}
