package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"envinit/internal/spec"
)

func TestRenderRouteScript(t *testing.T) {
	item := spec.RDMAConfig{
		Name:    "ens11np0",
		IP:      "11.1.1.11",
		Prefix:  24,
		Gateway: "11.1.1.1",
		Table:   101,
	}
	content := renderRouteScript(item, "11.1.0.0/21", 32761)
	if !strings.Contains(content, `BROADCAST="11.1.1.255"`) {
		t.Fatalf("expected broadcast in route script, got:\n%s", content)
	}
	if !strings.Contains(content, `ip rule add from "$IP" table "$TABLE" priority "$PRIORITY"`) {
		t.Fatalf("expected source rule in route script, got:\n%s", content)
	}
	if !strings.Contains(content, `done < <(ip rule show | grep "$DEV" || true)`) {
		t.Fatalf("expected tolerant device-based cleanup in route script, got:\n%s", content)
	}
	for _, want := range []string{
		`ip rule del from all oif "$DEV" table "$TABLE" priority "$PRIORITY" 2>/dev/null || true`,
		`ip rule del from "$IP" table "$TABLE" priority "$PRIORITY" 2>/dev/null || true`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected exact cleanup rule %q in route script, got:\n%s", want, content)
		}
	}
}

func TestRenderMgmtNetplan(t *testing.T) {
	cfg := spec.MachineConfig{
		MgmtBondName: "bond0",
		MgmtIP:       "10.101.9.11",
		MgmtPrefix:   26,
		MgmtGateway:  "10.101.9.1",
		MgmtIfaces:   []string{"ens20f0np0", "ens20f1np1"},
		MgmtDNS:      []string{"10.0.0.2"},
		MgmtMTU:      1500,
		BondMode:     "802.3ad",
		BondLACPRate: "slow",
		BondXmitHash: "layer3+4",
	}
	content := renderMgmtNetplan(cfg)
	if !strings.Contains(content, "ens20f0np0: {}") || !strings.Contains(content, "ens20f1np1: {}") || !strings.Contains(content, "bond0") {
		t.Fatalf("unexpected mgmt netplan:\n%s", content)
	}
	if !strings.Contains(content, "10.101.9.11/26") {
		t.Fatalf("mgmt ip missing from netplan:\n%s", content)
	}
}

func TestRenderMgmtNetplanActiveBackup(t *testing.T) {
	cfg := spec.MachineConfig{
		MgmtBondName: "bond0",
		MgmtIP:       "10.157.5.207",
		MgmtPrefix:   23,
		MgmtGateway:  "10.157.4.1",
		MgmtIfaces:   []string{"ens12f0np0", "ens12f1np1"},
		MgmtMTU:      1500,
		BondMode:     "active-backup",
		BondMII:      100,
		BondPrimary:  "ens12f0np0",
	}
	content := renderMgmtNetplan(cfg)
	for _, want := range []string{
		"mode: active-backup",
		"mii-monitor-interval: 100",
		"primary: ens12f0np0",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected %q in active-backup netplan:\n%s", want, content)
		}
	}
	for _, unwanted := range []string{"lacp-rate:", "transmit-hash-policy:"} {
		if strings.Contains(content, unwanted) {
			t.Fatalf("did not expect %q in active-backup netplan:\n%s", unwanted, content)
		}
	}
}

func TestRenderMgmtNetplanSingleInterface(t *testing.T) {
	cfg := spec.MachineConfig{
		MgmtBondName: "bond0",
		MgmtIP:       "10.101.9.11",
		MgmtPrefix:   26,
		MgmtGateway:  "10.101.9.1",
		MgmtIfaces:   []string{"ens20f0np0"},
		MgmtDNS:      []string{"10.0.0.2"},
		MgmtMTU:      1500,
	}
	content := renderMgmtNetplan(cfg)
	if strings.Contains(content, "bonds:") {
		t.Fatalf("did not expect bond config for single interface:\n%s", content)
	}
	if !strings.Contains(content, "ens20f0np0:") || !strings.Contains(content, "10.101.9.11/26") {
		t.Fatalf("single-interface mgmt config missing expected content:\n%s", content)
	}
}

func TestRenderOfflineAPTEntriesUseCopiedTarget(t *testing.T) {
	app := &App{
		Bundle: spec.Bundle{
			OfflineAPT: spec.OfflineAPTConfig{
				MaterialPath: "/mnt/usb/repo",
				CopyTo:       "/opt/repo",
				Entries: []string{
					"deb [trusted=yes] file:{{offline_apt_target}} jammy main",
					"deb [trusted=yes] file:/mnt/usb/repo jammy extra",
				},
			},
		},
	}
	got := app.renderOfflineAPTEntries()
	want := []string{
		"deb [trusted=yes] file:/opt/repo jammy main",
		"deb [trusted=yes] file:/opt/repo jammy extra",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected apt entries: got=%v want=%v", got, want)
	}
}

func TestDescribeOFEDUsesRunningKernel(t *testing.T) {
	binDir := t.TempDir()
	uname := filepath.Join(binDir, "uname")
	if err := os.WriteFile(uname, []byte("#!/bin/sh\nprintf '5.15.0-test-generic\\n'\n"), 0o755); err != nil {
		t.Fatalf("write uname stub: %v", err)
	}
	t.Setenv("PATH", binDir)

	app := &App{
		Bundle: spec.Bundle{
			Artifacts: spec.Artifacts{
				WorkDir:     "/opt/kunlun",
				OFEDArchive: "/mnt/usb/ofed.tgz",
			},
		},
		Machine: spec.MachineConfig{HostID: "node01"},
		Stages:  map[string]bool{"ofed": true},
		Output:  ioDiscard{},
	}
	got, err := app.Describe()
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if !strings.Contains(got, "-k 5.15.0-test-generic") {
		t.Fatalf("expected running kernel in OFED plan, got:\n%s", got)
	}
}

