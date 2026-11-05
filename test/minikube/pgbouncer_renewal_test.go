//go:build minikube

package main

import (
	"strings"
	"testing"
	"time"
)

func TestDatabaseProjectionEvidence(t *testing.T) {
	const path = "/database-trust/ca.pem"
	const value = "public CA bytes\n"
	const digest = "ff62b5a9a0c3d1b7225b199a8bc7de409ccaf1ba66afad5271daece82a66221a"
	// The literal hash is checked independently below; no TLS behavior is mocked.
	if databaseHash([]byte(value)) != digest {
		t.Fatal("unexpected public input hash")
	}
	for _, test := range []struct {
		raw   string
		valid bool
	}{
		{digest + "  " + path + "\n", true},
		{digest + "  /other", false}, {strings.Repeat("0", 64) + "  " + path, false},
		{"warning\n" + digest + "  " + path, false}, {"", false},
	} {
		if databaseProjectionMatches([]byte(test.raw), path, value) != test.valid {
			t.Errorf("projection evidence accepted=%v: %q", test.valid, test.raw)
		}
	}
}

func renewalTestPods(prefix string) map[string][]podIdentity {
	result := map[string][]podIdentity{}
	for _, role := range []string{"api", "worker", "pgbouncer", "postgres"} {
		count := 1
		if role == "api" || role == "worker" {
			count = 2
		}
		for i := 0; i < count; i++ {
			result[role] = append(result[role], podIdentity{UID: prefix + role + string(rune('a'+i)), ImageID: role + "-image", ServiceAccount: role + "-account", Ready: true})
		}
	}
	return result
}

func TestDatabasePodActivationEvidence(t *testing.T) {
	before := renewalTestPods("old-")
	for _, test := range []struct {
		name                     string
		activate, restart, valid bool
		mutate                   func(map[string][]podIdentity)
	}{
		{"projection only", false, false, true, func(after map[string][]podIdentity) {}},
		{"trust activation", true, false, true, func(after map[string][]podIdentity) {
			for _, role := range []string{"api", "worker", "pgbouncer"} {
				for i := range after[role] {
					after[role][i].UID = "next-" + after[role][i].UID
				}
			}
		}},
		{"server activation", true, true, true, func(after map[string][]podIdentity) {
			for role := range after {
				for i := range after[role] {
					after[role][i].UID = "next-" + after[role][i].UID
				}
			}
		}},
		{"unexpected automatic rollout", false, false, false, func(after map[string][]podIdentity) { after["api"][0].UID = "new" }},
		{"missing activation", true, false, false, func(after map[string][]podIdentity) {}},
		{"replica missing", false, false, false, func(after map[string][]podIdentity) { after["worker"] = after["worker"][:1] }},
		{"restart", false, false, false, func(after map[string][]podIdentity) { after["api"][0].Restarts = 1 }},
		{"unready", false, false, false, func(after map[string][]podIdentity) { after["worker"][0].Ready = false }},
		{"duplicate", false, false, false, func(after map[string][]podIdentity) { after["api"][1].UID = after["api"][0].UID }},
		{"missing UID", false, false, false, func(after map[string][]podIdentity) { after["pgbouncer"][0].UID = "" }},
		{"different image", false, false, false, func(after map[string][]podIdentity) { after["api"][0].ImageID = "other" }},
		{"different account", false, false, false, func(after map[string][]podIdentity) { after["worker"][0].ServiceAccount = "owner" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			after := renewalTestPods("old-")
			test.mutate(after)
			if err := databasePodTransition(before, after, test.activate, test.restart); (err == nil) != test.valid {
				t.Fatalf("activation accepted=%v: %v", test.valid, err)
			}
		})
	}
}

func TestDatabaseFreshBackendEvidence(t *testing.T) {
	now := time.Date(2026, 11, 2, 6, 0, 0, 0, time.UTC)
	makePhase := func(job string, pid int, postmaster time.Time) databaseRenewalPhase {
		phase := databaseRenewalPhase{JobID: job, Backends: map[string]databaseRenewalBackend{}}
		for _, role := range []string{"catalog_api", "catalog_worker", "catalog_owner"} {
			phase.Backends[role] = databaseRenewalBackend{PID: pid, Started: now.Add(time.Duration(pid) * time.Second), Postmaster: postmaster}
		}
		return phase
	}
	before := makePhase("first", 2, now)
	for _, test := range []struct {
		name           string
		restart, valid bool
		phase          databaseRenewalPhase
	}{
		{"fresh trust", false, true, makePhase("next", 3, now)},
		{"new server", true, true, makePhase("next", 2, now.Add(time.Second))},
		{"same Job", false, false, makePhase("first", 3, now)},
		{"warm backend", false, false, makePhase("next", 2, now)},
		{"lost server restart", true, false, makePhase("next", 3, now)},
		{"unexpected server restart", false, false, makePhase("next", 3, now.Add(time.Second))},
		{"no PID", false, false, makePhase("next", 0, now)},
		{"no server identity", false, false, makePhase("next", 3, time.Time{})},
		{"missing role", false, false, databaseRenewalPhase{JobID: "next"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := databaseFreshBackends(before, test.phase, test.restart); (err == nil) != test.valid {
				t.Fatalf("backend evidence accepted=%v: %v", test.valid, err)
			}
		})
	}
}

func TestDatabaseLeafRejectsNonCertificateAndTrailingData(t *testing.T) {
	material, err := databaseTLSMaterial(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := databaseLeaf(material["postgres.crt"]); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"", material["postgres.key"], material["postgres.crt"] + "garbage", material["postgres.crt"] + material["ca.pem"]} {
		if _, err := databaseLeaf(raw); err == nil {
			t.Fatal("accepted invalid leaf")
		}
	}
}
