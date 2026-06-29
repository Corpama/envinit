package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureGrubCmdlineAddsRequiredKernelParams(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grub")
	if err := os.WriteFile(path, []byte("GRUB_CMDLINE_LINUX=\"quiet\"\n"), 0o644); err != nil {
		t.Fatalf("write grub config: %v", err)
	}

	changed, content, err := ensureGrubCmdline(path, requiredKernelCmdline)
	if err != nil {
		t.Fatalf("ensure grub cmdline: %v", err)
	}
	if !changed {
		t.Fatal("expected grub config to change")
	}
	for _, want := range []string{"rw", "biosdevname=0", "iommu=pt", "mitigations=off", "nokaslr"} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected %q in grub config:\n%s", want, content)
		}
	}
	if strings.Contains(content, "quiet") {
		t.Fatalf("expected quiet to be removed:\n%s", content)
	}
}