func TestDescribeDoesNotLogUnameCapture(t *testing.T) {
	binDir := t.TempDir()
	uname := filepath.Join(binDir, "uname")
	if err := os.WriteFile(uname, []byte("#!/bin/sh\nprintf '5.15.0-test-generic\\n'\n"), 0o755); err != nil {
		t.Fatalf("write uname stub: %v", err)
	}
	t.Setenv("PATH", binDir)

	var output strings.Builder
	app := &App{
		Bundle: spec.Bundle{
			Packages: []string{"linux-headers-{{uname_r}}"},
		},
		Machine: spec.MachineConfig{HostID: "node01"},
		Stages:  map[string]bool{"apt": true},
		Output:  &output,
	}
	got, err := app.Describe()
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if strings.Contains(got, "capture: uname -r") || strings.Contains(output.String(), "capture: uname -r") {
		t.Fatalf("did not expect uname capture log in plan output; describe=%q output=%q", got, output.String())
	}
}

func TestDescribeXREUsesUbuntuKernelHeadersDir(t *testing.T) {
	binDir := t.TempDir()
	uname := filepath.Join(binDir, "uname")
	if err := os.WriteFile(uname, []byte("#!/bin/sh\nprintf '5.15.0-test-generic\\n'\n"), 0o755); err != nil {
		t.Fatalf("write uname stub: %v", err)
	}
	t.Setenv("PATH", binDir)

	app := &App{
		Bundle: spec.Bundle{
			Artifacts: spec.Artifacts{
				XREInstaller: "/mnt/usb/xre.run",
			},
			XRE: spec.XREConfig{CardModel: "P900"},
		},
		Machine: spec.MachineConfig{HostID: "node01"},
		Stages:  map[string]bool{"xre": true},
		Output:  ioDiscard{},
	}
	got, err := app.Describe()
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if !strings.Contains(got, "KERNELDIR=/usr/src/linux-headers-5.15.0-test-generic") {
		t.Fatalf("expected Ubuntu kernel headers dir in XRE plan, got:\n%s", got)
	}
}

func TestClassifyP800PartNumbers(t *testing.T) {
	for _, tc := range []struct {
		name      string
		output    string
		want      string
		wantErr   string
		wantCount int
	}{
		{
			name:      "VC",
			output:    "    XPU Part Number                       : B00100300110112\n    XPU Part Number                       : B00100300110112\n",
			want:      "VC",
			wantCount: 2,
		},
		{
			name:      "VD",
			output:    "    XPU Part Number                       : B00100300110312\n",
			want:      "VD",
			wantCount: 1,
		},
		{
			name:    "unknown",
			output:  "    XPU Part Number                       : B00100300110999\n",
			wantErr: "unknown P800 XPU Part Number",
		},
		{
			name:    "mixed",
			output:  "    XPU Part Number                       : B00100300110112\n    XPU Part Number                       : B00100300110312\n",
			wantErr: "mixed P800 XPU variants",
		},
		{
			name:    "missing",
			output:  "Product Name: P800\n",
			wantErr: "did not report any XPU Part Number",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, partNumbers, err := classifyP800PartNumbers(tc.output)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("classify part numbers: %v", err)
			}
			if got != tc.want || len(partNumbers) != tc.wantCount {
				t.Fatalf("unexpected classification: variant=%q part_numbers=%v", got, partNumbers)
			}
		})
	}
}

func TestConfigureXRECardP800VDBacksUpAndOverwritesKunlunConfig(t *testing.T) {
	root := t.TempDir()
	binDir := t.TempDir()
	commandLog := filepath.Join(t.TempDir(), "commands.log")
	xpuSMI := filepath.Join(binDir, "xpu-smi")
	if err := os.WriteFile(xpuSMI, []byte("#!/bin/sh\nprintf '    XPU Part Number                       : B00100300110312\\n'\n"), 0o755); err != nil {
		t.Fatalf("write xpu-smi stub: %v", err)
	}
	for _, name := range []string{"lsof", "rmmod", "modprobe"} {
		mustWriteCommandLogStub(t, binDir, name, commandLog)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	target := filepath.Join(root, "etc/modprobe.d/kunlun.conf")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir modprobe dir: %v", err)
	}
	if err := os.WriteFile(target, []byte("options kunlun KLreg_RegistryDwords=\"RmDisableSMMU=1;\"\n"), 0o644); err != nil {
		t.Fatalf("write default kunlun config: %v", err)
	}
	app := &App{
		Root:   root,
		Output: ioDiscard{},
		now: func() time.Time {
			return time.Date(2026, time.June, 1, 10, 20, 30, 0, time.UTC)
		},
	}
	if err := app.configureXRECard(xreCardModelP800); err != nil {
		t.Fatalf("configure P800 VD: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read updated kunlun config: %v", err)
	}
	if string(got) != renderP800VDKunlunModprobe() {
		t.Fatalf("unexpected updated kunlun config:\n%s", got)
	}
	backup := target + ".bak.20260601_102030"
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("expected backup %s: %v", backup, err)
	}
	logged, err := os.ReadFile(commandLog)
	if err != nil {
		t.Fatalf("read command log: %v", err)
	}
	wantLog := "lsof /dev/xpu*\n" +
		"rmmod kunlun_peermem\n" +
		"rmmod kunlun\n" +
		"modprobe kunlun\n" +
		"modprobe kunlun_peermem\n"
	if string(logged) != wantLog {
		t.Fatalf("unexpected serial reload order:\ngot:\n%swant:\n%s", logged, wantLog)
	}
}

func TestConfigureXRECardP800VCKeepsDefaultKunlunConfig(t *testing.T) {
	root := t.TempDir()
	binDir := t.TempDir()
	xpuSMI := filepath.Join(binDir, "xpu-smi")
	if err := os.WriteFile(xpuSMI, []byte("#!/bin/sh\nprintf '    XPU Part Number                       : B00100300110112\\n'\n"), 0o755); err != nil {
		t.Fatalf("write xpu-smi stub: %v", err)
	}
	t.Setenv("PATH", binDir)

	target := filepath.Join(root, "etc/modprobe.d/kunlun.conf")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir modprobe dir: %v", err)
	}
	defaultConfig := "options kunlun KLreg_RegistryDwords=\"RmDisableSMMU=1;\"\n"
	if err := os.WriteFile(target, []byte(defaultConfig), 0o644); err != nil {
		t.Fatalf("write default kunlun config: %v", err)
	}
	app := &App{Root: root, Output: ioDiscard{}}
	if err := app.configureXRECard(xreCardModelP800); err != nil {
		t.Fatalf("configure P800 VC: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read unchanged kunlun config: %v", err)
	}
	if string(got) != defaultConfig {
		t.Fatalf("expected VC config to stay unchanged, got:\n%s", got)
	}
	backups, err := filepath.Glob(target + ".bak.*")
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	if len(backups) != 0 {
		t.Fatalf("did not expect VC backup files, got %v", backups)
	}
}

