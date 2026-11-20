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
	}{
		{
			path:           "../../examples/bundle.ubuntu22.sample.json",
			osFamily:       "ubuntu",
			packageManager: "apt",
			networkBackend: "netplan",
		},
		{
			path:           "../../examples/bundle.kylin10sp3.sample.json",
			osFamily:       "kylin",
			packageManager: "yum",
			networkBackend: "auto",
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
			if len(b.Warnings) != 0 {
				t.Fatalf("canonical example produced warnings: %v", b.Warnings)
			}
			if !b.Check.Bandwidth.GIDIndexAuto || !b.Check.RDMAPing.PayloadAuto {
				t.Fatal("examples must use automatic GID selection and per-stream MTU payload")
			}
			wantTarget := "/etc/apt/sources.list.d/kunlun-offline.list"
			wantEntry := "deb [trusted=yes] file:/opt/kunlun-apt-repo jammy main"
			if tc.packageManager == "yum" {
				wantTarget = "/etc/yum.repos.d/kunlun-offline.repo"
				wantEntry = "baseurl=file:///opt/kunlun-rpm-repo"
			}
			repo := b.EffectiveOfflineRepo()
			entries := strings.NewReplacer("{{offline_repo_target}}", repo.CopyTo, "{{offline_apt_target}}", repo.CopyTo).Replace(strings.Join(repo.Entries, "\n"))
			if !repo.Enabled || repo.TargetFile != wantTarget || !strings.Contains(entries, wantEntry) {
				t.Fatalf("unexpected offline repository defaults: %+v, rendered entries=%q", repo, entries)
			}
			raw := readExampleBundleJSON(t, tc.path)
			for _, key := range []string{"offline_apt", "platform_options"} {
				if _, ok := raw[key]; ok {
					t.Fatalf("%s should not contain %s", tc.path, key)
				}
			}
			platform, ok := raw["platform"].(map[string]any)
			if !ok {
				t.Fatalf("%s should contain a platform object", tc.path)
			}
			for _, key := range []string{"kernel_headers_package", "kernel_headers_dir"} {
				if _, ok := platform[key]; ok {
					t.Fatalf("%s should not contain default platform.%s", tc.path, key)
				}
			}
			if packages, ok := raw["packages"].([]any); ok {
				for _, item := range packages {
					if item == "linux-headers-{{uname_r}}" || item == "kernel-devel-{{uname_r}}" {
						t.Fatalf("%s should rely on built-in kernel header prerequisites, found %v", tc.path, item)
					}
				}
			}
			defaults, ok := raw["defaults"].(map[string]any)
			if !ok {
				t.Fatal("example must contain defaults")
			}
			for _, key := range []string{"mgmt_bond_name", "mgmt_interfaces", "mgmt_prefix", "mgmt_gateway", "rdma_interfaces", "rdma_prefix", "rdma_route_cidr"} {
				if _, ok := defaults[key]; ok {
					t.Fatalf("%s should not contain deprecated defaults.%s", tc.path, key)
				}
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
