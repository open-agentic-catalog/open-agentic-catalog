package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemovedDocumentBridgeSettingsAreRejected(t *testing.T) {
	for _, driver := range []string{"disabled", "memory", "filesystem", "s3"} {
		t.Run("json/"+driver, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte("{\"objects\":{\"driver\":\""+driver+"\"}}"), 0o600); err != nil {
				t.Fatal(err)
			}
			var cfg Config
			if err := decodeFile(path, &cfg); err == nil || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("obsolete objects configuration returned %v", err)
			}
		})
	}
	for _, name := range []string{"CATALOG_OBJECT_DRIVER", "CATALOG_OBJECT_DIRECTORY", "CATALOG_OBJECT_ENDPOINT", "CATALOG_OBJECT_BUCKET", "CATALOG_OBJECT_REGION"} {
		t.Run(name, func(t *testing.T) {
			for _, value := range []string{"", "private-obsolete-value"} {
				cfg := Defaults()
				err := applyEnvironment(&cfg, func(key string) (string, bool) { return value, key == name })
				if err == nil || !strings.Contains(err.Error(), "no longer supported") || strings.Contains(err.Error(), "private-obsolete-value") {
					t.Fatalf("obsolete environment setting returned %v", err)
				}
			}
		})
	}
}
