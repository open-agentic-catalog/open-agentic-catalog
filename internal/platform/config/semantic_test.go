// Semantic configuration tests distinguish vector identity from transport and
// ranking settings and exercise strict disabled/enabled validation without I/O.
package config

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

// validSemantic returns a synthetic local policy, not a running provider.
func validSemantic() Semantic {
	s := SemanticDefaults()
	s.Enabled = true
	s.Endpoint = "http://127.0.0.1:18000/v1/embeddings"
	s.Model = "test"
	s.ProfileRevision = "v1"
	s.Dimensions = 384
	s.AllowHTTP = true
	s.AllowedAddressPrefixes = []string{"127.0.0.0/8"}
	return s
}

// TestSemanticValidation covers independent bounds rather than network availability.
func TestSemanticValidation(t *testing.T) {
	if err := validSemantic().Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*Semantic)
	}{
		{"no endpoint", func(s *Semantic) { s.Endpoint = "" }},
		{"userinfo", func(s *Semantic) { s.Endpoint = "https://user:secret@example.com/v1/embeddings" }},
		{"query", func(s *Semantic) { s.Endpoint = "https://example.com/v1/embeddings?secret=a" }},
		{"bare query", func(s *Semantic) { s.Endpoint = "https://example.com/v1/embeddings?" }},
		{"fragment", func(s *Semantic) { s.Endpoint += "#private" }},
		{"unclean path", func(s *Semantic) { s.Endpoint = "https://example.com/x/../v1/embeddings" }},
		{"escaped path", func(s *Semantic) { s.Endpoint = "https://example.com/%76/embeddings" }},
		{"HTTP implicit", func(s *Semantic) { s.AllowHTTP = false }},
		{"HTTP broad", func(s *Semantic) { s.AllowedAddressPrefixes = []string{"0.0.0.0/0"} }},
		{"HTTP credentials", func(s *Semantic) { s.TokenFile = "token" }},
		{"HTTP no prefixes", func(s *Semantic) { s.AllowedAddressPrefixes = nil }},
		{"bad prefix", func(s *Semantic) { s.AllowedAddressPrefixes = []string{"127.0.0.1/8"} }},
		{"model", func(s *Semantic) { s.Model = "\nprivate" }},
		{"revision", func(s *Semantic) { s.ProfileRevision = "" }},
		{"dimensions", func(s *Semantic) { s.Dimensions = 0 }},
		{"reduction mismatch", func(s *Semantic) { s.RequestedDimensions = 256 }},
		{"cutoff", func(s *Semantic) { s.SimilarityCutoff = math.NaN() }},
		{"prefix", func(s *Semantic) { s.QueryPrefix = "\x00" }},
		{"batch", func(s *Semantic) { s.BatchSize = 33 }},
		{"concurrency", func(s *Semantic) { s.QueryConcurrency = 0 }},
		{"timeout", func(s *Semantic) { s.QueryTimeout = Duration{Duration: time.Minute} }},
		{"tenant", func(s *Semantic) { s.ExcludedTenants = []string{"invalid"} }},
		{"token path control", func(s *Semantic) {
			s.Endpoint = "https://example.com/v1/embeddings"
			s.TokenFile = "/credentials/token\n"
		}},
		{"CA path control", func(s *Semantic) {
			s.CAFile = "/credentials/ca\x00.pem"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := validSemantic()
			tc.change(&s)
			if err := s.Validate(); err == nil {
				t.Fatal("accepted invalid settings")
			}
			s.Enabled = false
			if err := s.Validate(); err != nil {
				t.Fatal("disabled required provider", err)
			}
		})
	}
}

// TestSemanticCollectionAndPathLimits pairs each maximum with its first rejected
// size so other validation failures cannot disguise an incorrect collection cap.
func TestSemanticCollectionAndPathLimits(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, errorContext string
		limit              int
		change             func(*Semantic, int)
	}{
		{"address prefixes", "address prefixes", 16, func(s *Semantic, size int) {
			s.AllowedAddressPrefixes = make([]string, size)
			for i := range s.AllowedAddressPrefixes {
				s.AllowedAddressPrefixes[i] = fmt.Sprintf("127.0.%d.0/24", i)
			}
		}},
		{"tenant exclusions", "tenant exclusions", 256, func(s *Semantic, size int) {
			s.ExcludedTenants = make([]string, size)
			for i := range s.ExcludedTenants {
				s.ExcludedTenants[i] = fmt.Sprintf("abcdefab-0000-4000-8000-%012x", i+1)
			}
		}},
		{"token path", "credential path", 4096, func(s *Semantic, size int) {
			s.TokenFile = "/" + strings.Repeat("x", size-1)
		}},
		{"CA path", "credential path", 4096, func(s *Semantic, size int) {
			s.CAFile = "/" + strings.Repeat("x", size-1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, size := range []int{test.limit, test.limit + 1} {
				s := validSemantic()
				s.Endpoint = "https://example.com/v1/embeddings"
				test.change(&s, size)
				err := s.Validate()
				if size == test.limit {
					if err != nil {
						t.Fatalf("maximum supported size %d: %v", size, err)
					}
				} else if err == nil || !strings.Contains(err.Error(), test.errorContext) {
					t.Fatalf("size %d must fail at its own bound: %v", size, err)
				}
			}
		})
	}
}