func TestConfigureXRECardP900SkipsXPUSMI(t *testing.T) {
	app := &App{Output: ioDiscard{}}
	if err := app.configureXRECard(xreCardModelP900); err != nil {
		t.Fatalf("configure P900: %v", err)
	}
}

func TestNormalizeXRECardModelRejectsMissingAndUnknownValues(t *testing.T) {
	for _, value := range []string{"", "P700"} {
		if _, err := normalizeXRECardModel(value); err == nil {
			t.Fatalf("expected card model %q to fail", value)
		}
	}
}

func TestEnsureSysctlSettingsDryRunDoesNotCreateDirectory(t *testing.T) {
	root := t.TempDir()
	app := &App{
		Root:   root,
		DryRun: true,
		Machine: spec.MachineConfig{
			RDMA: []spec.RDMAConfig{{Name: "ens11np0"}},
		},
		Output: ioDiscard{},
	}
	if err := app.ensureSysctlSettings(); err != nil {
		t.Fatalf("ensure sysctl settings: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "etc")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created etc directory or returned unexpected stat error: %v", err)
	}
}

func TestNewResolvesManagementByMACAndKeepsRDMATargetNames(t *testing.T) {
	root := t.TempDir()
	mustWriteMAC(t, root, "eth-mgmt-a", "aa:bb:cc:dd:ee:01")
	mustWriteMAC(t, root, "eth-mgmt-b", "aa:bb:cc:dd:ee:02")
	mustWriteMAC(t, root, "rdma-a", "aa:bb:cc:dd:ee:11")
	mustWriteMAC(t, root, "rdma-b", "aa:bb:cc:dd:ee:12")
	mustWriteMAC(t, root, "rdma-c", "aa:bb:cc:dd:ee:13")
	mustWriteMAC(t, root, "rdma-d", "aa:bb:cc:dd:ee:14")

	bundle := spec.Bundle{}
	bundle.ApplyDefaults()
	record := spec.MachineRecord{
		HostID:      "xpu11",
		Hostname:    "xpu11",
		MgmtIP:      "10.101.9.11",
		MgmtPrefix:  "26",
		MgmtGateway: "10.101.9.1",
		MgmtMAC1:    "AA-BB-CC-DD-EE-01",
		MgmtMAC2:    "aa:bb:cc:dd:ee:02",
		RDMA: []spec.RDMARecord{
			{IP: "11.1.1.11", MAC: "aa:bb:cc:dd:ee:11"},
			{IP: "11.1.2.11", MAC: "aa:bb:cc:dd:ee:12"},
			{IP: "11.1.3.11", MAC: "aa:bb:cc:dd:ee:13"},
			{IP: "11.1.4.11", MAC: "aa:bb:cc:dd:ee:14"},
		},
	}

	app, err := New(bundle, []spec.MachineRecord{record}, "xpu11", root, true, map[string]bool{"all": true}, ioDiscard{})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	if got := strings.Join(app.Machine.MgmtIfaces, ","); got != "eth-mgmt-a,eth-mgmt-b" {
		t.Fatalf("unexpected mgmt ifaces: %s", got)
	}
	if app.Machine.RDMA[0].Name != "ens11np0" || app.Machine.RDMA[3].Name != "ens17np0" {
		t.Fatalf("unexpected rdma names: %#v", app.Machine.RDMA)
	}
	if app.Machine.RDMA[0].MAC != "aa:bb:cc:dd:ee:11" || app.Machine.RDMA[3].MAC != "aa:bb:cc:dd:ee:14" {
		t.Fatalf("unexpected rdma macs: %#v", app.Machine.RDMA)
	}
}

func TestMatchMachineByMACWhenHostOmitted(t *testing.T) {
	root := t.TempDir()
	mustWriteMAC(t, root, "eth-mgmt-a", "aa:bb:cc:dd:ee:01")
	mustWriteMAC(t, root, "eth-mgmt-b", "aa:bb:cc:dd:ee:02")
	mustWriteMAC(t, root, "rdma-a", "aa:bb:cc:dd:ee:11")
	mustWriteMAC(t, root, "rdma-b", "aa:bb:cc:dd:ee:12")
	mustWriteMAC(t, root, "rdma-c", "aa:bb:cc:dd:ee:13")
	mustWriteMAC(t, root, "rdma-d", "aa:bb:cc:dd:ee:14")

	bundle := spec.Bundle{}
	bundle.ApplyDefaults()
	records := []spec.MachineRecord{
		{
			HostID:      "other",
			Hostname:    "other",
			MgmtIP:      "10.101.9.99",
			MgmtPrefix:  "26",
			MgmtGateway: "10.101.9.1",
			MgmtMAC1:    "aa:bb:cc:dd:ee:99",
			MgmtMAC2:    "aa:bb:cc:dd:ee:98",
			RDMA: []spec.RDMARecord{
				{IP: "11.1.1.99"},
				{IP: "11.1.2.99"},
				{IP: "11.1.3.99"},
				{IP: "11.1.4.99"},
			},
		},
		{
			HostID:      "xpu11",
			Hostname:    "xpu11",
			MgmtIP:      "10.101.9.11",
			MgmtPrefix:  "26",
			MgmtGateway: "10.101.9.1",
			MgmtMAC1:    "aa:bb:cc:dd:ee:01",
			MgmtMAC2:    "aa:bb:cc:dd:ee:02",
			RDMA: []spec.RDMARecord{
				{IP: "11.1.1.11", MAC: "aa:bb:cc:dd:ee:11"},
				{IP: "11.1.2.11", MAC: "aa:bb:cc:dd:ee:12"},
				{IP: "11.1.3.11", MAC: "aa:bb:cc:dd:ee:13"},
				{IP: "11.1.4.11", MAC: "aa:bb:cc:dd:ee:14"},
			},
		},
	}

	app, err := New(bundle, records, "", root, true, map[string]bool{"all": true}, ioDiscard{})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	if app.Machine.HostID != "xpu11" {
		t.Fatalf("expected xpu11, got %s", app.Machine.HostID)
	}
}

