// Unit tests exercise isolated validation and controlled dependencies.
package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestConfigurationFailsClosed(t *testing.T) {
	c := fixtureConfig(t, "http://127.0.0.1:8080")
	for name, change := range map[string]func(*Config){
		"production test users": func(c *Config) { c.Environment = "production" },
		"unknown environment":   func(c *Config) { c.Environment = "unknown" },
		"unknown auth":          func(c *Config) { c.AuthMode = "password" },
		"nonlocal development":  func(c *Config) { c.Origin = "https://example.com" },
		"credentials in origin": func(c *Config) { c.Origin = "https://user:password@example.com" },
		"origin path":           func(c *Config) { c.Origin += "/app" },
		"upstream query":        func(c *Config) { c.Upstream += "?url=https://attacker.invalid" },
		"upstream path":         func(c *Config) { c.Upstream += "/other" },
		"base traversal":        func(c *Config) { c.BasePath = "/app/../other" },
		"base HTML":             func(c *Config) { c.BasePath = `/app"><script>` },
		"listen":                func(c *Config) { c.Listen = "missing-port" },
		"cookie":                func(c *Config) { c.CookieName = "invalid;cookie" },
		"session":               func(c *Config) { c.SessionSeconds = 0 },
		"request bytes":         func(c *Config) { c.RequestBytes = 65 << 20 },
		"response bytes":        func(c *Config) { c.ResponseBytes = 0 },
		"timeout":               func(c *Config) { c.TimeoutSeconds = 0 },
		"mixed auth":            func(c *Config) { c.Issuer = "https://issuer.example" },
		"documentation":         func(c *Config) { c.DocumentationURL = "javascript:alert(1)" },
		"HTTP documentation":    func(c *Config) { c.DocumentationURL = "http://example.com/docs" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := c
			change(&bad)
			err := bad.validate()
			if err == nil {
				t.Fatal("invalid configuration accepted")
			}
			// Unreadable mounted inputs must not mask the configuration error.
			missing := filepath.Join(t.TempDir(), "missing")
			bad.KeysFile, bad.Assets, bad.DevelopmentUsersFile = missing, missing, missing
			s, constructorErr := New(bad, nil)
			if s != nil || constructorErr == nil || constructorErr.Error() != err.Error() {
				t.Fatalf("constructor validation=%v, host=%v; want %v", constructorErr, s != nil, err)
			}
		})
	}
	file := filepath.Join(t.TempDir(), "config.json")
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(file); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		strings.Replace(string(raw), `"listen":`, `"Listen":`, 1),
		strings.Replace(string(raw), `"documentationUrl":""`, `"documentationUrl":null`, 1),
		strings.TrimSuffix(string(raw), "}") + `,"scopes":null}`,
		`{"listen":"a","listen":"b"}`, `{"unknown":true}`, `{"Listen":":8080"}`, `{"listen":null}`, `{"scopes":null}`, `{"listen":"\ud800"}`, `{} {}`, strings.Repeat(" ", 65<<10)} {
		if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(file); err == nil {
			t.Fatal("invalid input accepted")
		}
	}
	if _, err := readFile(t.TempDir(), 100); err == nil {
		t.Fatal("directory accepted")
	}
	if _, err := readFile(filepath.Join(t.TempDir(), "missing"), 100); err == nil {
		t.Fatal("missing file accepted")
	}
	for _, count := range []int{3, 4} {
		pairs := make([]keyPair, count)
		for index := range pairs {
			pairs[index] = keyPair{Hash: strings.Repeat("ab", 64), Block: strings.Repeat("cd", 32)}
		}
		raw, err := json.Marshal(pairs)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(c.KeysFile, raw, 0600); err != nil {
			t.Fatal(err)
		}
		s := &Server{config: c}
		if err := s.initCookies(); (err == nil) != (count == 3) {
			t.Fatalf("key ring with %d pairs: %v", count, err)
		}
		if count == 3 && len(s.codecs) != count {
			t.Fatal("maximum key ring lost a decoder")
		}
	}
	for _, raw := range []string{"[]", `[{"hash":"00","block":"00"}]`, `[{"hash":"unknown","block":"unknown"}]`} {
		if err := os.WriteFile(c.KeysFile, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := New(c, nil); err == nil {
			t.Fatal("invalid keys accepted")
		}
	}
}