// TestSemanticIdentity separates vector rebuilds and cursor invalidation from
// transport, tenant eligibility and work-budget changes without freezing hashes.
func TestSemanticIdentity(t *testing.T) {
	t.Parallel()

	s := validSemantic()
	s.Endpoint = "https://embeddings.example/v1/embeddings"
	s.AllowHTTP = false
	s.AllowedAddressPrefixes = nil
	if err := s.Validate(); err != nil {
		t.Fatalf("valid profile control: %v", err)
	}
	embedding, ranking := s.EmbeddingProfile(), s.RankingProfile()
	if embedding == "" || ranking == "" || s.EmbeddingProfile() != embedding || s.RankingProfile() != ranking {
		t.Fatal("profile identities must be nonempty and deterministic")
	}
	for _, test := range []struct {
		name                           string
		change                         func(*Semantic)
		changeEmbedding, changeRanking bool
	}{
		{"endpoint", func(s *Semantic) { s.Endpoint = "https://other.example/v1/embeddings" }, false, false},
		{"token file", func(s *Semantic) { s.TokenFile = "/not-read/token" }, false, false},
		{"private trust", func(s *Semantic) { s.CAFile = "/not-read/ca.pem" }, false, false},
		{"response alias", func(s *Semantic) { s.ResponseModel = "canonical-model-name" }, false, false},
		{"query timeout", func(s *Semantic) { s.QueryTimeout.Duration = 3 * time.Second }, false, false},
		{"batch timeout", func(s *Semantic) { s.BatchTimeout.Duration = 12 * time.Second }, false, false},
		{"query concurrency", func(s *Semantic) { s.QueryConcurrency = 8 }, false, false},
		{"batch concurrency", func(s *Semantic) { s.BatchConcurrency = 2 }, false, false},
		{"batch size", func(s *Semantic) { s.BatchSize = 8 }, false, false},
		{"tenant exclusion", func(s *Semantic) { s.ExcludedTenants = []string{"abcdefab-0000-4000-8000-000000000001"} }, false, false},
		{"cutoff", func(s *Semantic) { s.SimilarityCutoff = .75 }, false, true},
		{"model", func(s *Semantic) { s.Model = "other" }, true, true},
		{"revision", func(s *Semantic) { s.ProfileRevision = "v2" }, true, true},
		{"dimensions", func(s *Semantic) { s.Dimensions = 768 }, true, true},
		{"query prefix", func(s *Semantic) { s.QueryPrefix = "query: " }, true, true},
		{"document prefix", func(s *Semantic) { s.DocumentPrefix = "document: " }, true, true},
		{"input limit", func(s *Semantic) { s.MaxInputBytes = 4096 }, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			next := s
			test.change(&next)
			if err := next.Validate(); err != nil {
				t.Fatalf("changed configuration must remain valid: %v", err)
			}
			if changed := next.EmbeddingProfile() != embedding; changed != test.changeEmbedding {
				t.Fatalf("embedding identity changed = %t, want %t", changed, test.changeEmbedding)
			}
			if changed := next.RankingProfile() != ranking; changed != test.changeRanking {
				t.Fatalf("ranking identity changed = %t, want %t", changed, test.changeRanking)
			}
		})
	}
}

// TestSemanticTenantEligibility checks exclusions independently of vector identity.
func TestSemanticTenantEligibility(t *testing.T) {
	t.Parallel()
	s := validSemantic()
	tenant := "abcdefab-0000-4000-8000-000000000001"
	if !s.PermitsTenant(tenant) || s.PermitsTenant("invalid") {
		t.Fatal("tenant validation")
	}
	s.ExcludedTenants = []string{strings.ToUpper(tenant)}
	if s.PermitsTenant(tenant) {
		t.Fatal("tenant exclusion")
	}
	s.ExcludedTenants = append(s.ExcludedTenants, tenant)
	if s.Validate() == nil {
		t.Fatal("duplicate exclusions")
	}
}