func TestNewFallsBackToInterfaceNamesWhenMACMissing(t *testing.T) {
	root := t.TempDir()
	bundle := spec.Bundle{}
	bundle.ApplyDefaults()
	record := spec.MachineRecord{
		HostID:      "xpu12",
		Hostname:    "xpu12",
		MgmtIP:      "10.101.9.12",
		MgmtPrefix:  "26",
		MgmtGateway: "10.101.9.1",
		MgmtIface1:  "ens20f0np0",
		MgmtIface2:  "ens20f1np1",
		RDMA: []spec.RDMARecord{
			{Name: "ens11np0", IP: "11.1.1.12"},
			{Name: "ens13np0", IP: "11.1.2.12"},
			{Name: "ens15np0", IP: "11.1.3.12"},
			{Name: "ens17np0", IP: "11.1.4.12"},
		},
	}

	app, err := New(bundle, []spec.MachineRecord{record}, "xpu12", root, true, map[string]bool{"all": true}, ioDiscard{})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	if got := strings.Join(app.Machine.MgmtIfaces, ","); got != "ens20f0np0,ens20f1np1" {
		t.Fatalf("unexpected mgmt ifaces: %s", got)
	}
	if app.Machine.RDMA[0].Name != "ens11np0" || app.Machine.RDMA[3].Name != "ens17np0" {
		t.Fatalf("unexpected rdma names: %#v", app.Machine.RDMA)
	}
}

func TestNewSupportsSingleManagementInterface(t *testing.T) {
	root := t.TempDir()
	bundle := spec.Bundle{}
	bundle.ApplyDefaults()
	record := spec.MachineRecord{
		HostID:      "xpu13",
		Hostname:    "xpu13",
		MgmtIP:      "10.101.9.13",
		MgmtPrefix:  "26",
		MgmtGateway: "10.101.9.1",
		MgmtIface1:  "ens20f0np0",
		RDMA: []spec.RDMARecord{
			{Name: "ens11np0", IP: "11.1.1.13"},
			{Name: "ens13np0", IP: "11.1.2.13"},
			{Name: "ens15np0", IP: "11.1.3.13"},
			{Name: "ens17np0", IP: "11.1.4.13"},
		},
	}

	app, err := New(bundle, []spec.MachineRecord{record}, "xpu13", root, true, map[string]bool{"all": true}, ioDiscard{})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	if got := strings.Join(app.Machine.MgmtIfaces, ","); got != "ens20f0np0" {
		t.Fatalf("unexpected mgmt ifaces: %s", got)
	}
}

func TestNewHonorsExplicitSecondManagementInterfaceWhenBundleDefaultsToSingle(t *testing.T) {
	root := t.TempDir()
	bundle := spec.Bundle{
		Defaults: spec.Defaults{
			MgmtInterfaces: []string{"ens20f0np0"},
		},
	}
	bundle.ApplyDefaults()
	record := spec.MachineRecord{
		HostID:      "xpu14",
		Hostname:    "xpu14",
		MgmtIP:      "10.101.9.14",
		MgmtPrefix:  "26",
		MgmtGateway: "10.101.9.1",
		MgmtIface1:  "ens20f0np0",
		MgmtIface2:  "ens20f1np1",
		RDMA: []spec.RDMARecord{
			{Name: "ens11np0", IP: "11.1.1.14"},
			{Name: "ens13np0", IP: "11.1.2.14"},
			{Name: "ens15np0", IP: "11.1.3.14"},
			{Name: "ens17np0", IP: "11.1.4.14"},
		},
	}

	app, err := New(bundle, []spec.MachineRecord{record}, "xpu14", root, true, map[string]bool{"all": true}, ioDiscard{})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	if got := strings.Join(app.Machine.MgmtIfaces, ","); got != "ens20f0np0,ens20f1np1" {
		t.Fatalf("unexpected mgmt ifaces: %s", got)
	}
}

func TestPrepareOfflineAPTMaterialsCopiesDirectory(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(source, "dists"), 0o755); err != nil {
		t.Fatalf("mkdir source repo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "dists", "Release"), []byte("jammy\n"), 0o644); err != nil {
		t.Fatalf("write source repo file: %v", err)
	}
	app := &App{
		Root: root,
		Bundle: spec.Bundle{
			OfflineAPT: spec.OfflineAPTConfig{
				MaterialPath: source,
				CopyTo:       "/opt/repo",
			},
		},
		Output: ioDiscard{},
	}
	if err := app.prepareOfflineAPTMaterials(); err != nil {
		t.Fatalf("prepare offline apt materials: %v", err)
	}
	target := filepath.Join(root, "opt", "repo", "dists", "Release")
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read copied repo file: %v", err)
	}
	if string(data) != "jammy\n" {
		t.Fatalf("unexpected copied repo file content: %q", string(data))
	}
}

