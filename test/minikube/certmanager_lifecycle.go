//go:build minikube

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

type certificateReport struct {
	CA                    string            `json:"caSha256"`
	Initial               issuedCertificate `json:"initial"`
	Renewed               issuedCertificate `json:"renewed"`
	Recovered             issuedCertificate `json:"recovered"`
	IssuerFailureObserved bool              `json:"issuerFailureObserved"`
	WorkloadsUnchanged    bool              `json:"workloadsUnchanged"`
}

func (h *harness) readCertificateSecret(ctx context.Context, name string) (map[string][]byte, error) {
	raw, err := h.topologyCommand(ctx, "read owned certificate Secret", nil, "-n", namespace, "get", "secret/"+name, "-o", "json", "--request-timeout=3s")
	if err != nil {
		return nil, errors.New("owned certificate Secret is unavailable")
	}
	var secret struct {
		Data map[string][]byte `json:"data"`
	}
	if json.Unmarshal(raw, &secret) != nil || len(secret.Data["tls.crt"]) == 0 || len(secret.Data["tls.key"]) == 0 {
		return nil, errors.New("owned TLS Secret is incomplete")
	}
	return secret.Data, nil
}

func (h *harness) issuedCertificate(ctx context.Context) (issuedCertificate, error) {
	secret, err := h.readCertificateSecret(ctx, certificateSecret)
	if err != nil {
		return issuedCertificate{}, err
	}
	return inspectIssuedCertificate(secret["tls.crt"], secret["tls.key"], h.certificateRoot, time.Now())
}

func (h *harness) renewCertificate() error {
	pins, err := readCertificatePins()
	if err != nil {
		return err
	}
	_, err = h.run("request supported manual certificate renewal", h.certificateTool(pins.CMCTL.File), "--kubeconfig", h.kubeconfig, "--context", h.profile, "--namespace", namespace, "renew", "catalog-mcp")
	return err
}

func (h *harness) waitCertificateReplacement(before issuedCertificate) (issuedCertificate, error) {
	ctx, cancel := context.WithTimeout(h.ctx, 2*time.Minute)
	defer cancel()
	var after issuedCertificate
	err := waitTopology(ctx, func() (bool, error) {
		var err error
		after, err = h.issuedCertificate(ctx)
		if err != nil {
			return false, err
		}
		return requireRenewedCertificate(before, after) == nil, nil
	})
	return after, err
}

func (h *harness) waitServedCertificate(client *http.Client, want issuedCertificate) error {
	ctx, cancel := context.WithTimeout(h.ctx, 90*time.Second)
	defer cancel()
	return waitTopology(ctx, func() (bool, error) {
		got, err := readIngressCertificate(ctx, client)
		return err == nil && got == want, nil
	})
}