func TestConstructorDevelopmentUserBounds(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, alias, want        string
		count                    int
		duplicate                bool
		profileGroup             string
		elevated, separateTenant bool
	}{
		{name: "one user", alias: "reader", count: 1},
		{name: "bundle presentation", alias: "reader", count: 1, profileGroup: "bundle", elevated: true, separateTenant: true},
		{name: "template presentation", alias: "reader", count: 1, profileGroup: "template"},
		{name: "invalid profile group", alias: "reader", count: 1, profileGroup: "unknown", want: "invalid UI development profile group"},
		{name: "maximum users", alias: "reader", count: 32},
		{name: "maximum alias", alias: "r" + strings.Repeat("a", 47), count: 1},
		{name: "too many users", alias: "reader", count: 33, want: "UI development users must contain 1..32 fixed users"},
		{name: "uppercase alias", alias: "Reader", count: 1, want: "invalid or duplicate UI development user"},
		{name: "empty alias", count: 1, want: "invalid or duplicate UI development user"},
		{name: "alias too long", alias: "r" + strings.Repeat("a", 48), count: 1, want: "invalid or duplicate UI development user"},
		{name: "duplicate alias", alias: "reader", count: 2, duplicate: true, want: "invalid or duplicate UI development user"},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := fixtureConfig(t, "https://api.example.test")
			// The unchanged mounted inputs construct a usable neighboring host.
			newFixture(t, c)
			users := make([]developmentUser, test.count)
			for i := range users {
				alias := test.alias
				if i > 0 && !test.duplicate {
					alias += "-" + strconv.Itoa(i)
				}
				users[i] = developmentUser{Alias: alias, Label: "Reader", Description: "Read local resources", Token: "test-token", ProfileGroup: test.profileGroup, Elevated: test.elevated, SeparateTenant: test.separateTenant}
			}
			raw, err := json.Marshal(users)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(c.DevelopmentUsersFile, raw, 0600); err != nil {
				t.Fatal(err)
			}
			s, err := New(c, nil)
			if test.want != "" {
				if s != nil || err == nil || err.Error() != test.want {
					t.Fatalf("constructor host=%v error=%v; want %s", s != nil, err, test.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(s.Close)
			w := uiRequest(s, http.MethodGet, "/auth/options", "")
			var options struct {
				Mode  string            `json:"mode"`
				Users []developmentUser `json:"users"`
			}
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &options) != nil || options.Mode != "development" || len(options.Users) != test.count {
				t.Fatalf("accepted users unavailable: %d %s", w.Code, w.Body.String())
			}
			for i, user := range options.Users {
				if user.Alias != users[i].Alias || user.Label != users[i].Label || user.Description != users[i].Description || user.ProfileGroup != users[i].ProfileGroup || user.Elevated != users[i].Elevated || user.SeparateTenant != users[i].SeparateTenant || user.Token != "" {
					t.Fatal("options changed configured users or exposed a token")
				}
			}
		})
	}
}

func TestAssetsAndDrain(t *testing.T) {
	c := fixtureConfig(t, "http://127.0.0.1:8080")
	s := newFixture(t, c)
	if w := uiRequest(s, "GET", "/readyz", ""); w.Code != http.StatusOK {
		t.Fatal("configured assets should be ready without an API connection")
	}
	s.Drain()
	for route, want := range map[string]int{"/readyz": 503, "/livez": 200, "/auth/session": 503} {
		if w := uiRequest(s, "GET", route, ""); w.Code != want {
			t.Fatalf("draining %s: got %d, want %d", route, w.Code, want)
		}
	}
	if err := os.Remove(filepath.Join(c.Assets, "assets", "app-test.js")); err != nil {
		t.Fatal(err)
	}
	if _, err := New(c, nil); err == nil {
		t.Fatal("missing entry asset accepted")
	}
	for _, body := range []string{"", "<!doctype html><h1>No bundle</h1>", strings.Repeat("x", (1<<20)+1)} {
		if err := os.WriteFile(filepath.Join(c.Assets, "index.html"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := New(c, nil); err == nil {
			t.Fatal("unusable entry page accepted")
		}
	}
}

func TestDevelopmentChoicePresentationValidation(t *testing.T) {
	for _, tt := range []struct {
		name, label, description string
		valid                    bool
	}{
		{"description", "Reader", "Read local resources", true}, {"omitted description", "Reader", "", true}, {"byte boundary", strings.Repeat("x", 100), strings.Repeat("é", 150), true},
		{"label control", "Reader\u0007", "", false}, {"description control", "Reader", "Secret\ncontrol", false}, {"description bytes", "Reader", strings.Repeat("é", 151), false}, {"label bytes", strings.Repeat("x", 101), "", false},
		{"label UTF8 byte boundary", strings.Repeat("🙂", 25), "", true}, {"label UTF8 byte overflow", strings.Repeat("🙂", 25) + "a", "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := fixtureConfig(t, "http://127.0.0.1:8080")
			raw, err := json.Marshal([]developmentUser{{Alias: "reader", Label: tt.label, Description: tt.description, Token: "private-token"}})
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(c.DevelopmentUsersFile, raw, 0600); err != nil {
				t.Fatal(err)
			}
			s, err := New(c, nil)
			if (err == nil) != tt.valid {
				t.Fatalf("presentation accepted=%v err=%v", s != nil, err)
			}
			if s != nil {
				t.Cleanup(s.Close)
				reply := uiRequest(s, http.MethodGet, "/auth/options", "")
				if strings.Contains(reply.Body.String(), "private-token") {
					t.Fatal("chooser exposed credential")
				}
			}
		})
	}
}

func TestProductionOIDCRejectsDevelopmentInputs(t *testing.T) {
	c := oidcFixtureConfig(t, "https://api.example.test", "https://issuer.example.test/realm")
	c.Environment, c.Origin = "production", "https://browse.example.test"
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Config){
		"development user file": func(c *Config) { c.DevelopmentUsersFile = "unreadable-development-users" },
		"development mode":      func(c *Config) { c.AuthMode = "development" },
		"HTTP origin":           func(c *Config) { c.Origin = "http://browse.example.test" },
		"HTTP issuer":           func(c *Config) { c.Issuer = "http://issuer.example.test" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := c
			change(&invalid)
			if invalid.validate() == nil {
				t.Fatal("incompatible production settings accepted")
			}
		})
	}
}
