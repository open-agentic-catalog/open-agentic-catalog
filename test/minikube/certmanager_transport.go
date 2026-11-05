//go:build minikube

package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"time"
)

// Use the public hostname for HTTP and TLS while connecting only to the owned
// loopback tunnel. No DNS, ambient proxy, redirects or TLS sessions are reused.
func certificateIngressClient(address string, root []byte) (*http.Client, error) {
	endpoint, err := netip.ParseAddrPort(address)
	if err != nil || !endpoint.Addr().IsLoopback() || endpoint.Port() == 0 {
		return nil, errors.New("certificate probe requires an owned literal-loopback tunnel")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(root) {
		return nil, errors.New("certificate probe requires independent CA trust")
	}
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	transport := &http.Transport{
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: 3 * time.Second,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: certificateHost},
		DialContext: func(ctx context.Context, network, selected string) (net.Conn, error) {
			if selected != certificateHost+":443" || network != "tcp" {
				return nil, errors.New("certificate probe destination is outside the selected ingress")
			}
			return dialer.DialContext(ctx, "tcp", address)
		},
	}
	return &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

// Read the actual served leaf after normal hostname/chain/time verification.
// A different Secret or Ready condition cannot substitute for this handshake.
func readIngressCertificate(ctx context.Context, client *http.Client) (issuedCertificate, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+certificateHost+"/.well-known/oauth-protected-resource/mcp", nil)
	if err != nil {
		return issuedCertificate{}, err
	}
	reply, err := client.Do(request)
	if err != nil {
		return issuedCertificate{}, errors.New("fresh ingress TLS exchange failed")
	}
	defer func() { _ = reply.Body.Close() }()
	if reply.StatusCode != http.StatusOK || reply.TLS == nil || len(reply.TLS.PeerCertificates) == 0 || len(reply.TLS.VerifiedChains) == 0 || reply.TLS.DidResume {
		return issuedCertificate{}, errors.New("ingress did not return metadata over a fresh verified TLS connection")
	}
	body, err := io.ReadAll(io.LimitReader(reply.Body, (64<<10)+1))
	if err != nil || len(body) > 64<<10 {
		return issuedCertificate{}, errors.New("ingress metadata response is incomplete or exceeds its bound")
	}
	var metadata struct {
		Resource string   `json:"resource"`
		Issuers  []string `json:"authorization_servers"`
	}
	if json.Unmarshal(body, &metadata) != nil || metadata.Resource != "https://"+certificateHost+"/mcp" || len(metadata.Issuers) != 1 || metadata.Issuers[0] != oidcIssuer {
		return issuedCertificate{}, errors.New("ingress metadata resource or OIDC issuer differs from the fixture")
	}
	leaf := reply.TLS.PeerCertificates[0]
	return certificateIdentity(leaf), nil
}

func certificateIdentity(leaf *x509.Certificate) issuedCertificate {
	return issuedCertificate{SHA256: fmt.Sprintf("%x", sha256.Sum256(leaf.Raw)), PublicKeySHA256: fmt.Sprintf("%x", sha256.Sum256(leaf.RawSubjectPublicKeyInfo)), Serial: leaf.SerialNumber.String(), NotAfter: leaf.NotAfter}
}

func requireRenewedCertificate(before, after issuedCertificate) error {
	if before.SHA256 == "" || after.SHA256 == "" || before.Serial == "" || after.Serial == "" || before.PublicKeySHA256 == "" || after.PublicKeySHA256 == "" || before.NotAfter.IsZero() || after.NotAfter.IsZero() {
		return errors.New("certificate renewal evidence is incomplete")
	}
	if before.SHA256 == after.SHA256 || before.Serial == after.Serial || before.PublicKeySHA256 == after.PublicKeySHA256 || after.NotAfter.Before(before.NotAfter) {
		return errors.New("served certificate, serial and private key did not rotate without shortening validity")
	}
	return nil
}