// validSemanticConfig combines existing fixtures; its DSN is data, not a connection.
func validSemanticConfig() Config {
	c := validTestConfig()
	c.Database = Database{Driver: "postgres", DSN: "postgres://catalog@database.invalid/catalog", MaxConnLifetime: Defaults().Database.MaxConnLifetime, MaxConnIdleTime: Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: Defaults().Database.HealthCheckPeriod}
	c.SemanticSearch = validSemantic()
	c.SemanticSearch.QueryTimeout.Duration = 500 * time.Millisecond
	return c
}

// TestSemanticRoleValidation checks enabled storage requirements and error
// propagation while preserving disabled and migration dependency boundaries.
func TestSemanticRoleValidation(t *testing.T) {
	t.Parallel()

	for _, role := range []Role{RoleAPI, RoleWorker} {
		t.Run(string(role), func(t *testing.T) {
			c := validSemanticConfig()
			if err := c.ValidateFor(role); err != nil {
				t.Fatalf("valid enabled configuration: %v", err)
			}
			c.SemanticSearch.Model = ""
			if err := c.ValidateFor(role); err == nil || !strings.Contains(err.Error(), "semantic model") {
				t.Fatalf("invalid provider configuration must reach role validation: %v", err)
			}
		})
	}
	for _, role := range []Role{RoleAPI, RoleWorker, RoleAll} {
		t.Run(string(role)+" memory", func(t *testing.T) {
			c := validSemanticConfig()
			c.Database = Database{Driver: "memory", MaxConnLifetime: Defaults().Database.MaxConnLifetime, MaxConnIdleTime: Defaults().Database.MaxConnIdleTime, HealthCheckPeriod: Defaults().Database.HealthCheckPeriod}
			if err := c.ValidateFor(role); err == nil || !strings.Contains(err.Error(), "semantic search requires PostgreSQL") {
				t.Fatalf("enabled semantic search must reject memory storage: %v", err)
			}
			c.SemanticSearch.Enabled = false
			if err := c.ValidateFor(role); err != nil {
				t.Fatalf("disabled semantic search must permit memory storage: %v", err)
			}
		})
	}
	c := validSemanticConfig()
	c.SemanticSearch = Semantic{Enabled: true}
	if err := c.ValidateFor(RoleMigration); err != nil {
		t.Fatalf("migration must not require provider configuration: %v", err)
	}
}

// TestSemanticRoleTimeoutBudgets checks strict fallback and lease boundaries
// without constructing an API server, worker or embedding client.
func TestSemanticRoleTimeoutBudgets(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, errorContext string
		role               Role
		change             func(*Config, time.Duration)
	}{
		{"query fallback", "lexical fallback", RoleAPI, func(c *Config, offset time.Duration) {
			c.SemanticSearch.QueryTimeout.Duration = 750*time.Millisecond + offset
		}},
		{"batch lease", "half the worker lease", RoleWorker, func(c *Config, offset time.Duration) {
			c.Worker.LeaseDuration.Duration = 20 * time.Second
			c.SemanticSearch.BatchTimeout.Duration = 10*time.Second + offset
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, offset := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
				t.Run(offset.String(), func(t *testing.T) {
					c := validSemanticConfig()
					test.change(&c, offset)
					if err := c.SemanticSearch.Validate(); err != nil {
						t.Fatalf("provider settings must be valid independently of role budgets: %v", err)
					}
					err := c.ValidateFor(test.role)
					if offset < 0 {
						if err != nil {
							t.Fatalf("timeout just inside its budget: %v", err)
						}
					} else if err == nil || !strings.Contains(err.Error(), test.errorContext) {
						t.Fatalf("timeout at or outside its budget must be rejected: %v", err)
					}
				})
			}
			c := validSemanticConfig()
			test.change(&c, 0)
			otherRole := RoleWorker
			if test.role == RoleWorker {
				otherRole = RoleAPI
			}
			if err := c.ValidateFor(otherRole); err != nil {
				t.Fatalf("%s must not enforce the other role's timeout budget: %v", otherRole, err)
			}
		})
	}
}

// TestSemanticOverride verifies closed duplicate-safe overlay behavior.
func TestSemanticOverride(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{"enabled":null}`, `{"excludedTenants":[null]}`, `{"enabled":true,"enabled":false}`, `{"model":"x","unknown":true}`, `{} {}`} {
		c := Defaults()
		if applySemantic(&c, raw) == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	c := Defaults()
	if err := applySemantic(&c, `{"enabled":false,"model":"example"}`); err != nil || c.SemanticSearch.Model != "example" || c.SemanticSearch.BatchSize != 16 {
		t.Fatalf("overlay %+v %v", c.SemanticSearch, err)
	}
}
