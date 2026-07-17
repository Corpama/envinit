package bundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRejectsUnknownBundleField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bundle.json")
	content := `{
  "defaults": {
    "configure_managment_network": false
  },
  "platform": {
    "os_family": "ubuntu",
    "package_manager": "apt",
    "network_backend": "netplan"
  }
}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "unknown field") || !strings.Contains(err.Error(), "configure_managment_network") {
		t.Fatalf("Load unknown field error = %v", err)
	}
}

func TestLoadRejectsMultipleJSONValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bundle.json")
	if err := os.WriteFile(path, []byte(`{} {}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "multiple JSON values") {
		t.Fatalf("Load trailing JSON error = %v", err)
	}
}