func TestPrepareOfflineAPTMaterialsSkipsUnchangedDirectory(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(source, "dists"), 0o755); err != nil {
		t.Fatalf("mkdir source repo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "dists", "Release"), []byte("jammy\n"), 0o644); err != nil {
		t.Fatalf("write source repo file: %v", err)
	}
	app := &App{
		Root: root,
		Bundle: spec.Bundle{
			OfflineAPT: spec.OfflineAPTConfig{
				MaterialPath: source,
				CopyTo:       "/opt/repo",
			},
		},
		Output: ioDiscard{},
	}
	if err := app.prepareOfflineAPTMaterials(); err != nil {
		t.Fatalf("first prepare offline apt materials: %v", err)
	}
	if err := app.prepareOfflineAPTMaterials(); err != nil {
		t.Fatalf("second prepare offline apt materials: %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(root, "opt", "repo.bak.*"))
	if err != nil {
		t.Fatalf("glob repo backups: %v", err)
	}
	if len(backups) != 0 {
		t.Fatalf("expected no repo backups for unchanged material, got %v", backups)
	}
}

func TestPlannedFilesRespectSelectedStages(t *testing.T) {
	bundle := spec.Bundle{}
	bundle.ApplyDefaults()
	app := &App{
		Bundle: bundle,
		Machine: spec.MachineConfig{
			MgmtIfaces: []string{"enp2s0", "enp3s0"},
			RDMA: []spec.RDMAConfig{
				{Name: "enp10s0"},
				{Name: "enp11s0"},
			},
		},
		Stages: map[string]bool{
			"network": true,
			"udev":    true,
			"sysctl":  true,
			"iommu":   true,
		},
	}
	got := app.plannedFiles()
	want := []string{
		"/etc/netplan/00-kunlun-bond.yaml",
		"/etc/netplan/10-kunlun-enp10s0.yaml",
		"/etc/networkd-dispatcher/routable.d/config_rt_enp10s0.sh",
		"/etc/netplan/10-kunlun-enp11s0.yaml",
		"/etc/networkd-dispatcher/routable.d/config_rt_enp11s0.sh",
		"/etc/udev/rules.d/70-persistent-net.rules",
		"/etc/sysctl.conf",
		"/etc/default/grub",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected planned files: got=%v want=%v", got, want)
	}
}

func TestPlannedFilesSkipRDMANetworkFilesWhenRouteConfigDisabled(t *testing.T) {
	disabled := false
	bundle := spec.Bundle{
		Defaults: spec.Defaults{
			RDMAConfigureIPRoute: &disabled,
		},
	}
	bundle.ApplyDefaults()
	app := &App{
		Bundle: bundle,
		Machine: spec.MachineConfig{
			MgmtIfaces: []string{"enp2s0"},
			RDMA: []spec.RDMAConfig{
				{Name: "enp10s0"},
			},
		},
		Stages: map[string]bool{
			"network": true,
		},
	}
	got := app.plannedFiles()
	want := []string{
		"/etc/netplan/00-kunlun-bond.yaml",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected planned files: got=%v want=%v", got, want)
	}
}

func TestDescribeSkipsRDMAActionsWhenRDMAIsDisabled(t *testing.T) {
	disabled := false
	bundle := spec.Bundle{
		Defaults: spec.Defaults{
			RDMAExsist: &disabled,
		},
	}
	bundle.ApplyDefaults()
	app := &App{
		Bundle: bundle,
		Machine: spec.MachineConfig{
			HostID:      "node01",
			MgmtIfaces:  []string{"enp2s0"},
			MgmtIP:      "172.16.18.11",
			MgmtPrefix:  24,
			MgmtGateway: "172.16.18.1",
		},
		Stages: map[string]bool{
			"network":   true,
			"mlxconfig": true,
			"post":      true,
		},
	}
	got, err := app.Describe()
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	for _, want := range []string{
		"skip all RDMA actions because rdma_exsist=false",
		"skip mlxconfig: rdma_exsist=false",
		"skip RDMA post-boot service because rdma_exsist=false",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in describe output, got:\n%s", want, got)
		}
	}
}

func TestRequiredPackagesDeduplicatesPostPowerDependencies(t *testing.T) {
	app := &App{
		Bundle: spec.Bundle{
			Packages: []string{"linux-headers-{{uname_r}}", "custom-package", "ipmitool"},
			PostPowerAction: spec.PostPowerAction{
				Action: "soft",
			},
		},
	}
	got, err := app.requiredPackagesWithUname("5.15.0-125-generic")
	if err != nil {
		t.Fatalf("required packages: %v", err)
	}
	want := []string{
		"linux-headers-5.15.0-125-generic",
		"custom-package",
		"ipmitool",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected packages: got=%v want=%v", got, want)
	}
}

func TestRequiredPackagesIncludeIPMIToolForPostPowerAction(t *testing.T) {
	app := &App{
		Bundle: spec.Bundle{
			Packages: []string{"custom-package"},
			PostPowerAction: spec.PostPowerAction{
				Action: "cycle",
			},
		},
	}
	got, err := app.requiredPackagesWithUname("5.15.0-125-generic")
	if err != nil {
		t.Fatalf("required packages: %v", err)
	}
	want := []string{
		"custom-package",
		"linux-headers-5.15.0-125-generic",
		"ipmitool",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected packages: got=%v want=%v", got, want)
	}
}

func TestRunPostStageSkipsWhenConfirmationDenied(t *testing.T) {
	confirm := true
	var output strings.Builder
	app := &App{
		Bundle: spec.Bundle{
			PostPowerAction: spec.PostPowerAction{
				Action:  "soft",
				Confirm: &confirm,
			},
		},
		DryRun: true,
		Output: &output,
	}
	if err := app.runPostStage(); err != nil {
		t.Fatalf("run post stage: %v", err)
	}
	if !strings.Contains(output.String(), "power soft") {
		t.Fatalf("expected confirmation log, got: %s", output.String())
	}
}

func TestPostPowerActionSupportsHardOff(t *testing.T) {
	app := &App{
		Bundle: spec.Bundle{
			PostPowerAction: spec.PostPowerAction{
				Action: "off",
			},
		},
	}
	got, err := app.postPowerAction()
	if err != nil {
		t.Fatalf("post power action: %v", err)
	}
	if got.Action != "off" || got.Confirm {
		t.Fatalf("unexpected post power action: %#v", got)
	}
}

func TestApplyRejectsAlternateRootOutsideDryRun(t *testing.T) {
	app := &App{
		Root:   t.TempDir(),
		Stages: map[string]bool{"all": true},
		Output: ioDiscard{},
	}
	err := app.Apply()
	if err == nil {
		t.Fatal("expected alternate-root apply to fail")
	}
	if !strings.Contains(err.Error(), "does not support --root") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEnsureHostnameRunsWhenHostSpecified(t *testing.T) {
	var output strings.Builder
	app := &App{
		HostSpecified: true,
		DryRun:        true,
		Machine: spec.MachineConfig{
			Hostname: "envinit-test-hostname",
		},
		Output: &output,
	}
	if err := app.ensureHostname(); err != nil {
		t.Fatalf("ensure hostname: %v", err)
	}
	if !strings.Contains(output.String(), "set hostname") {
		t.Fatalf("expected hostname change log, got:\n%s", output.String())
	}
}

func TestInstallPostPackagesPreservesBundleOrder(t *testing.T) {
	binDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "dpkg.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + marker + "\n"
	path := filepath.Join(binDir, "dpkg")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write dpkg stub: %v", err)
	}
	t.Setenv("PATH", binDir)

	app := &App{
		Bundle: spec.Bundle{
			PostPackages: []string{
				"/mnt/usb/pkg-a.deb",
				"/mnt/usb/pkg-b.deb",
			},
		},
		Output: ioDiscard{},
	}
	if err := app.installPostPackages(); err != nil {
		t.Fatalf("install post packages: %v", err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read dpkg log: %v", err)
	}
	want := "-i /mnt/usb/pkg-a.deb\n-i /mnt/usb/pkg-b.deb\n"
	if string(data) != want {
		t.Fatalf("unexpected install order: got %q want %q", string(data), want)
	}
}

func TestRunPostStageRunsCopyAndCommandsAfterRDMABootService(t *testing.T) {
	source := filepath.Join(t.TempDir(), "agent.service")
	if err := os.WriteFile(source, []byte("[Service]\nExecStart=/bin/true\n"), 0o644); err != nil {
		t.Fatalf("write post copy source: %v", err)
	}

	var output strings.Builder
	app := &App{
		Root:   t.TempDir(),
		DryRun: true,
		Bundle: spec.Bundle{
			PostTasks: []spec.PostTask{
				{Type: "copy", Source: source, Target: "/etc/systemd/system/agent.service"},
				{Type: "cmd", Command: "systemctl daemon-reload"},
			},
			PostPowerAction: spec.PostPowerAction{
				Action: "none",
			},
		},
		Machine: spec.MachineConfig{
			RDMA: []spec.RDMAConfig{{Name: "ens11np0"}},
		},
		Output: &output,
	}
	if err := app.runPostStage(); err != nil {
		t.Fatalf("run post stage: %v", err)
	}
	got := output.String()
	serviceIdx := strings.Index(got, "write /usr/local/sbin/kunlun-post-boot.sh")
	copyIdx := strings.Index(got, "copy "+source+" ->")
	cmdIdx := strings.Index(got, "run: bash -lc systemctl daemon-reload")
	if serviceIdx == -1 || copyIdx == -1 || cmdIdx == -1 {
		t.Fatalf("missing expected post actions in output:\n%s", got)
	}
	if !(serviceIdx < copyIdx && copyIdx < cmdIdx) {
		t.Fatalf("unexpected post action order:\n%s", got)
	}
}

func TestRunPostTasksSupportsFilesystemAndCommandTasks(t *testing.T) {
	binDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "cmd.log")
	bash := filepath.Join(binDir, "bash")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > " + marker + "\n"
	if err := os.WriteFile(bash, []byte(script), 0o755); err != nil {
		t.Fatalf("write bash stub: %v", err)
	}
	t.Setenv("PATH", binDir)

	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "agent.service")
	if err := os.WriteFile(source, []byte("[Service]\nExecStart=/bin/true\n"), 0o600); err != nil {
		t.Fatalf("write post task source: %v", err)
	}
	removeTarget := filepath.Join(root, "opt", "playbook", "remove-me")
	if err := os.MkdirAll(filepath.Dir(removeTarget), 0o755); err != nil {
		t.Fatalf("mkdir remove target parent: %v", err)
	}
	if err := os.WriteFile(removeTarget, []byte("remove\n"), 0o644); err != nil {
		t.Fatalf("write remove target: %v", err)
	}

	app := &App{
		Root: root,
		Bundle: spec.Bundle{
			PostTasks: []spec.PostTask{
				{Type: "mkdir", Path: "/opt/playbook", Mode: "0750"},
				{Type: "copy", Source: source, Target: "/opt/playbook/agent.service", Mode: "0644"},
				{Type: "mv", Source: "/opt/playbook/agent.service", Target: "/etc/systemd/system/agent.service"},
				{Type: "rm", Path: "/opt/playbook/remove-me"},
				{Type: "cmd", Command: "systemctl daemon-reload && systemctl enable agent.service"},
			},
		},
		Output: ioDiscard{},
	}
	if err := app.runPostTasks(); err != nil {
		t.Fatalf("run post tasks: %v", err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	got := strings.TrimSpace(string(data))
	want := "-lc systemctl daemon-reload && systemctl enable agent.service"
	if got != want {
		t.Fatalf("unexpected bash args: got %q want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(root, "opt", "playbook", "agent.service")); !os.IsNotExist(err) {
		t.Fatalf("expected moved source to be absent, stat err=%v", err)
	}
	target := filepath.Join(root, "etc", "systemd", "system", "agent.service")
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat moved service: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("unexpected service mode: %04o", info.Mode().Perm())
	}
	if _, err := os.Stat(removeTarget); !os.IsNotExist(err) {
		t.Fatalf("expected remove target to be gone, stat err=%v", err)
	}
}

func TestRenderPostBootScriptUsesPlannedRDMAInterfaces(t *testing.T) {
	content := renderPostBootScript(spec.MachineConfig{
		RDMA: []spec.RDMAConfig{
			{Name: "ens11np0"},
			{Name: "ens13np0"},
		},
	}, "# custom hook\n")
	for _, want := range []string{
		`"ens11np0"`,
		`"ens13np0"`,
		`if ! ethtool -G "$iface" rx 8192 tx 8192; then`,
		`if ! bus_info=$(ethtool -i "$iface" 2>/dev/null | awk -F': ' '$1 == "bus-info" {print $2; exit}'); then`,
		`if ! mlxreg -d "$bus_info" --reg_name ROCE_ACCL --set adaptive_routing_forced_en=0x1 --yes; then`,
		"# custom hook",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected %q in post boot script, got:\n%s", want, content)
		}
	}
}

func TestExtractPostBootCustomBlockPreservesUserContent(t *testing.T) {
	content := strings.Join([]string{
		"managed header",
		postBootCustomBegin,
		"echo user custom action",
		postBootCustomEnd,
		"managed footer",
	}, "\n")
	got := extractPostBootCustomBlock(content)
	if !strings.Contains(got, "echo user custom action") {
		t.Fatalf("expected custom block to be preserved, got %q", got)
	}
}

func TestUdevStageDiscoversRDMAByPCIOrderAndRenamesTemporarily(t *testing.T) {
	root := t.TempDir()
	mustWriteNetDevice(t, root, "mgmt0", "aa:bb:cc:dd:ee:01", "0000:10:00.0", "mlx5_core", 0, "p0")
	mustWriteNetDevice(t, root, "rdma-b", "aa:bb:cc:dd:ee:22", "0000:30:00.0", "mlx5_core", 0, "p0")
	mustWriteNetDevice(t, root, "rdma-a", "aa:bb:cc:dd:ee:11", "0000:20:00.0", "mlx5_core", 0, "p0")

	var output strings.Builder
	app := &App{
		Root:   root,
		DryRun: true,
		Bundle: spec.Bundle{
			Defaults: spec.Defaults{
				RDMAExsist: boolPtr(true),
			},
		},
		Machine: spec.MachineConfig{
			MgmtIfaces: []string{"mgmt0"},
			MgmtMACs:   []string{"aa:bb:cc:dd:ee:01"},
			RDMA: []spec.RDMAConfig{
				{Name: "ens15np0"},
				{Name: "ens16np0"},
			},
		},
		Output: &output,
	}

	bindings, err := app.interfaceBindings()
	if err != nil {
		t.Fatalf("interface bindings: %v", err)
	}
	content := renderUdevRules(bindings)
	for _, want := range []string{
		`ATTR{address}=="aa:bb:cc:dd:ee:11", ATTR{type}=="1", NAME="ens15np0"`,
		`ATTR{address}=="aa:bb:cc:dd:ee:22", ATTR{type}=="1", NAME="ens16np0"`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected %q in udev rules:\n%s", want, content)
		}
	}

	if err := app.runUdevStage(); err != nil {
		t.Fatalf("run udev stage: %v", err)
	}
	got := output.String()
	for _, want := range []string{
		"run: udevadm control --reload-rules",
		"run: ip link set dev rdma-a down",
		"run: ip link set dev rdma-a name ei-tmp0",
		"run: ip link set dev ei-tmp0 name ens15np0",
		"run: ip link set dev rdma-b name ei-tmp1",
		"run: ip link set dev ei-tmp1 name ens16np0",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in output:\n%s", want, got)
		}
	}
}

