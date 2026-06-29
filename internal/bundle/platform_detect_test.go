package bundle

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"envinit/internal/spec"
)

func TestApplyDetectedPlatformDefaultsDetectsKylinFromOSRelease(t *testing.T) {
	path := writeOSRelease(t, `ID="kylin"
ID_LIKE="rhel fedora"
`)
	b := spec.Bundle{}
	applyDetectedPlatformDefaults(&b, platformDetector{
		osReleasePath: path,
		lookPath:      fakeLookPath(),
	})
	b.ApplyDefaults()

	if b.Platform.OSFamily != "kylin" {
		t.Fatalf("unexpected os family: %s", b.Platform.OSFamily)
	}
	if b.Platform.PackageManager != "yum" {
		t.Fatalf("unexpected package manager: %s", b.Platform.PackageManager)
	}
	if b.Platform.NetworkBackend != "auto" {
		t.Fatalf("unexpected network backend: %s", b.Platform.NetworkBackend)
	}
}

func TestApplyDetectedPlatformDefaultsDetectsUbuntuFromOSRelease(t *testing.T) {
	path := writeOSRelease(t, `ID=ubuntu
ID_LIKE=debian
`)
	b := spec.Bundle{}
	applyDetectedPlatformDefaults(&b, platformDetector{
		osReleasePath: path,
		lookPath:      fakeLookPath(),
	})
	b.ApplyDefaults()

	if b.Platform.OSFamily != "ubuntu" {
		t.Fatalf("unexpected os family: %s", b.Platform.OSFamily)
	}
	if b.Platform.PackageManager != "apt" {
		t.Fatalf("unexpected package manager: %s", b.Platform.PackageManager)
	}
	if b.Platform.NetworkBackend != "netplan" {
		t.Fatalf("unexpected network backend: %s", b.Platform.NetworkBackend)
	}
}

func TestApplyDetectedPlatformDefaultsTreatsAutoFieldsAsUnset(t *testing.T) {
	path := writeOSRelease(t, `ID=ubuntu
ID_LIKE=debian
`)
	b := spec.Bundle{
		Platform: spec.PlatformConfig{
			OSFamily:       "auto",
			PackageManager: "auto",
			NetworkBackend: "auto",
		},
	}
	applyDetectedPlatformDefaults(&b, platformDetector{
		osReleasePath: path,
		lookPath:      fakeLookPath(),
	})
	b.ApplyDefaults()

	if b.Platform.OSFamily != "ubuntu" {
		t.Fatalf("unexpected os family: %s", b.Platform.OSFamily)
	}
	if b.Platform.PackageManager != "apt" {
		t.Fatalf("unexpected package manager: %s", b.Platform.PackageManager)
	}
	if b.Platform.NetworkBackend != "netplan" {
		t.Fatalf("unexpected network backend: %s", b.Platform.NetworkBackend)
	}
}

func TestApplyDetectedPlatformDefaultsOverridesStaleOSAndPackageFields(t *testing.T) {
	path := writeOSRelease(t, `ID=kylin
ID_LIKE="rhel fedora"
`)
	b := spec.Bundle{
		Platform: spec.PlatformConfig{
			OSFamily:       "ubuntu",
			PackageManager: "apt",
			NetworkBackend: "netplan",
		},
	}
	applyDetectedPlatformDefaults(&b, platformDetector{
		osReleasePath: path,
		lookPath:      fakeLookPath("apt-get"),
	})
	b.ApplyDefaults()

	if b.Platform.OSFamily != "kylin" {
		t.Fatalf("unexpected os family: %s", b.Platform.OSFamily)
	}
	if b.Platform.PackageManager != "yum" {
		t.Fatalf("unexpected package manager: %s", b.Platform.PackageManager)
	}
	if b.Platform.NetworkBackend != "auto" {
		t.Fatalf("unexpected network backend: %s", b.Platform.NetworkBackend)
	}
}

func TestApplyDetectedPlatformDefaultsPreservesExplicitNetworkBackend(t *testing.T) {
	path := writeOSRelease(t, `ID=kylin
ID_LIKE="rhel fedora"
`)
	b := spec.Bundle{
		Platform: spec.PlatformConfig{
			NetworkBackend: "network",
		},
	}
	applyDetectedPlatformDefaults(&b, platformDetector{
		osReleasePath: path,
		lookPath:      fakeLookPath(),
	})
	b.ApplyDefaults()

	if b.Platform.NetworkBackend != "network" {
		t.Fatalf("unexpected network backend: %s", b.Platform.NetworkBackend)
	}
}

func TestApplyDetectedPlatformDefaultsFallsBackToPackageManagerCommand(t *testing.T) {
	b := spec.Bundle{}
	applyDetectedPlatformDefaults(&b, platformDetector{
		osReleasePath: filepath.Join(t.TempDir(), "missing-os-release"),
		lookPath:      fakeLookPath("dnf"),
	})
	b.ApplyDefaults()

	if b.Platform.OSFamily != "redhat" {
		t.Fatalf("unexpected os family: %s", b.Platform.OSFamily)
	}
	if b.Platform.PackageManager != "yum" {
		t.Fatalf("unexpected package manager: %s", b.Platform.PackageManager)
	}
}

func writeOSRelease(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "os-release")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write os-release: %v", err)
	}
	return path
}

func fakeLookPath(commands ...string) func(string) (string, error) {
	available := map[string]bool{}
	for _, command := range commands {
		available[command] = true
	}
	return func(command string) (string, error) {
		if available[command] {
			return "/usr/bin/" + command, nil
		}
		return "", errors.New("not found")
	}
}
