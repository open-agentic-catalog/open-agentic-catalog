package demoarchive

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStrictAllowlistAndOrigin(t *testing.T) {
	a := Allowlist{Format: 1, TenantID: TenantID, Entries: []Entry{{Path: "/example.zip", Digest: "sha256:" + strings.Repeat("a", 64), Size: MaxArchiveBytes}}}
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(raw)
	if err != nil || decoded.Digest() != a.Digest() {
		t.Fatal("exact boundary identity", err)
	}
	for _, input := range []string{`null`, `{"format":1,"tenantId":"` + TenantID + `","entries":null}`, strings.Replace(string(raw), `"format":1`, `"format":1,"format":1`, 1), strings.Replace(string(raw), `"format":1`, `"Format":1`, 1), strings.Replace(string(raw), `"format":1`, `"format":1,"unknown":1`, 1), strings.Repeat(" ", MaxAllowlistBytes+1)} {
		if _, err := Decode([]byte(input)); err == nil {
			t.Fatal("unsafe input accepted")
		}
	}
	for _, mutate := range []func(*Allowlist){func(a *Allowlist) { a.Format = 2 }, func(a *Allowlist) { a.TenantID = "other" }, func(a *Allowlist) { a.Entries[0].Size = 0 }, func(a *Allowlist) { a.Entries[0].Size++ }, func(a *Allowlist) { a.Entries[0].Path = "/%65xample.zip" }, func(a *Allowlist) { a.Entries[0].Digest = "sha256:BAD" }, func(a *Allowlist) { a.Entries = make([]Entry, MaxArchives+1) }} {
		changed := a
		changed.Entries = append([]Entry{}, a.Entries...)
		mutate(&changed)
		if changed.Validate() == nil {
			t.Fatal("invalid identity")
		}
	}
	for _, origin := range []string{"https://127.0.0.1:18443", "https://[::1]:443", "https://172.30.255.41:8443"} {
		if ValidateOrigin(origin) != nil {
			t.Fatal("valid origin rejected")
		}
	}
	for _, origin := range []string{"https://%", "https://localhost:443", "http://127.0.0.1", "https://8.8.8.8", "https://user@127.0.0.1", "https://127.0.0.1/", "https://127.0.0.1?", "https://127.0.0.1#x", "https://127.0.0.1:0", "https://127.0.0.1:65536", "https://[::ffff:127.0.0.1]", "https://[fe80::1%25eth0]"} {
		if ValidateOrigin(origin) == nil {
			t.Fatal("unsafe origin admitted", origin)
		}
	}
}
