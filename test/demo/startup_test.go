package main

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestStartupStagePreservesCauseAndUsesFixedVocabulary(t *testing.T) {
	cause := errors.New("private path, credentials and raw dependency error")
	for _, stage := range []string{
		"sample.load", "prepared.write", "prepared.read", "tls.prepare",
		"tls.not-yet-valid", "tls.expired", "tls.hostname", "listener.address",
		"listener.address-in-use", "listener.serve",
	} {
		t.Run(stage, func(t *testing.T) {
			err := atStartupStage(stage, cause)
			if !errors.Is(err, cause) || startupStage(fmt.Errorf("wrapper: %w", err)) != stage {
				t.Fatal("startup stage or original error lost")
			}
			if startupStage(atStartupStage("tls.prepare", err)) != stage {
				t.Fatal("outer stage replaced the more precise stage")
			}
		})
	}
	for _, err := range []error{nil, cause, atStartupStage("private-secret", cause)} {
		if startupStage(err) != "unclassified" {
			t.Fatal("unreviewed error escaped the fallback")
		}
	}
	if atStartupStage("tls.prepare", nil) != nil {
		t.Fatal("successful startup classified as a failure")
	}
}

func TestExistingCertificateValidityStages(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	leaf := &x509.Certificate{
		NotBefore: start, NotAfter: start.Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("172.30.255.41")},
	}
	for _, tc := range []struct {
		name, ip, stage string
		now             time.Time
	}{
		{"before validity", "172.30.255.41", "tls.not-yet-valid", start.Add(-time.Nanosecond)},
		{"valid at start", "172.30.255.41", "", start},
		{"valid before expiry", "172.30.255.41", "", leaf.NotAfter.Add(-time.Nanosecond)},
		{"expired at end", "172.30.255.41", "tls.expired", leaf.NotAfter},
		{"hostname mismatch", "172.30.254.41", "tls.hostname", start},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateExistingCertificate(leaf, tc.ip, tc.now)
			if tc.stage == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || startupStage(err) != tc.stage {
				t.Fatalf("expected %s, got %s", tc.stage, startupStage(err))
			}
		})
	}
}