func TestRenameRDMATemporarilyUsesTwoPhaseRenameForNameConflicts(t *testing.T) {
	root := t.TempDir()
	mustWriteMAC(t, root, "ens15np0", "aa:bb:cc:dd:ee:22")
	mustWriteMAC(t, root, "enp20s0", "aa:bb:cc:dd:ee:11")

	var output strings.Builder
	app := &App{
		Root:   root,
		DryRun: true,
		Output: &output,
	}
	err := app.renameRDMATemporarily([]interfaceBinding{
		{Kind: "rdma", Name: "ens15np0", CurrentName: "enp20s0"},
		{Kind: "rdma", Name: "ens16np0", CurrentName: "ens15np0"},
	})
	if err != nil {
		t.Fatalf("rename rdma temporarily: %v", err)
	}
	got := output.String()
	firstMove := strings.Index(got, "run: ip link set dev enp20s0 name ei-tmp0")
	conflictMove := strings.Index(got, "run: ip link set dev ens15np0 name ei-tmp1")
	targetMove := strings.Index(got, "run: ip link set dev ei-tmp0 name ens15np0")
	if firstMove == -1 || conflictMove == -1 || targetMove == -1 {
		t.Fatalf("missing expected rename commands:\n%s", got)
	}
	if !(firstMove < targetMove && conflictMove < targetMove) {
		t.Fatalf("expected all current names to move to temp names before target renames:\n%s", got)
	}
}

