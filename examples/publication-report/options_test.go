package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func unitOptions(command string) []string {
	args := []string{command, "--origin", "https://catalog.example.test", "--tenant", testTenant, "--host", testHost, "--database-file", "fixture.dsn"}
	if command == "serve" {
		args = append(args, "--signer-file", "signer.json", "--tls-cert", "server.pem", "--tls-key", "key.pem")
	}
	if command == "serve" || command == "reconcile" {
		args = append(args, "--token-file", "token")
	}
	return args
}
func withoutOption(args []string, flag string) []string {
	result := []string{}
	for i := 0; i < len(args); i++ {
		if args[i] == flag {
			i++
			continue
		}
		result = append(result, args[i])
	}
	return result
}
func TestCommandSpecificOptionContracts(t *testing.T) {
	for _, command := range []string{"init", "serve", "reconcile", "render"} {
		t.Run(command, func(t *testing.T) {
			base := unitOptions(command)
			got, err := parseOptions(base)
			want := options{command: command, origin: "https://catalog.example.test", tenant: testTenant, host: testHost, db: "fixture.dsn", listen: "127.0.0.1:9443", output: "publication-report.json", capacity: 1000, retention: 24 * time.Hour, poll: 5 * time.Second, reconciliation: time.Minute}
			if command == "serve" {
				want.signer = "signer.json"
				want.cert = "server.pem"
				want.key = "key.pem"
			}
			if command == "serve" || command == "reconcile" {
				want.token = "token"
			}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatal("valid command/default projection", got, err)
			}
			required := map[string]string{"--origin": "invalid_Catalog_inputs", "--tenant": "invalid_inputs", "--host": "invalid_inputs", "--database-file": "invalid_inputs"}
			if command == "serve" {
				for _, flag := range []string{"--signer-file", "--tls-cert", "--tls-key"} {
					required[flag] = "receiver_TLS_and_signer_required"
				}
			}
			if command == "serve" || command == "reconcile" {
				required["--token-file"] = "API_token_file_required"
			}
			for flag, code := range required {
				t.Run(flag, func(t *testing.T) { _, err := parseOptions(withoutOption(base, flag)); requireCode(t, err, code) })
			}
		})
	}
}

func TestOptionRangeBoundaries(t *testing.T) {
	for _, tc := range []struct {
		flag, value string
		valid       bool
	}{
		{"--capacity", "1", true}, {"--capacity", "10000", true},
		{"--retention", "1m", true}, {"--retention", "720h", true}, {"--retention", "59999999999ns", false}, {"--retention", "720h1ns", false},
		{"--poll-interval", "1s", true}, {"--poll-interval", "1m", true}, {"--poll-interval", "999999999ns", false}, {"--poll-interval", "1m1ns", false},
		{"--reconcile-interval", "5s", true}, {"--reconcile-interval", "24h", true}, {"--reconcile-interval", "4999999999ns", false}, {"--reconcile-interval", "24h1ns", false},
	} {
		t.Run(tc.flag+"="+tc.value, func(t *testing.T) {
			args := append(unitOptions("init"), tc.flag, tc.value)
			_, err := parseOptions(args)
			if tc.valid {
				if err != nil {
					t.Fatal("accepted boundary refused", err)
				}
			} else {
				requireCode(t, err, "invalid_inputs")
			}
		})
	}
}

func TestDispatchAndListenerInputRefusals(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code string
	}{
		{nil, "select_init_serve_reconcile_or_render"},
		{[]string{"PRIVATE_UNKNOWN_COMMAND"}, "invalid_command"},
		{append(unitOptions("init"), "extra"), "invalid_inputs"},
		{append(unitOptions("init"), "--listen", "127.0.0.1"), "invalid_listener"},
	} {
		_, err := parseOptions(tc.args)
		requireCode(t, err, tc.code)
	}
	base := append(unitOptions("init"), "--origin", "http://127.0.0.1:8080")
	_, err := parseOptions(base)
	requireCode(t, err, "invalid_Catalog_inputs")
	o, err := parseOptions(append(base, "--allow-loopback-http"))
	if err != nil || !o.loopback || o.origin != "http://127.0.0.1:8080" {
		t.Fatal("explicit loopback option not forwarded", err)
	}
}

func TestPublicationHostBoundaries(t *testing.T) {
	longest := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	for _, host := range []string{testHost, "127.0.0.1", "::1", strings.Repeat("a", 63) + ".test", longest} {
		if !validHost(host) {
			t.Fatal("supported host refused", host)
		}
	}
	for _, host := range []string{"", "Upper.example", "under_score.example", "-bad.example", "bad-.example", "empty..example", "trailing.example.", strings.Repeat("a", 64) + ".test", longest + "d", "0:0:0:0:0:0:0:1"} {
		if validHost(host) {
			t.Fatal("invalid or noncanonical host accepted", host)
		}
	}
}
