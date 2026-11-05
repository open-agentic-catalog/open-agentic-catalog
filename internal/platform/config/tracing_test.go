package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTracingReportRedactsDeploymentInputs(t *testing.T) {
	c := Defaults()
	c.Tracing.Enabled = true
	c.Tracing.Endpoint = "https://private-marker.example/v1/traces"
	c.Tracing.Installation = "private-marker-installation"
	c.Tracing.HeadersFile = "/private-marker/headers.json"
	c.Tracing.CAFile = "/private-marker/ca.pem"
	c.Tracing.ClientCertificateFile = "/private-marker/cert.pem"
	c.Tracing.ClientKeyFile = "/private-marker/key.pem"
	raw, err := json.Marshal(c.Report())
	if err != nil || strings.Contains(string(raw), "private-marker") {
		t.Fatal("report exposed tracing inputs")
	}
	if !c.Report().Tracing.EndpointConfigured || !c.Report().Tracing.ClientCertificateConfigured {
		t.Fatal("report omitted safe presence flags")
	}
}

func TestTracingConfigurationBoundaries(t *testing.T) {
	valid := Defaults().Tracing
	valid.Enabled = true
	valid.Endpoint = "https://collector.example/custom/v1/traces"
	valid.Installation = "fixture"
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg := validTestConfig()
	cfg.Tracing = valid
	if err := cfg.ValidateFor(RoleAPI); err != nil {
		t.Fatalf("valid runtime tracing configuration: %v", err)
	}
	cfg.Tracing.QueueSize = 0
	if err := cfg.ValidateFor(RoleAPI); err == nil || err.Error() != cfg.Tracing.Validate().Error() {
		t.Fatalf("runtime validation did not propagate the tracing error: %v", err)
	}
	for name, mutate := range map[string]func(*Tracing){
		"query":        func(c *Tracing) { c.Endpoint += "?token=private" },
		"fragment":     func(c *Tracing) { c.Endpoint += "#x" },
		"userinfo":     func(c *Tracing) { c.Endpoint = "https://secret@collector.example/v1/traces" },
		"encoded path": func(c *Tracing) { c.Endpoint = "https://collector.example/x%2Fy" },
		"unclean path": func(c *Tracing) { c.Endpoint = "https://collector.example/x/../v1/traces" },
		"ratio":        func(c *Tracing) { c.SampleRatio = 2 },
		"queue":        func(c *Tracing) { c.QueueSize = 0 },
		"key pair":     func(c *Tracing) { c.ClientKeyFile = "/private" },
		"installation": func(c *Tracing) { c.Installation = "" },
		"prefix count": func(c *Tracing) {
			c.AllowedAddressPrefixes = make([]string, 17)
			for i := range c.AllowedAddressPrefixes {
				c.AllowedAddressPrefixes[i] = "10.0.0.0/8"
			}
		},
		"noncanonical prefix":        func(c *Tracing) { c.AllowedAddressPrefixes = []string{"10.0.0.1/8"} },
		"plaintext":                  func(c *Tracing) { c.Endpoint = "http://collector.example/v1/traces" },
		"plaintext without prefixes": func(c *Tracing) { c.Endpoint = "http://collector.example/v1/traces"; c.AllowHTTP = true },
		"public plaintext": func(c *Tracing) {
			c.Endpoint = "http://collector.example/v1/traces"
			c.AllowHTTP = true
			c.AllowedAddressPrefixes = []string{"0.0.0.0/0"}
		},
		"partly public prefix": func(c *Tracing) {
			c.Endpoint = "http://collector.example/v1/traces"
			c.AllowHTTP = true
			c.AllowedAddressPrefixes = []string{"10.0.0.0/7"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := valid
			mutate(&c)
			if err := c.Validate(); err == nil {
				t.Fatal("unsafe input accepted")
			}
		})
	}
	for _, prefix := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.1/32", "fc00::/7", "::1/128"} {
		c := valid
		c.Endpoint, c.AllowHTTP, c.AllowedAddressPrefixes = "http://collector.example/v1/traces", true, []string{prefix}
		if err := c.Validate(); err != nil {
			t.Fatalf("explicit private prefix %s: %v", prefix, err)
		}
	}
}

func TestTracingOverrideIsClosed(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{"enabled":true,"enabled":false}`, `{"headers":{"Authorization":"secret"}}`, `{"typo":1}`, `{} {}`} {
		c := Defaults()
		if err := applyTracing(&c, raw); err == nil {
			t.Fatal("unsafe override")
		}
	}
	c := Defaults()
	if err := applyTracing(&c, `{"enabled":true,"endpoint":"https://collector.example/v1/traces","installation":"test"}`); err != nil {
		t.Fatal(err)
	}
	if err := c.Tracing.Validate(); err != nil {
		t.Fatal(err)
	}
}