func TestEnsureInterfacesReadyReportsMissingNames(t *testing.T) {
	root := t.TempDir()
	mustWriteMAC(t, root, "enp10s0", "aa:bb:cc:dd:ee:10")
	app := &App{
		Root: root,
		Machine: spec.MachineConfig{
			MgmtIfaces: []string{"enp10s0", "enp11s0"},
			RDMA: []spec.RDMAConfig{
				{Name: "enp12s0"},
			},
		},
	}
	err := app.ensureInterfacesReady()
	if err == nil {
		t.Fatal("expected missing interface error")
	}
	if !strings.Contains(err.Error(), "enp11s0") || !strings.Contains(err.Error(), "enp12s0") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRefreshInitramfsFallsBackToUpdateInitramfs(t *testing.T) {
	binDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "called.txt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$0 $*\" > " + marker + "\n"
	path := filepath.Join(binDir, "update-initramfs")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write update-initramfs stub: %v", err)
	}
	t.Setenv("PATH", binDir)

	app := &App{Output: ioDiscard{}}
	if err := app.refreshInitramfs(); err != nil {
		t.Fatalf("refresh initramfs: %v", err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if got := strings.TrimSpace(string(data)); !strings.Contains(got, "update-initramfs -u") {
		t.Fatalf("expected update-initramfs fallback, got %q", got)
	}
}

func TestDescribeIncludesMstStartForMlxConfig(t *testing.T) {
	app := &App{
		Bundle: spec.Bundle{
			MlxConfig: spec.MlxConfig{
				DeviceGlob: "/dev/mst/mt4129_pciconf*",
				Settings: map[string]string{
					"CNP_DSCP_P1": "48",
				},
			},
		},
		Machine: spec.MachineConfig{
			HostID:      "node01",
			MgmtIfaces:  []string{"enp2s0"},
			MgmtIP:      "172.16.18.11",
			MgmtPrefix:  24,
			MgmtGateway: "172.16.18.1",
		},
		Stages: map[string]bool{
			"mlxconfig": true,
		},
	}
	got, err := app.Describe()
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if !strings.Contains(got, "run mst start") {
		t.Fatalf("expected mst start in describe output, got:\n%s", got)
	}
}

func TestDescribeIncludesDetailedStageActions(t *testing.T) {
	confirm := true
	bundle := spec.Bundle{
		PostPowerAction: spec.PostPowerAction{
			Action:  "soft",
			Confirm: &confirm,
		},
		PostPackages: []string{
			"/mnt/usb/pkg-a.deb",
			"/mnt/usb/pkg-b.deb",
		},
		PostTasks: []spec.PostTask{
			{Type: "copy", Source: "/mnt/usb/agent.service", Target: "/etc/systemd/system/agent.service"},
			{Type: "cmd", Command: "systemctl daemon-reload"},
		},
		Defaults: spec.Defaults{
			BackupExistingNetplan: true,
		},
	}
	bundle.ApplyDefaults()
	app := &App{
		Bundle: bundle,
		Machine: spec.MachineConfig{
			HostID:        "node01",
			Hostname:      "node01",
			MgmtBondName:  "bond0",
			MgmtIP:        "172.16.18.11",
			MgmtPrefix:    24,
			MgmtGateway:   "172.16.18.1",
			MgmtIfaces:    []string{"enp2s0", "enp3s0"},
			MgmtMTU:       1500,
			BondMode:      "802.3ad",
			BondLACPRate:  "slow",
			BondXmitHash:  "layer3+4",
			RDMAMTU:       9000,
			RouteCIDR:     "11.1.0.0/21",
			RoutePriority: 32761,
			RDMA: []spec.RDMAConfig{
				{Name: "enp10s0", IP: "11.1.1.11", Prefix: 24, Gateway: "11.1.1.1", Table: 101},
			},
		},
		Stages: map[string]bool{
			"network": true,
			"post":    true,
		},
	}
	got, err := app.Describe()
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	for _, want := range []string{
		"Detailed actions:",
		"[network]",
		"write /etc/netplan/00-kunlun-bond.yaml with bond bond0 (mode=802.3ad) over enp2s0,enp3s0",
		"run netplan generate",
		"run bash /etc/networkd-dispatcher/routable.d/config_rt_enp10s0.sh",
		"enable RoCE adaptive routing on enp10s0",
		"[post]",
		"install post package 1/2 with dpkg -i /mnt/usb/pkg-a.deb",
		"install post package 2/2 with dpkg -i /mnt/usb/pkg-b.deb",
		"write /usr/local/sbin/kunlun-post-boot.sh",
		"write and enable /etc/systemd/system/kunlun-post-boot.service",
		"post task 1/2: copy /mnt/usb/agent.service to /etc/systemd/system/agent.service",
		"post task 2/2: run systemctl daemon-reload",
		"ask for confirmation before running ipmitool power soft",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in describe output, got:\n%s", want, got)
		}
	}
}

func TestParseEthtoolBusInfo(t *testing.T) {
	got, err := parseEthtoolBusInfo("driver: mlx5_core\nversion: 5.15\nbus-info: 0000:0b:00.0\n")
	if err != nil {
		t.Fatalf("parse bus-info: %v", err)
	}
	if got != "0000:0b:00.0" {
		t.Fatalf("unexpected bus-info: %s", got)
	}
}

func TestParseEthtoolBusInfoRequiresValue(t *testing.T) {
	if _, err := parseEthtoolBusInfo("driver: mlx5_core\nbus-info: \n"); err == nil {
		t.Fatal("expected missing bus-info error")
	}
}

func TestEnableRoCEAdaptiveRoutingIsBestEffort(t *testing.T) {
	binDir := t.TempDir()
	ethtool := filepath.Join(binDir, "ethtool")
	if err := os.WriteFile(ethtool, []byte("#!/bin/sh\nprintf 'driver: mlx5_core\\nbus-info: 0000:0b:00.0\\n'\n"), 0o755); err != nil {
		t.Fatalf("write ethtool stub: %v", err)
	}
	mlxreg := filepath.Join(binDir, "mlxreg")
	if err := os.WriteFile(mlxreg, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write mlxreg stub: %v", err)
	}
	t.Setenv("PATH", binDir)

	var output strings.Builder
	app := &App{
		Machine: spec.MachineConfig{
			RDMA: []spec.RDMAConfig{{Name: "ens11np0"}},
		},
		Output: &output,
	}
	app.enableRoCEAdaptiveRouting()
	if !strings.Contains(output.String(), "non-fatal command failed") {
		t.Fatalf("expected non-fatal mlxreg failure log, got:\n%s", output.String())
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) {
	return len(p), nil
}

func mustWriteMAC(t *testing.T, root string, iface string, mac string) {
	t.Helper()
	path := filepath.Join(root, "sys/class/net", iface)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(filepath.Join(path, "address"), []byte(mac+"\n"), 0o644); err != nil {
		t.Fatalf("write mac %s: %v", iface, err)
	}
}

func mustWriteNetDevice(t *testing.T, root string, iface string, mac string, pci string, driver string, devPort int, physPortName string) {
	t.Helper()
	netDir := filepath.Join(root, "sys/class/net", iface)
	if err := os.MkdirAll(netDir, 0o755); err != nil {
		t.Fatalf("mkdir net dir %s: %v", netDir, err)
	}
	if err := os.WriteFile(filepath.Join(netDir, "address"), []byte(mac+"\n"), 0o644); err != nil {
		t.Fatalf("write mac %s: %v", iface, err)
	}
	if err := os.WriteFile(filepath.Join(netDir, "dev_port"), []byte(fmt.Sprintf("%d\n", devPort)), 0o644); err != nil {
		t.Fatalf("write dev_port %s: %v", iface, err)
	}
	if err := os.WriteFile(filepath.Join(netDir, "phys_port_name"), []byte(physPortName+"\n"), 0o644); err != nil {
		t.Fatalf("write phys_port_name %s: %v", iface, err)
	}
	deviceDir := filepath.Join(root, "sys/devices/pci0000:00", pci)
	if err := os.MkdirAll(deviceDir, 0o755); err != nil {
		t.Fatalf("mkdir device dir %s: %v", deviceDir, err)
	}
	driverDir := filepath.Join(root, "sys/bus/pci/drivers", driver)
	if err := os.MkdirAll(driverDir, 0o755); err != nil {
		t.Fatalf("mkdir driver dir %s: %v", driverDir, err)
	}
	if err := os.Symlink(deviceDir, filepath.Join(netDir, "device")); err != nil {
		t.Fatalf("symlink device for %s: %v", iface, err)
	}
	if err := os.Symlink(driverDir, filepath.Join(deviceDir, "driver")); err != nil {
		t.Fatalf("symlink driver for %s: %v", iface, err)
	}
}

func boolPtr(value bool) *bool {
	return &value
}

func mustWriteCommandLogStub(t *testing.T, binDir string, name string, logPath string) {
	t.Helper()
	content := fmt.Sprintf("#!/bin/sh\nprintf '%%s %%s\\n' %q \"$*\" >> %q\n", name, logPath)
	if err := os.WriteFile(filepath.Join(binDir, name), []byte(content), 0o755); err != nil {
		t.Fatalf("write %s stub: %v", name, err)
	}
}
