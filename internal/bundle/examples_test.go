package bundle

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestExampleProfileBundlesLoad(t *testing.T) {
	cases := []struct {
		path           string
		osFamily       string
		packageManager string
		networkBackend string
		forbiddenKey   string
	}{
		{
			path:           "../../examples/bundle.ubuntu22.sample.json",
			osFamily:       "ubuntu",
			packageManager: "apt",
			networkBackend: "netplan",
			forbiddenKey:   "offline_repo",
		},
		{
			path:           "../../examples/bundle.kylin10sp3.sample.json",
			osFamily:       "kylin",
			packageManager: "yum",
			networkBackend: "auto",
			forbiddenKey:   "offline_apt",
		},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			b, err := Load(tc.path)
			if err != nil {
				t.Fatalf("load bundle: %v", err)
			}
			if b.Platform.OSFamily != tc.osFamily {
				t.Fatalf("os_family = %q, want %q", b.Platform.OSFamily, tc.osFamily)
			}
			if b.Platform.PackageManager != tc.packageManager {
				t.Fatalf("package_manager = %q, want %q", b.Platform.PackageManager, tc.packageManager)
			}
			if b.Platform.NetworkBackend != tc.networkBackend {
				t.Fatalf("network_backend = %q, want %q", b.Platform.NetworkBackend, tc.networkBackend)
			}
			raw := readExampleBundleJSON(t, tc.path)
			if _, ok := raw[tc.forbiddenKey]; ok {
				t.Fatalf("%s should not contain %s", tc.path, tc.forbiddenKey)
			}
			check, ok := raw["check"].(map[string]any)
			if !ok {
				t.Fatalf("%s should contain a check object", tc.path)
			}
			if _, ok := check["bandwidth"].(map[string]any); !ok {
				t.Fatalf("%s should contain check.bandwidth", tc.path)
			}
			if _, ok := check["xccl"].(map[string]any); !ok {
				t.Fatalf("%s should contain check.xccl", tc.path)
			}
			if _, ok := check["rdma_ping"].(map[string]any); !ok {
				t.Fatalf("%s should contain check.rdma_ping", tc.path)
			}
			if _, ok := check["ssh"].(map[string]any); !ok {
				t.Fatalf("%s should contain check.ssh", tc.path)
			}
			for _, legacyKey := range []string{
				"duration", "gid_index", "iterations", "bandwidth_qps", "message_size",
				"report_gbits", "mmap_device", "min_gbits", "parallel", "base_port", "rdma_groups",
				"rdma_ping_count", "rdma_ping_payload_size", "rdma_ping_timeout", "ssh_user", "ssh_options",
			} {
				if _, ok := check[legacyKey]; ok {
					t.Fatalf("%s should not contain legacy flat check key %s", tc.path, legacyKey)
				}
			}
			for _, path := range []string{
				b.Artifacts.OFEDArchive,
				b.Artifacts.XREInstaller,
				b.Artifacts.XDRArchive,
				b.Artifacts.FirmwareArchive,
			} {
				if !strings.HasPrefix(path, "data/") {
					t.Fatalf("artifact path %q should use data/ relative material path", path)
				}
			}
			if !strings.Contains(b.Artifacts.OFEDArchive, "/hca/mellanox/") {
				t.Fatalf("ofed path %q should use hca/mellanox directory", b.Artifacts.OFEDArchive)
			}
			if !strings.Contains(b.Artifacts.XREInstaller, "/xpu_driver/") || !strings.Contains(b.Artifacts.XDRArchive, "/xpu_driver/") {
				t.Fatalf("xre/xdr paths should use xpu_driver directory: %q %q", b.Artifacts.XREInstaller, b.Artifacts.XDRArchive)
			}
			if !strings.Contains(b.Artifacts.FirmwareArchive, "/xpu_firmware/") {
				t.Fatalf("firmware path %q should use xpu_firmware directory", b.Artifacts.FirmwareArchive)
			}
			for _, path := range b.Artifacts.ContainerPackages {
				if !strings.Contains(path, "/xpu_container_toolkit/") {
					t.Fatalf("container package path %q should use xpu_container_toolkit directory", path)
				}
			}
			if !b.Check.XCCL.Enabled {
				t.Fatal("profile sample should enable the XCCL collective check")
			}
			if !strings.HasPrefix(b.Check.XCCL.MPICHArchive, "data/misc/mpich-5.0.1-") {
				t.Fatalf("unexpected MPICH runtime path: %q", b.Check.XCCL.MPICHArchive)
			}
			if b.Check.XCCL.XCCLArchive != "data/misc/xccl_Linux_x86_64-3.2.2.0.tar.gz" {
				t.Fatalf("unexpected XCCL archive path: %q", b.Check.XCCL.XCCLArchive)
			}
		})
	}
}

func readExampleBundleJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parse bundle JSON: %v", err)
	}
	return raw
}