func (h *harness) requireCertificateStatus(revision int, issuing bool) error {
	ctx, cancel := context.WithTimeout(h.ctx, 60*time.Second)
	defer cancel()
	return waitTopology(ctx, func() (bool, error) {
		raw, err := h.topologyCommand(ctx, "inspect certificate revision", nil, "-n", namespace, "get", "certificate/catalog-mcp", "-o", "json", "--request-timeout=3s")
		if err != nil {
			return false, err
		}
		var value struct {
			Status struct {
				Revision   int `json:"revision"`
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		}
		if json.Unmarshal(raw, &value) != nil {
			return false, errors.New("invalid certificate status")
		}
		ready, active := false, false
		for _, c := range value.Status.Conditions {
			if c.Type == "Ready" {
				ready = c.Status == "True"
			}
			if c.Type == "Issuing" {
				active = c.Status == "True"
			}
		}
		return value.Status.Revision == revision && active == issuing && (issuing || ready), nil
	})
}

func (h *harness) certificatePodUIDs() (map[string][]string, error) {
	result := map[string][]string{}
	for _, role := range []string{"api", "worker"} {
		pods, err := h.pods(role)
		if err != nil || len(pods) != 1 {
			return nil, errors.New("certificate test runtime inventory changed")
		}
		if !pods[0].Ready || pods[0].Restarts != 0 {
			return nil, errors.New("certificate test runtime is not ready without restarts")
		}
		result[role] = []string{pods[0].UID}
	}
	for _, scope := range []struct {
		namespace, release string
		names              []string
	}{{certificateControllerNamespace, "catalog-cert-manager", []string{"controller", "webhook", "cainjector"}}, {namespace, "catalog-edge", []string{"traefik"}}} {
		raw, err := h.kubectl("inspect external certificate processes", "-n", scope.namespace, "get", "pods", "-l", "app.kubernetes.io/instance="+scope.release, "-o", "json")
		if err != nil {
			return nil, err
		}
		var inventory struct {
			Items []struct {
				Metadata struct {
					UID string `json:"uid"`
				} `json:"metadata"`
				Spec struct {
					Containers []struct {
						Image string `json:"image"`
					} `json:"containers"`
				} `json:"spec"`
				Status struct {
					Conditions []struct {
						Type   string `json:"type"`
						Status string `json:"status"`
					} `json:"conditions"`
					Containers []struct {
						ImageID  string `json:"imageID"`
						Restarts int    `json:"restartCount"`
					} `json:"containerStatuses"`
				} `json:"status"`
			} `json:"items"`
		}
		if json.Unmarshal(raw, &inventory) != nil || len(inventory.Items) != len(scope.names) {
			return nil, errors.New("external certificate process inventory differs")
		}
		for _, pod := range inventory.Items {
			if len(pod.Spec.Containers) != 1 || len(pod.Status.Containers) != 1 || pod.Metadata.UID == "" || pod.Status.Containers[0].Restarts != 0 {
				return nil, errors.New("external certificate process restarted or changed")
			}
			ready := false
			for _, c := range pod.Status.Conditions {
				if c.Type == "Ready" && c.Status == "True" {
					ready = true
				}
			}
			matched := ""
			for _, name := range scope.names {
				if strings.TrimPrefix(pod.Spec.Containers[0].Image, "docker.io/library/") == h.certificateImages[name] {
					matched = name
				}
			}
			if !ready || matched == "" || result[matched] != nil {
				return nil, errors.New("external certificate process identity differs")
			}
			id := pod.Status.Containers[0].ImageID
			pins, e := readCertificatePins()
			if e != nil {
				return nil, e
			}
			digest := strings.SplitN(pins.Images[matched].Reference, "@", 2)[1]
			if id != h.report.Images["cert-manager-input-"+matched] && !strings.HasSuffix(id, "@"+digest) {
				return nil, errors.New("external certificate runtime image differs from its pin")
			}
			h.report.Images["cert-manager-running-"+matched] = id
			result[matched] = []string{pod.Metadata.UID}
		}
	}
	return result, nil
}

func (h *harness) requireCertificateWorkloads(before map[string][]string) error {
	after, err := h.certificatePodUIDs()
	if err != nil {
		return err
	}
	for role, want := range before {
		if !slices.Equal(want, after[role]) {
			return errors.New("certificate renewal replaced an application or controller Pod")
		}
	}
	return nil
}

func (h *harness) certificateIssuerFailure(client *http.Client, want issuedCertificate) error {
	if _, err := h.kubectl("make owned CA issuer unavailable", "-n", namespace, "patch", "issuer/catalog-local-ca", "--type=merge", "-p", `{"spec":{"ca":{"secretName":"catalog-test-missing"}}}`); err != nil {
		return err
	}
	if _, err := h.kubectl("observe unavailable CA issuer", "-n", namespace, "wait", "--for=condition=Ready=false", "issuer/catalog-local-ca", "--timeout=60s"); err != nil {
		return err
	}
	if err := h.renewCertificate(); err != nil {
		return err
	}
	if err := h.requireCertificateStatus(2, true); err != nil {
		return err
	}
	for range 3 {
		current, err := h.issuedCertificate(h.ctx)
		if err != nil || current != want {
			return errors.New("failed issuance changed the retained certificate")
		}
		served, err := readIngressCertificate(h.ctx, client)
		if err != nil || served != want {
			return errors.New("issuer failure interrupted the last valid served certificate")
		}
		select {
		case <-h.ctx.Done():
			return h.ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	h.report.Certificates.IssuerFailureObserved = true
	_, err := h.kubectl("restore the original local CA issuer", "-n", namespace, "patch", "issuer/catalog-local-ca", "--type=merge", "-p", `{"spec":{"ca":{"secretName":"catalog-test-ca"}}}`)
	return err
}

func (h *harness) verifyCertificateRenewal() error {
	h.report.Certificates = &certificateReport{}
	if err := h.installCertificateController(); err != nil {
		return err
	}
	initial, err := h.issuedCertificate(h.ctx)
	if err != nil {
		return err
	}
	if err := h.requireCertificateStatus(1, false); err != nil {
		return err
	}
	h.report.Certificates.CA = fmt.Sprintf("%x", sha256.Sum256(h.certificateRoot))
	h.report.Certificates.Initial = initial
	h.report.Checks = append(h.report.Checks, "namespaced-ca-issuance-and-exact-key-pair-before-ingress-start")
	if err := h.installCertificateIngress(); err != nil {
		return err
	}
	processes, err := h.certificatePodUIDs()
	if err != nil {
		return err
	}
	address, err := h.startPortForward("deployment/catalog-edge-traefik", 8443)
	if err != nil {
		return err
	}
	client, err := certificateIngressClient(address, h.certificateRoot)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	if err := h.waitServedCertificate(client, initial); err != nil {
		return err
	}
	if err := h.verifyCertificateMCP(client); err != nil {
		return err
	}
	if err := h.verifyCertificateRejections(address, initial); err != nil {
		return err
	}
	h.report.Checks = append(h.report.Checks, "verified-initial-ingress-identity-mcp-auth-and-exact-routes", "wrong-host-untrusted-and-expired-handshake-refusals")
	if err := h.renewCertificate(); err != nil {
		return err
	}
	renewed, err := h.waitCertificateReplacement(initial)
	if err != nil {
		return err
	}
	if err := h.requireCertificateStatus(2, false); err != nil {
		return err
	}
	if err := h.waitServedCertificate(client, renewed); err != nil {
		return err
	}
	if err := h.verifyCertificateMCP(client); err != nil {
		return err
	}
	h.report.Certificates.Renewed = renewed
	h.report.Checks = append(h.report.Checks, "manual-renewal-serves-new-leaf-serial-and-key-on-fresh-connections")
	if err := h.certificateIssuerFailure(client, renewed); err != nil {
		return err
	}
	recovered, err := h.waitCertificateReplacement(renewed)
	if err != nil {
		return err
	}
	if err := h.requireCertificateStatus(3, false); err != nil {
		return err
	}
	if err := h.waitServedCertificate(client, recovered); err != nil {
		return err
	}
	if err := h.verifyCertificateMCP(client); err != nil {
		return err
	}
	root, err := h.readCertificateSecret(h.ctx, localCASecret)
	if err != nil || !bytes.Equal(root["tls.crt"], h.certificateRoot) {
		return errors.New("local CA authority changed during renewal")
	}
	if err := h.requireCertificateWorkloads(processes); err != nil {
		return err
	}
	h.report.Certificates.Recovered = recovered
	h.report.Certificates.WorkloadsUnchanged = true
	h.report.Checks = append(h.report.Checks, "issuer-unavailability-retains-valid-serving-and-recovers-pending-renewal", "stable-ca-and-no-application-or-controller-restarts")
	_, err = h.kubectl("stop owned issuance before cleanup", "-n", namespace, "delete", "certificate/catalog-mcp", "issuer/catalog-local-ca", "--wait=true", "--timeout=30s")
	return err
}

func (h *harness) verifyCertificateRejections(address string, leaf issuedCertificate) error {
	other, err := localIssuerMaterial(time.Now())
	if err != nil {
		return err
	}
	for _, kind := range []string{"hostname", "authority", "expired"} {
		root := h.certificateRoot
		if kind == "authority" {
			root = []byte(other["tls.crt"])
		}
		client, err := certificateIngressClient(address, root)
		if err != nil {
			return err
		}
		transport, ok := client.Transport.(*http.Transport)
		if !ok {
			return errors.New("certificate probe has an unexpected transport")
		}
		if kind == "hostname" {
			transport.TLSClientConfig.ServerName = "wrong.test"
		}
		if kind == "expired" {
			transport.TLSClientConfig.Time = func() time.Time { return leaf.NotAfter.Add(time.Second) }
		}
		request, err := http.NewRequestWithContext(h.ctx, http.MethodGet, "https://"+certificateHost+"/.well-known/oauth-protected-resource/mcp", nil)
		if err != nil {
			return err
		}
		reply, requestErr := client.Do(request)
		if reply != nil {
			_ = reply.Body.Close()
		}
		client.CloseIdleConnections()
		var hostname x509.HostnameError
		var authority x509.UnknownAuthorityError
		var invalid x509.CertificateInvalidError
		valid := kind == "hostname" && errors.As(requestErr, &hostname) || kind == "authority" && errors.As(requestErr, &authority) || kind == "expired" && errors.As(requestErr, &invalid) && invalid.Reason == x509.Expired
		if !valid {
			return errors.New("certificate rejection did not reach the selected TLS validation guard")
		}
	}
	return nil
}
