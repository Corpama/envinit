package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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
	if !strings.Contains(content, `ip route replace "$ROUTE_CIDR" dev "$DEV" scope link table "$TABLE" src "$IP" proto static`) {
		t.Fatalf("expected link-scope RDMA CIDR route in route script, got:\n%s", content)
	}
	if strings.Contains(content, `ip route replace "$ROUTE_CIDR" via "$GW"`) {
		t.Fatalf("did not expect RDMA CIDR route to go via gateway, got:\n%s", content)
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

func TestRenderNetworkManagerIfcfgBondAndRoutes(t *testing.T) {
	machine := spec.MachineConfig{
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
	bond := renderBondIfcfg(machine, true)
	for _, want := range []string{
		"DEVICE=bond0",
		"TYPE=Bond",
		"IPADDR=10.101.9.11",
		"PREFIX=26",
		"GATEWAY=10.101.9.1",
		`BONDING_OPTS="mode=802.3ad miimon=100 lacp_rate=slow xmit_hash_policy=layer3+4"`,
		"NM_CONTROLLED=yes",
		"DNS1=10.0.0.2",
	} {
		if !strings.Contains(bond, want) {
			t.Fatalf("expected %q in bond ifcfg:\n%s", want, bond)
		}
	}

	slave := renderBondSlaveIfcfg(machine, "ens20f0np0", true)
	for _, want := range []string{"DEVICE=ens20f0np0", "MASTER=bond0", "SLAVE=yes", "NM_CONTROLLED=yes"} {
		if !strings.Contains(slave, want) {
			t.Fatalf("expected %q in slave ifcfg:\n%s", want, slave)
		}
	}

	rdma := spec.RDMAConfig{Name: "ens11np0", IP: "11.1.1.11", Prefix: 24, Gateway: "11.1.1.1", Table: 101}
	ifcfg := renderRDMAIfcfg(rdma, 9000, true)
	for _, want := range []string{"DEVICE=ens11np0", "IPADDR=11.1.1.11", "PREFIX=24", "MTU=9000"} {
		if !strings.Contains(ifcfg, want) {
			t.Fatalf("expected %q in rdma ifcfg:\n%s", want, ifcfg)
		}
	}
	route := renderIfcfgRoute(rdma, "11.1.0.0/21")
	if !strings.Contains(route, "default via 11.1.1.1 dev ens11np0 table 101") ||
		!strings.Contains(route, "11.1.0.0/21 dev ens11np0 scope link table 101 src 11.1.1.11 proto static") {
		t.Fatalf("unexpected route file:\n%s", route)
	}
	rule := renderIfcfgRule(rdma, 32761)
	if !strings.Contains(rule, "from all oif ens11np0 table 101 priority 32761") ||
		!strings.Contains(rule, "from 11.1.1.11 table 101 priority 32761") {
		t.Fatalf("unexpected rule file:\n%s", rule)
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

func TestRenderOfflineRepoEntriesUseCopiedTarget(t *testing.T) {
	app := &App{}
	repo := spec.OfflineAPTConfig{
		MaterialPath: "/mnt/usb/rpm-repo",
		CopyTo:       "/opt/rpm-repo",
		Entries: []string{
			"baseurl=file://{{offline_repo_target}}",
			"gpgcheck=0",
		},
	}
	got := app.renderOfflineRepoEntries(repo)
	want := []string{
		"baseurl=file:///opt/rpm-repo",
		"gpgcheck=0",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected repo entries: got=%v want=%v", got, want)
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

func TestNewAutoDetectsSingleManagementInterface(t *testing.T) {
	root := t.TempDir()
	mustWriteNetDevice(t, root, "eno1", "aa:bb:cc:dd:ee:01", "0000:20:00.0", "ixgbe", 0, "p0")

	bundle := spec.Bundle{}
	bundle.ApplyDefaults()
	record := spec.MachineRecord{
		HostID:      "xpu-auto1",
		Hostname:    "xpu-auto1",
		MgmtIP:      "10.101.9.15",
		MgmtPrefix:  "26",
		MgmtGateway: "10.101.9.1",
		RDMA: []spec.RDMARecord{
			{IP: "11.1.1.15"},
			{IP: "11.1.2.15"},
			{IP: "11.1.3.15"},
			{IP: "11.1.4.15"},
		},
	}

	app, err := New(bundle, []spec.MachineRecord{record}, "xpu-auto1", root, true, map[string]bool{"all": true}, ioDiscard{})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	if got := strings.Join(app.Machine.MgmtIfaces, ","); got != "eno1" {
		t.Fatalf("unexpected auto mgmt ifaces: %s", got)
	}
	if got := strings.Join(app.Machine.MgmtMACs, ","); got != "aa:bb:cc:dd:ee:01" {
		t.Fatalf("unexpected auto mgmt macs: %s", got)
	}
}

func TestNewAutoDetectsTwoManagementInterfacesForBond(t *testing.T) {
	root := t.TempDir()
	mustWriteNetDevice(t, root, "eno-b", "aa:bb:cc:dd:ee:02", "0000:30:00.0", "ixgbe", 0, "p0")
	mustWriteNetDevice(t, root, "eno-a", "aa:bb:cc:dd:ee:01", "0000:20:00.0", "ixgbe", 0, "p0")

	bundle := spec.Bundle{}
	bundle.ApplyDefaults()
	record := spec.MachineRecord{
		HostID:      "xpu-auto2",
		Hostname:    "xpu-auto2",
		MgmtIP:      "10.101.9.16",
		MgmtPrefix:  "26",
		MgmtGateway: "10.101.9.1",
		RDMA: []spec.RDMARecord{
			{IP: "11.1.1.16"},
			{IP: "11.1.2.16"},
			{IP: "11.1.3.16"},
			{IP: "11.1.4.16"},
		},
	}

	app, err := New(bundle, []spec.MachineRecord{record}, "xpu-auto2", root, true, map[string]bool{"all": true}, ioDiscard{})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	if got := strings.Join(app.Machine.MgmtIfaces, ","); got != "eno-a,eno-b" {
		t.Fatalf("unexpected auto mgmt ifaces: %s", got)
	}
	if got := app.managementSummaryName(); got != "bond0" {
		t.Fatalf("expected bond0 summary, got %s", got)
	}
}

func TestDiscoverNetDevicesIncludesSpeedAndModel(t *testing.T) {
	root := t.TempDir()
	mustWriteNetDevice(t, root, "eno1", "aa:bb:cc:dd:ee:01", "0000:20:00.0", "ixgbe", 0, "p0")

	app := &App{Root: root}
	devices, err := app.discoverNetDevices()
	if err != nil {
		t.Fatalf("discover net devices: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("expected one device, got %d", len(devices))
	}
	device := devices[0]
	if device.SpeedMbps != 100000 {
		t.Fatalf("expected speed 100000, got %d", device.SpeedMbps)
	}
	if got := deviceSpeedLabel(device); got != "100G" {
		t.Fatalf("expected 100G speed label, got %s", got)
	}
	if got := deviceModelLabel(device); got != "0x15b3:0x101d" {
		t.Fatalf("expected model label, got %s", got)
	}
}

func TestNICBindingReviewShowsPlannedIPAndSpaceBinding(t *testing.T) {
	var output strings.Builder
	review := newNICBindingReview([]interfaceBinding{
		{
			Kind:        "mgmt",
			Name:        "ens20f0np0",
			CurrentName: "eno1",
			MAC:         "aa:bb:cc:dd:ee:01",
			Address:     "10.101.9.11/26",
			Gateway:     "10.101.9.1",
		},
	}, []netDevice{
		{
			Name:      "eno1",
			MAC:       "aa:bb:cc:dd:ee:01",
			PCI:       "0000:20:00.0",
			Driver:    "ixgbe",
			SpeedMbps: 25000,
			VendorID:  "0x8086",
			DeviceID:  "0x1572",
		},
	})

	renderNICBindingReview(&output, review)
	got := output.String()
	for _, want := range []string{
		"IP",
		"apply:",
		"10.101.9.11/26",
		"ens20f0np0 10.101.9.11/26",
		"Space/n choose NIC",
		"t swap target",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in review output:\n%s", want, got)
		}
	}
	assertRenderedLinesFit(t, got, 100)
}

func TestNICBindingReviewSpaceOpensNICOptions(t *testing.T) {
	review := newNICBindingReview([]interfaceBinding{
		{
			Kind:    "rdma",
			Name:    "ens11np0",
			Address: "11.1.1.11/24",
		},
	}, []netDevice{
		{Name: "enp0s1", MAC: "16:b3:1a:7c:a3:a0", SpeedMbps: 1000, Driver: "e1000"},
		{Name: "enp0s2", MAC: "6a:69:38:e0:53:70", SpeedMbps: 1000, Driver: "e1000"},
	})
	model := newNICBindingModel(review)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeySpace})
	updatedModel := updated.(nicBindingModel)
	if !updatedModel.review.DropdownOpen || updatedModel.review.DropdownMode != "nic" {
		t.Fatalf("expected Space to open NIC dropdown, got open=%v mode=%q", updatedModel.review.DropdownOpen, updatedModel.review.DropdownMode)
	}

	var output strings.Builder
	renderNICBindingReview(&output, updatedModel.review)
	got := output.String()
	for _, want := range []string{"NIC Options", "Current NIC", "enp0s1", "enp0s2"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in NIC dropdown:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Target Plan Options") {
		t.Fatalf("did not expect target plan dropdown for Space:\n%s", got)
	}
}

func TestNICBindingReviewSpaceConfirmsNICOption(t *testing.T) {
	review := newNICBindingReview([]interfaceBinding{
		{
			Kind:    "rdma",
			Name:    "ens11np0",
			Address: "11.1.1.11/24",
		},
	}, []netDevice{
		{Name: "enp0s1", MAC: "16:b3:1a:7c:a3:a0", SpeedMbps: 1000, Driver: "e1000"},
		{Name: "enp0s2", MAC: "6a:69:38:e0:53:70", SpeedMbps: 1000, Driver: "e1000"},
	})
	review.openNICDropdown()
	review.DropdownIndex = 1

	model := newNICBindingModel(review)
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeySpace})
	updatedModel := updated.(nicBindingModel)
	if updatedModel.review.DropdownOpen {
		t.Fatal("expected Space to confirm and close NIC dropdown")
	}
	binding := updatedModel.review.Bindings[0]
	if binding.CurrentName != "enp0s2" || binding.MAC != "6a:69:38:e0:53:70" {
		t.Fatalf("expected Space to bind enp0s2, got %#v", binding)
	}
}

func TestNICBindingReviewAbortKeysAlwaysQuit(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'q'}},
		{Type: tea.KeyRunes, Runes: []rune{'Q'}},
		{Type: tea.KeyCtrlC},
	} {
		review := newNICBindingReview([]interfaceBinding{
			{Kind: "rdma", Name: "ens11np0", Address: "11.1.1.11/24"},
		}, []netDevice{
			{Name: "enp0s1", MAC: "16:b3:1a:7c:a3:a0"},
		})
		review.openNICDropdown()
		model := newNICBindingModel(review)
		updated, cmd := model.Update(key)
		updatedModel := updated.(nicBindingModel)
		if !updatedModel.aborted {
			t.Fatalf("expected key %q to abort", key.String())
		}
		if cmd == nil {
			t.Fatalf("expected key %q to return quit command", key.String())
		}
	}
}

func TestNICBindingReviewNICDropdownSwapsPhysicalNICs(t *testing.T) {
	review := newNICBindingReview([]interfaceBinding{
		{
			Kind:        "mgmt",
			Name:        "ens20f0np0",
			CurrentName: "eno1",
			MAC:         "aa:bb:cc:dd:ee:01",
			Address:     "10.101.9.11/26",
		},
		{
			Kind:        "mgmt",
			Name:        "ens20f1np1",
			CurrentName: "eno2",
			MAC:         "aa:bb:cc:dd:ee:02",
			Address:     "10.101.9.11/26",
		},
	}, []netDevice{
		{Name: "eno1", MAC: "aa:bb:cc:dd:ee:01"},
		{Name: "eno2", MAC: "aa:bb:cc:dd:ee:02"},
		{Name: "eno3", MAC: "aa:bb:cc:dd:ee:03"},
	})

	review.Selected = 0
	review.openNICDropdown()
	review.DropdownIndex = 2
	review.applyNICDropdown()

	if got := review.Bindings[0].CurrentName; got != "eno3" {
		t.Fatalf("expected selected row NIC to become eno3, got %s", got)
	}
	if got := review.Bindings[0].MAC; got != "aa:bb:cc:dd:ee:03" {
		t.Fatalf("expected selected row MAC to become eno3 MAC, got %s", got)
	}
	if err := validateReviewBindings(review.Bindings); err != nil {
		t.Fatalf("expected bindings to remain valid: %v", err)
	}
}

func TestNICBindingReviewDropdownSwapsTargetPlans(t *testing.T) {
	review := newNICBindingReview([]interfaceBinding{
		{
			Kind:        "mgmt",
			Name:        "ens20f0np0",
			CurrentName: "eno1",
			MAC:         "aa:bb:cc:dd:ee:01",
			Address:     "10.101.9.11/26",
		},
		{
			Kind:        "rdma",
			Name:        "ens11np0",
			CurrentName: "rdma0",
			MAC:         "aa:bb:cc:dd:ee:11",
			Address:     "11.1.1.11/24",
			Table:       101,
		},
	}, nil)

	review.Selected = 0
	review.openTargetDropdown()
	review.DropdownIndex = 1
	review.applyTargetDropdown()

	if got := review.Bindings[0].Name; got != "ens11np0" {
		t.Fatalf("expected selected row target to become ens11np0, got %s", got)
	}
	if got := review.Bindings[0].CurrentName; got != "eno1" {
		t.Fatalf("expected selected row physical NIC to stay eno1, got %s", got)
	}
	if got := review.Bindings[0].Address; got != "11.1.1.11/24" {
		t.Fatalf("expected selected row planned IP to follow target, got %s", got)
	}
	if got := review.Bindings[1].Name; got != "ens20f0np0" {
		t.Fatalf("expected displaced row target to become ens20f0np0, got %s", got)
	}
	if got := review.Bindings[1].CurrentName; got != "rdma0" {
		t.Fatalf("expected displaced row physical NIC to stay rdma0, got %s", got)
	}
	if err := validateReviewBindings(review.Bindings); err != nil {
		t.Fatalf("expected swapped bindings to remain valid: %v", err)
	}
}

func TestNICBindingReviewAllowsForcedManualRoleAssignment(t *testing.T) {
	review := newNICBindingReview([]interfaceBinding{
		{Kind: "mgmt", Name: "ens20f0np0", CurrentName: "enp0s3", MAC: "f6:09:a2:3e:bf:da", Address: "10.101.9.11/24"},
		{Kind: "mgmt", Name: "ens20f1np1", CurrentName: "enp0s4", MAC: "02:7a:da:03:d1:f0", Address: "10.101.9.11/24"},
		{Kind: "rdma", Name: "ens11np0", Address: "11.1.1.11/24", NeedsReview: true},
		{Kind: "rdma", Name: "ens13np0", Address: "11.1.2.11/24", NeedsReview: true},
		{Kind: "rdma", Name: "ens15np0", Address: "11.1.3.11/24", NeedsReview: true},
		{Kind: "rdma", Name: "ens17np0", Address: "11.1.4.11/24", NeedsReview: true},
	}, []netDevice{
		{Name: "enp0s1", MAC: "16:b3:1a:7c:a3:a0", SpeedMbps: 1000, Driver: "e1000", VendorID: "0x8086", DeviceID: "0x100e"},
		{Name: "enp0s2", MAC: "6a:69:38:e0:53:70", SpeedMbps: 1000, Driver: "e1000", VendorID: "0x8086", DeviceID: "0x100e"},
		{Name: "enp0s3", MAC: "f6:09:a2:3e:bf:da", SpeedMbps: 100, Driver: "e100", VendorID: "0x8086", DeviceID: "0x2449"},
		{Name: "enp0s4", MAC: "02:7a:da:03:d1:f0", SpeedMbps: 100, Driver: "e100", VendorID: "0x8086", DeviceID: "0x2449"},
		{Name: "enp0s5", MAC: "ce:bf:6f:e3:14:0c", SpeedMbps: 100, Driver: "e100", VendorID: "0x8086", DeviceID: "0x2449"},
		{Name: "enp0s6", MAC: "22:39:14:87:ed:6d", SpeedMbps: 100, Driver: "e100", VendorID: "0x8086", DeviceID: "0x2449"},
	})

	for row, nicNumber := range []string{"1", "2", "3", "4", "5", "6"} {
		review.Selected = row
		review.applyNICByNumber(nicNumber)
	}
	if err := validateReviewBindings(review.Bindings); err != nil {
		t.Fatalf("expected forced manual assignment to validate: %v", err)
	}
	got := map[string]string{}
	for _, binding := range review.Bindings {
		got[binding.Name] = binding.CurrentName
	}
	want := map[string]string{
		"ens20f0np0": "enp0s1",
		"ens20f1np1": "enp0s2",
		"ens11np0":   "enp0s3",
		"ens13np0":   "enp0s4",
		"ens15np0":   "enp0s5",
		"ens17np0":   "enp0s6",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected forced assignment: got=%v want=%v", got, want)
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
			MgmtIP:     "172.16.18.11",
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
			MgmtIP:     "172.16.18.11",
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
		"/etc/udev/rules.d/70-persistent-net.rules",
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

func TestRequiredPackagesUseYumKernelDevel(t *testing.T) {
	app := &App{
		Bundle: spec.Bundle{
			Platform: spec.PlatformConfig{
				PackageManager: "yum",
			},
			Packages: []string{"gcc", "make"},
		},
	}
	got, err := app.requiredPackagesWithUname("3.10.0-1160.el7.x86_64")
	if err != nil {
		t.Fatalf("required packages: %v", err)
	}
	want := []string{
		"gcc",
		"make",
		"kernel-devel-3.10.0-1160.el7.x86_64",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected packages: got=%v want=%v", got, want)
	}
}

func TestDescribeYumAndNetworkManagerActions(t *testing.T) {
	binDir := t.TempDir()
	uname := filepath.Join(binDir, "uname")
	if err := os.WriteFile(uname, []byte("#!/bin/sh\nprintf '3.10.0-1160.el7.x86_64\\n'\n"), 0o755); err != nil {
		t.Fatalf("write uname stub: %v", err)
	}
	t.Setenv("PATH", binDir)

	bundle := spec.Bundle{
		Platform: spec.PlatformConfig{
			OSFamily:       "centos",
			PackageManager: "yum",
			NetworkBackend: "networkmanager",
		},
		Defaults: spec.Defaults{
			BackupExistingNetwork: true,
		},
		OfflineRepo: spec.OfflineAPTConfig{
			Enabled:    true,
			CopyTo:     "/opt/rpm-repo",
			TargetFile: "/etc/yum.repos.d/kunlun-offline.repo",
			Entries: []string{
				"[kunlun-offline]",
				"name=Kunlun Offline",
				"baseurl=file://{{offline_repo_target}}",
				"enabled=1",
				"gpgcheck=0",
			},
		},
		Packages: []string{"gcc", "make"},
	}
	bundle.ApplyDefaults()
	app := &App{
		Bundle: bundle,
		Machine: spec.MachineConfig{
			HostID:        "node01",
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
			"apt":     true,
			"network": true,
		},
		Output: ioDiscard{},
	}
	got, err := app.Describe()
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	for _, want := range []string{
		"write /etc/yum.repos.d/kunlun-offline.repo with offline yum repo entries",
		"run yum makecache",
		"run yum install -y gcc make kernel-devel-3.10.0-1160.el7.x86_64",
		"disable and stop network service when present: systemctl disable --now network",
		"enable and start NetworkManager: systemctl enable --now NetworkManager",
		"write /etc/sysconfig/network-scripts/ifcfg-bond0 and member ifcfg files",
		"write /etc/sysconfig/network-scripts/route-enp10s0 and /etc/sysconfig/network-scripts/rule-enp10s0",
		"write /etc/NetworkManager/dispatcher.d/90-kunlun-rdma-routes",
		"run nmcli connection reload",
		"run nmcli connection up bond0",
		"run bash /usr/local/sbin/kunlun-config_rt_enp10s0.sh",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in describe output, got:\n%s", want, got)
		}
	}
}

func TestAutoNetworkBackendPrefersLegacyNetworkService(t *testing.T) {
	binDir := t.TempDir()
	systemctl := filepath.Join(binDir, "systemctl")
	if err := os.WriteFile(systemctl, []byte("#!/bin/sh\n[ \"$3\" = network ]\n"), 0o755); err != nil {
		t.Fatalf("write systemctl stub: %v", err)
	}
	t.Setenv("PATH", binDir)

	app := &App{
		Bundle: spec.Bundle{
			Platform: spec.PlatformConfig{
				NetworkBackend: "auto",
			},
		},
	}
	if got := app.networkBackend(); got != "network" {
		t.Fatalf("unexpected backend: %s", got)
	}
}

func TestAutoNetworkBackendUsesNetworkManagerWhenActive(t *testing.T) {
	binDir := t.TempDir()
	systemctl := filepath.Join(binDir, "systemctl")
	if err := os.WriteFile(systemctl, []byte("#!/bin/sh\n[ \"$3\" = NetworkManager ]\n"), 0o755); err != nil {
		t.Fatalf("write systemctl stub: %v", err)
	}
	t.Setenv("PATH", binDir)

	app := &App{
		Bundle: spec.Bundle{
			Platform: spec.PlatformConfig{
				NetworkBackend: "auto",
			},
		},
	}
	if got := app.networkBackend(); got != "networkmanager" {
		t.Fatalf("unexpected backend: %s", got)
	}
}

func TestDescribeLegacyNetworkActions(t *testing.T) {
	app := &App{
		Bundle: spec.Bundle{
			Platform: spec.PlatformConfig{
				NetworkBackend: "network",
			},
		},
		Machine: spec.MachineConfig{
			HostID:        "node01",
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
		},
		Output: ioDiscard{},
	}
	got, err := app.Describe()
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	for _, want := range []string{
		"disable and stop NetworkManager when present: systemctl disable --now NetworkManager",
		"enable and start network service: systemctl enable --now network",
		"NM_CONTROLLED=no",
		"write /etc/sysconfig/network-scripts/route-enp10s0 and /etc/sysconfig/network-scripts/rule-enp10s0",
		"run ifup bond0",
		"run ifup enp10s0",
		"run bash /usr/local/sbin/kunlun-config_rt_enp10s0.sh",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in describe output, got:\n%s", want, got)
		}
	}
}

func TestExplicitNetworkBackendSwitchesServices(t *testing.T) {
	var output strings.Builder
	app := &App{
		DryRun: true,
		Bundle: spec.Bundle{
			Platform: spec.PlatformConfig{
				NetworkBackend: "network",
			},
		},
		Output: &output,
	}
	if err := app.ensureExplicitNetworkBackendService(); err != nil {
		t.Fatalf("ensure backend service: %v", err)
	}
	got := output.String()
	first := strings.Index(got, "run (allow failure): systemctl disable --now NetworkManager")
	second := strings.Index(got, "run: systemctl enable --now network")
	if first == -1 || second == -1 {
		t.Fatalf("missing service switch commands:\n%s", got)
	}
	if first > second {
		t.Fatalf("expected disable NetworkManager before enabling network:\n%s", got)
	}
}

func TestDescribeNetworkHonorsManagementAndImmediateApplySwitches(t *testing.T) {
	disabled := false
	app := &App{
		Bundle: spec.Bundle{
			Defaults: spec.Defaults{
				ConfigureManagementNetwork: &disabled,
				ApplyNetworkImmediately:    &disabled,
			},
		},
		Machine: spec.MachineConfig{
			HostID:        "node01",
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
		},
		Output: ioDiscard{},
	}
	got, err := app.Describe()
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	for _, want := range []string{
		"skip management network configuration because mgmt_ip is empty or configure_management_network=false",
		"write /etc/netplan/10-kunlun-enp10s0.yaml",
		"skip immediate netplan apply because apply_network_immediately=false",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in describe output, got:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{
		"write /etc/netplan/00-kunlun-bond.yaml",
		"run netplan apply",
		"run bash /etc/networkd-dispatcher/routable.d/config_rt_enp10s0.sh",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("did not expect %q in describe output, got:\n%s", unwanted, got)
		}
	}
}

func TestDescribeNetworkSkipsManagementWhenMgmtIPIsBlank(t *testing.T) {
	app := &App{
		Bundle: spec.Bundle{
			Defaults: spec.Defaults{},
		},
		Machine: spec.MachineConfig{
			HostID:        "node01",
			MgmtBondName:  "bond0",
			MgmtPrefix:    24,
			MgmtMTU:       1500,
			RDMAMTU:       9000,
			RouteCIDR:     "11.1.0.0/21",
			RoutePriority: 32761,
			RDMA: []spec.RDMAConfig{
				{Name: "enp10s0", IP: "11.1.1.11", Prefix: 24, Gateway: "11.1.1.1", Table: 101},
			},
		},
		Stages: map[string]bool{
			"network": true,
		},
		Output: ioDiscard{},
	}
	got, err := app.Describe()
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	for _, want := range []string{
		"Management network: skipped (mgmt_ip is empty or configure_management_network=false)",
		"skip management network configuration because mgmt_ip is empty or configure_management_network=false",
		"write /etc/netplan/10-kunlun-enp10s0.yaml",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in describe output, got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "00-kunlun-bond.yaml") {
		t.Fatalf("did not expect management netplan file in describe output, got:\n%s", got)
	}
}

func TestNetworkBeforeUdevRenamesAndAppliesBeforePersistentRules(t *testing.T) {
	root := t.TempDir()
	mustWriteNetDeviceWithSpeed(t, root, "eno1", "aa:bb:cc:dd:ee:01", "0000:20:00.0", "ixgbe", 0, "p0", 25000)
	mustWriteNetDeviceWithSpeed(t, root, "rdma0", "aa:bb:cc:dd:ee:11", "0000:40:00.0", "mlx5_core", 0, "p0", 400000)

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
			MgmtBondName:  "bond0",
			MgmtIP:        "10.101.9.11",
			MgmtPrefix:    24,
			MgmtGateway:   "10.101.9.1",
			MgmtIfaces:    []string{"ens20f0np0"},
			MgmtMTU:       1500,
			RDMAMTU:       9000,
			RouteCIDR:     "11.1.0.0/21",
			RoutePriority: 32761,
			RDMA: []spec.RDMAConfig{
				{Name: "ens11np0", IP: "11.1.1.11", Prefix: 24, Gateway: "11.1.1.1", Table: 101},
			},
		},
		Stages: map[string]bool{
			"network": true,
			"udev":    true,
		},
		Output: &output,
	}

	if err := app.runNetworkStage(); err != nil {
		t.Fatalf("run network stage: %v", err)
	}
	if app.networkApplyDeferred {
		t.Fatal("did not expect network apply to be deferred to udev")
	}
	if !app.interfaceBindingsConfirmed {
		t.Fatal("expected NIC bindings to be confirmed during network stage")
	}
	if got := len(app.confirmedInterfaceBindings); got != 2 {
		t.Fatalf("expected network stage to confirm two NIC bindings, got %d", got)
	}
	if err := app.runUdevStage(); err != nil {
		t.Fatalf("run udev stage: %v", err)
	}
	got := output.String()
	renameIdx := strings.Index(got, "temporarily renamed target interface names before applying network settings")
	applyIdx := strings.Index(got, "run: netplan apply")
	udevIdx := strings.Index(got, "run: udevadm control --reload-rules")
	if renameIdx == -1 || applyIdx == -1 || udevIdx == -1 {
		t.Fatalf("expected network rename/apply before udev reload, got:\n%s", got)
	}
	if !(renameIdx < applyIdx && applyIdx < udevIdx) {
		t.Fatalf("expected network rename/apply before udev reload, got:\n%s", got)
	}
}

func TestNetworkStageAutoEnablesUdevWhenPlannedInterfacesAreMissing(t *testing.T) {
	root := t.TempDir()
	mustWriteNetDevice(t, root, "eno1", "aa:bb:cc:dd:ee:01", "0000:20:00.0", "ixgbe", 0, "p0")

	var output strings.Builder
	app := &App{
		Root:   root,
		DryRun: true,
		Bundle: spec.Bundle{
			Defaults: spec.Defaults{
				RDMAExsist: boolPtr(false),
			},
		},
		Machine: spec.MachineConfig{
			HostID:       "test1",
			MgmtBondName: "bond0",
			MgmtIP:       "10.101.9.11",
			MgmtPrefix:   24,
			MgmtGateway:  "10.101.9.1",
			MgmtIfaces:   []string{"ens20f0np0"},
			MgmtMTU:      1500,
		},
		Stages: map[string]bool{
			"network": true,
		},
		Output: &output,
	}

	if !app.stageEnabled("udev") {
		t.Fatal("expected udev to be auto-enabled when network target interfaces are missing")
	}
	if got, want := app.selectedStages(), []string{"network", "udev"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected selected stages: got=%v want=%v", got, want)
	}
	if got := strings.Join(app.plannedFiles(), "\n"); !strings.Contains(got, udevFile) {
		t.Fatalf("expected planned files to include auto-enabled udev file, got:\n%s", got)
	}

	if err := app.Apply(); err != nil {
		t.Fatalf("apply dry-run: %v", err)
	}
	if !app.interfaceBindingsConfirmed {
		t.Fatal("expected NIC bindings to be confirmed during auto-enabled network stage")
	}
	got := output.String()
	for _, want := range []string{
		"==> stage: network",
		"temporarily renamed target interface names before applying network settings",
		"run: netplan apply",
		"==> stage: udev",
		"run: udevadm control --reload-rules",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in output:\n%s", want, got)
		}
	}
	if networkIdx, udevIdx := strings.Index(got, "==> stage: network"), strings.Index(got, "==> stage: udev"); networkIdx == -1 || udevIdx == -1 || networkIdx > udevIdx {
		t.Fatalf("expected network to run before auto-enabled udev, got:\n%s", got)
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
	if !strings.Contains(got, "run: udevadm control --reload-rules") {
		t.Fatalf("expected udev reload in output:\n%s", got)
	}
	if strings.Contains(got, "ip link set") {
		t.Fatalf("did not expect udev stage to temporarily rename interfaces; network stage owns temporary rename now:\n%s", got)
	}
}

func TestUdevStageDiscoversManagementByPCIOrderAndRenamesTemporarily(t *testing.T) {
	root := t.TempDir()
	mustWriteNetDeviceWithSpeed(t, root, "eno-b", "aa:bb:cc:dd:ee:22", "0000:30:00.0", "ixgbe", 0, "p0", 25000)
	mustWriteNetDeviceWithSpeed(t, root, "eno-a", "aa:bb:cc:dd:ee:11", "0000:20:00.0", "ixgbe", 0, "p0", 25000)
	mustWriteNetDeviceWithSpeed(t, root, "rdma0", "aa:bb:cc:dd:ee:33", "0000:40:00.0", "mlx5_core", 0, "p0", 400000)

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
			MgmtIfaces: []string{"ens20f0np0", "ens20f1np1"},
			RDMA: []spec.RDMAConfig{
				{Name: "ens11np0"},
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
		`ATTR{address}=="aa:bb:cc:dd:ee:11", ATTR{type}=="1", NAME="ens20f0np0"`,
		`ATTR{address}=="aa:bb:cc:dd:ee:22", ATTR{type}=="1", NAME="ens20f1np1"`,
		`ATTR{address}=="aa:bb:cc:dd:ee:33", ATTR{type}=="1", NAME="ens11np0"`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected %q in udev rules:\n%s", want, content)
		}
	}

	if err := app.runUdevStage(); err != nil {
		t.Fatalf("run udev stage: %v", err)
	}
	got := output.String()
	if !strings.Contains(got, "run: udevadm control --reload-rules") {
		t.Fatalf("expected udev reload in output:\n%s", got)
	}
	if strings.Contains(got, "ip link set") {
		t.Fatalf("did not expect udev stage to temporarily rename interfaces; network stage owns temporary rename now:\n%s", got)
	}
}

func TestUdevStageFallsBackToManualManagementReview(t *testing.T) {
	root := t.TempDir()
	mustWriteNetDevice(t, root, "eno-a", "aa:bb:cc:dd:ee:11", "0000:20:00.0", "ixgbe", 0, "p0")
	mustWriteNetDevice(t, root, "eno-b", "aa:bb:cc:dd:ee:22", "0000:30:00.0", "ixgbe", 0, "p0")
	mustWriteNetDevice(t, root, "eno-c", "aa:bb:cc:dd:ee:33", "0000:40:00.0", "ixgbe", 0, "p0")

	app := &App{
		Root: root,
		Machine: spec.MachineConfig{
			MgmtIfaces: []string{"ens20f0np0", "ens20f1np1"},
		},
	}

	bindings, err := app.interfaceBindings()
	if err != nil {
		t.Fatalf("interface bindings: %v", err)
	}
	if len(bindings) != 2 {
		t.Fatalf("expected two management bindings, got %d", len(bindings))
	}
	for _, binding := range bindings {
		if !binding.NeedsReview {
			t.Fatalf("expected binding to require manual review: %#v", binding)
		}
	}
	if bindings[0].CurrentName != "" || bindings[0].MAC != "" || bindings[1].CurrentName != "" || bindings[1].MAC != "" {
		t.Fatalf("expected manual fallback to avoid automatic NIC choices, got %#v", bindings)
	}
}

func TestManualNICReviewDryRunSuggestsMACMatching(t *testing.T) {
	root := t.TempDir()
	mustWriteNetDevice(t, root, "eno-a", "aa:bb:cc:dd:ee:11", "0000:20:00.0", "ixgbe", 0, "p0")
	mustWriteNetDevice(t, root, "eno-b", "aa:bb:cc:dd:ee:22", "0000:30:00.0", "ixgbe", 0, "p0")
	mustWriteNetDevice(t, root, "eno-c", "aa:bb:cc:dd:ee:33", "0000:40:00.0", "ixgbe", 0, "p0")

	app := &App{
		Root:   root,
		DryRun: true,
		Machine: spec.MachineConfig{
			MgmtIfaces: []string{"ens20f0np0", "ens20f1np1"},
		},
		Output: ioDiscard{},
	}

	_, err := app.confirmedNICBindings()
	if err == nil {
		t.Fatal("expected dry-run manual review to fail with guidance")
	}
	for _, want := range []string{"manual NIC binding review is required", "mgmt*_mac/rdma*_mac"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected %q in error, got %v", want, err)
		}
	}
}

func TestManualNICReviewFailsBeforeWritingWhenNoSelectableNICs(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sys", "class", "net"), 0o755); err != nil {
		t.Fatalf("mkdir net dir: %v", err)
	}
	app := &App{
		Root: root,
		Machine: spec.MachineConfig{
			MgmtIfaces: []string{"ens20f0np0"},
		},
		Output: ioDiscard{},
	}

	bindings := []interfaceBinding{{
		Kind:        "mgmt",
		Name:        "ens20f0np0",
		Address:     "10.101.9.11/24",
		NeedsReview: true,
	}}
	_, err := app.confirmInterfaceBindings(bindings)
	if err == nil {
		t.Fatal("expected no selectable NICs to fail")
	}
	if !strings.Contains(err.Error(), "no selectable NICs") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestManualMgmtReviewDoesNotLogRDMATwice(t *testing.T) {
	root := t.TempDir()
	mustWriteNetDevice(t, root, "eno-a", "aa:bb:cc:dd:ee:11", "0000:20:00.0", "ixgbe", 0, "p0")
	mustWriteNetDevice(t, root, "eno-b", "aa:bb:cc:dd:ee:22", "0000:30:00.0", "ixgbe", 0, "p0")
	mustWriteNetDevice(t, root, "eno-c", "aa:bb:cc:dd:ee:33", "0000:40:00.0", "ixgbe", 0, "p0")

	var output strings.Builder
	app := &App{
		Root: root,
		Bundle: spec.Bundle{
			Defaults: spec.Defaults{RDMAExsist: boolPtr(true)},
		},
		Machine: spec.MachineConfig{
			MgmtIfaces: []string{"ens20f0np0", "ens20f1np1"},
			RDMA: []spec.RDMAConfig{
				{Name: "ens11np0"},
				{Name: "ens13np0"},
			},
		},
		Output: &output,
	}

	if _, err := app.interfaceBindings(); err != nil {
		t.Fatalf("interface bindings: %v", err)
	}
	if count := strings.Count(output.String(), "RDMA auto discovery matched"); count != 1 {
		t.Fatalf("expected one RDMA manual review log, got %d:\n%s", count, output.String())
	}
}

func TestUdevStageRejectsIncompleteManualBindings(t *testing.T) {
	var output strings.Builder
	app := &App{
		DryRun: true,
		confirmedInterfaceBindings: []interfaceBinding{
			{
				Kind:        "mgmt",
				Name:        "ens20f0np0",
				Address:     "10.101.9.11/24",
				NeedsReview: true,
			},
		},
		interfaceBindingsConfirmed: true,
		Output:                     &output,
	}

	err := app.runUdevStage()
	if err == nil {
		t.Fatal("expected incomplete bindings to fail")
	}
	for _, want := range []string{"complete bindings", "mgmt*_mac/rdma*_mac"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected %q in error, got %v", want, err)
		}
	}
	if strings.Contains(output.String(), "write /etc/udev/rules.d/70-persistent-net.rules") {
		t.Fatalf("did not expect udev rules to be written with incomplete bindings, got:\n%s", output.String())
	}
}

func TestRDMADiscoveryFallsBackToManualReviewWithoutAutomaticChoices(t *testing.T) {
	root := t.TempDir()
	mustWriteNetDevice(t, root, "rdma-a", "aa:bb:cc:dd:ee:11", "0000:20:00.0", "mlx5_core", 0, "p0")

	app := &App{
		Root: root,
		Bundle: spec.Bundle{
			Defaults: spec.Defaults{RDMAExsist: boolPtr(true)},
		},
		Machine: spec.MachineConfig{
			RDMA: []spec.RDMAConfig{
				{Name: "ens11np0", IP: "11.1.1.11", Prefix: 24, Gateway: "11.1.1.1", Table: 101},
				{Name: "ens13np0", IP: "11.1.2.11", Prefix: 24, Gateway: "11.1.2.1", Table: 102},
			},
		},
		Output: ioDiscard{},
	}

	bindings, err := app.interfaceBindings()
	if err != nil {
		t.Fatalf("interface bindings: %v", err)
	}
	if len(bindings) != 2 {
		t.Fatalf("expected two RDMA bindings, got %d", len(bindings))
	}
	for _, binding := range bindings {
		if !binding.NeedsReview || binding.MAC != "" || binding.CurrentName != "" {
			t.Fatalf("expected manual RDMA binding without automatic choice, got %#v", binding)
		}
	}
}

func TestMgmtDiscoveryFallsBackToExactModelGroupWhenSpeedAmbiguous(t *testing.T) {
	root := t.TempDir()
	mustWriteNetDeviceWithModelAndSpeed(t, root, "eno-a1", "aa:bb:cc:dd:aa:01", "0000:20:00.0", "ixgbe", 0, "p0", 10000, "0x8086", "0x100e")
	mustWriteNetDeviceWithModelAndSpeed(t, root, "eno-a2", "aa:bb:cc:dd:aa:02", "0000:21:00.0", "ixgbe", 1, "p1", 10000, "0x8086", "0x100e")
	mustWriteNetDeviceWithModelAndSpeed(t, root, "eno-b1", "aa:bb:cc:dd:bb:01", "0000:30:00.0", "ixgbe", 0, "p0", 10000, "0x8086", "0x10fb")
	mustWriteNetDeviceWithModelAndSpeed(t, root, "eno-b2", "aa:bb:cc:dd:bb:02", "0000:31:00.0", "ixgbe", 1, "p1", 10000, "0x8086", "0x10fb")
	mustWriteNetDeviceWithModelAndSpeed(t, root, "eno-b3", "aa:bb:cc:dd:bb:03", "0000:32:00.0", "ixgbe", 2, "p2", 10000, "0x8086", "0x10fb")
	mustWriteNetDeviceWithModelAndSpeed(t, root, "eno-b4", "aa:bb:cc:dd:bb:04", "0000:33:00.0", "ixgbe", 3, "p3", 10000, "0x8086", "0x10fb")

	app := &App{
		Root: root,
		Machine: spec.MachineConfig{
			MgmtIfaces: []string{"ens20f0np0", "ens20f1np1"},
		},
	}
	devices, err := app.discoverMgmtDevices()
	if err != nil {
		t.Fatalf("discover mgmt devices: %v", err)
	}
	if got := netDeviceNames(devices); !reflect.DeepEqual(got, []string{"eno-a1", "eno-a2"}) {
		t.Fatalf("expected exact two-port model group, got %v from %#v", got, devices)
	}
}

func TestRDMADiscoveryFallsBackToExactModelGroupWhenSpeedAmbiguous(t *testing.T) {
	root := t.TempDir()
	mustWriteNetDeviceWithModelAndSpeed(t, root, "eno-a1", "aa:bb:cc:dd:aa:01", "0000:20:00.0", "ixgbe", 0, "p0", 10000, "0x8086", "0x100e")
	mustWriteNetDeviceWithModelAndSpeed(t, root, "eno-a2", "aa:bb:cc:dd:aa:02", "0000:21:00.0", "ixgbe", 1, "p1", 10000, "0x8086", "0x100e")
	mustWriteNetDeviceWithModelAndSpeed(t, root, "eno-b1", "aa:bb:cc:dd:bb:01", "0000:30:00.0", "ixgbe", 0, "p0", 10000, "0x8086", "0x10fb")
	mustWriteNetDeviceWithModelAndSpeed(t, root, "eno-b2", "aa:bb:cc:dd:bb:02", "0000:31:00.0", "ixgbe", 1, "p1", 10000, "0x8086", "0x10fb")
	mustWriteNetDeviceWithModelAndSpeed(t, root, "eno-b3", "aa:bb:cc:dd:bb:03", "0000:32:00.0", "ixgbe", 2, "p2", 10000, "0x8086", "0x10fb")
	mustWriteNetDeviceWithModelAndSpeed(t, root, "eno-b4", "aa:bb:cc:dd:bb:04", "0000:33:00.0", "ixgbe", 3, "p3", 10000, "0x8086", "0x10fb")

	app := &App{
		Root: root,
		Bundle: spec.Bundle{
			Defaults: spec.Defaults{RDMAExsist: boolPtr(true)},
		},
		Machine: spec.MachineConfig{
			MgmtMACs: []string{"aa:bb:cc:dd:aa:01", "aa:bb:cc:dd:aa:02"},
			RDMA: []spec.RDMAConfig{
				{Name: "ens11np0"},
				{Name: "ens13np0"},
				{Name: "ens15np0"},
				{Name: "ens17np0"},
			},
		},
	}
	devices, err := app.discoverRDMADevices()
	if err != nil {
		t.Fatalf("discover rdma devices: %v", err)
	}
	if got := netDeviceNames(devices); !reflect.DeepEqual(got, []string{"eno-b1", "eno-b2", "eno-b3", "eno-b4"}) {
		t.Fatalf("expected exact four-port model group, got %v from %#v", got, devices)
	}
}

func TestMgmtDiscoveryPrefersLinkedGroupBeforeLowestSpeed(t *testing.T) {
	root := t.TempDir()
	mustWriteNetDeviceWithSpeed(t, root, "mgmt25", "aa:bb:cc:dd:ee:25", "0000:20:00.0", "mlx5_core", 0, "p0", 25000)
	mustWriteNetDeviceWithSpeed(t, root, "mgmt100", "aa:bb:cc:dd:ee:10", "0000:30:00.0", "mlx5_core", 0, "p0", 100000)
	mustWriteNetDeviceWithSpeed(t, root, "mgmt200", "aa:bb:cc:dd:ee:20", "0000:40:00.0", "mlx5_core", 0, "p0", 200000)
	mustWriteLinkState(t, root, "mgmt25", "0", "down")
	mustWriteLinkState(t, root, "mgmt100", "1", "up")
	mustWriteLinkState(t, root, "mgmt200", "0", "down")

	app := &App{
		Root: root,
		Machine: spec.MachineConfig{
			MgmtIfaces: []string{"ens20f0np0"},
		},
	}
	devices, err := app.discoverMgmtDevices()
	if err != nil {
		t.Fatalf("discover mgmt devices: %v", err)
	}
	if len(devices) != 1 || devices[0].Name != "mgmt100" {
		t.Fatalf("expected linked 100G mgmt group, got %#v", devices)
	}
}

func TestMgmtDiscoveryUsesLowestSpeedWhenLinkStateDoesNotDistinguish(t *testing.T) {
	root := t.TempDir()
	mustWriteNetDeviceWithSpeed(t, root, "mgmt25", "aa:bb:cc:dd:ee:25", "0000:20:00.0", "mlx5_core", 0, "p0", 25000)
	mustWriteNetDeviceWithSpeed(t, root, "mgmt100", "aa:bb:cc:dd:ee:10", "0000:30:00.0", "mlx5_core", 0, "p0", 100000)
	mustWriteLinkState(t, root, "mgmt25", "0", "down")
	mustWriteLinkState(t, root, "mgmt100", "0", "down")

	app := &App{
		Root: root,
		Machine: spec.MachineConfig{
			MgmtIfaces: []string{"ens20f0np0"},
		},
	}
	devices, err := app.discoverMgmtDevices()
	if err != nil {
		t.Fatalf("discover mgmt devices: %v", err)
	}
	if len(devices) != 1 || devices[0].Name != "mgmt25" {
		t.Fatalf("expected lowest-speed mgmt group, got %#v", devices)
	}
}

func TestRDMADiscoveryPrefersLinkedHighSpeedGroup(t *testing.T) {
	root := t.TempDir()
	mustWriteNetDeviceWithSpeed(t, root, "rdma400", "aa:bb:cc:dd:ee:40", "0000:40:00.0", "mlx5_core", 0, "p0", 400000)
	mustWriteNetDeviceWithSpeed(t, root, "rdma800", "aa:bb:cc:dd:ee:80", "0000:80:00.0", "mlx5_core", 0, "p0", 800000)
	mustWriteLinkState(t, root, "rdma400", "1", "up")
	mustWriteLinkState(t, root, "rdma800", "0", "down")

	app := &App{
		Root: root,
		Bundle: spec.Bundle{
			Defaults: spec.Defaults{RDMAExsist: boolPtr(true)},
		},
		Machine: spec.MachineConfig{
			RDMA: []spec.RDMAConfig{{Name: "ens11np0"}},
		},
	}
	devices, err := app.discoverRDMADevices()
	if err != nil {
		t.Fatalf("discover rdma devices: %v", err)
	}
	if len(devices) != 1 || devices[0].Name != "rdma400" {
		t.Fatalf("expected linked 400G RDMA group, got %#v", devices)
	}
}

func TestDiscoverNetDevicesIncludesVirtualNICWithoutPCIDevice(t *testing.T) {
	root := t.TempDir()
	netDir := filepath.Join(root, "sys", "class", "net", "eth0")
	if err := os.MkdirAll(netDir, 0o755); err != nil {
		t.Fatalf("mkdir net dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(netDir, "address"), []byte("aa:bb:cc:dd:ee:44\n"), 0o644); err != nil {
		t.Fatalf("write mac: %v", err)
	}

	app := &App{Root: root}
	devices, err := app.discoverNetDevices()
	if err != nil {
		t.Fatalf("discover net devices: %v", err)
	}
	if len(devices) != 1 {
		t.Fatalf("expected virtual nic to be discovered, got %#v", devices)
	}
	device := devices[0]
	if device.Name != "eth0" || device.MAC != "aa:bb:cc:dd:ee:44" {
		t.Fatalf("unexpected virtual nic: %#v", device)
	}
}

func TestDiscoverNetDevicesIncludesSysfsSymlinkEntries(t *testing.T) {
	root := t.TempDir()
	classNetDir := filepath.Join(root, "sys", "class", "net")
	if err := os.MkdirAll(classNetDir, 0o755); err != nil {
		t.Fatalf("mkdir class net dir: %v", err)
	}
	ifaceDir := filepath.Join(root, "sys", "devices", "pci0000:00", "0000:20:00.0", "net", "enp0s1")
	if err := os.MkdirAll(ifaceDir, 0o755); err != nil {
		t.Fatalf("mkdir iface dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ifaceDir, "address"), []byte("16:b3:1a:7c:a3:a0\n"), 0o644); err != nil {
		t.Fatalf("write mac: %v", err)
	}
	if err := os.Symlink(ifaceDir, filepath.Join(classNetDir, "enp0s1")); err != nil {
		t.Fatalf("symlink class net iface: %v", err)
	}

	app := &App{Root: root}
	devices, err := app.discoverNetDevices()
	if err != nil {
		t.Fatalf("discover net devices: %v", err)
	}
	if len(devices) != 1 || devices[0].Name != "enp0s1" || devices[0].MAC != "16:b3:1a:7c:a3:a0" {
		t.Fatalf("expected symlinked sysfs NIC to be discovered, got %#v", devices)
	}
}

func TestReviewNetDevicesIncludeNICsWithInvalidMAC(t *testing.T) {
	root := t.TempDir()
	netDir := filepath.Join(root, "sys", "class", "net", "enp0s1")
	if err := os.MkdirAll(netDir, 0o755); err != nil {
		t.Fatalf("mkdir net dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(netDir, "address"), []byte("not-a-mac\n"), 0o644); err != nil {
		t.Fatalf("write invalid mac: %v", err)
	}

	app := &App{Root: root}
	autoDevices, err := app.discoverNetDevices()
	if err != nil {
		t.Fatalf("discover net devices: %v", err)
	}
	if len(autoDevices) != 0 {
		t.Fatalf("expected automatic discovery to skip invalid MAC device, got %#v", autoDevices)
	}

	reviewDevices, err := app.discoverReviewNetDevices()
	if err != nil {
		t.Fatalf("discover review net devices: %v", err)
	}
	if len(reviewDevices) != 1 || reviewDevices[0].Name != "enp0s1" || reviewDevices[0].MAC != "" {
		t.Fatalf("expected TUI candidate without MAC, got %#v", reviewDevices)
	}
}

func TestDiscoverNetDevicesExcludesCalicoAndPrefersCarrierUp(t *testing.T) {
	root := t.TempDir()
	mustWriteNetDevice(t, root, "eno-down", "aa:bb:cc:dd:ee:11", "0000:20:00.0", "ixgbe", 0, "p0")
	mustWriteNetDevice(t, root, "eno-up", "aa:bb:cc:dd:ee:22", "0000:30:00.0", "ixgbe", 0, "p0")
	mustWriteVirtualNetDevice(t, root, "lo", "00:00:00:00:00:00")
	mustWriteVirtualNetDevice(t, root, "calico1234", "aa:bb:cc:dd:ee:33")
	mustWriteVirtualNetDevice(t, root, "docker0", "aa:bb:cc:dd:ee:44")
	mustWriteVirtualNetDevice(t, root, "bond0", "aa:bb:cc:dd:ee:55")
	mustWriteVirtualNetDevice(t, root, "cni0", "aa:bb:cc:dd:ee:66")
	mustWriteVirtualNetDevice(t, root, "ovs-system", "aa:bb:cc:dd:ee:77")
	mustWriteLinkState(t, root, "eno-down", "0", "down")
	mustWriteLinkState(t, root, "eno-up", "1", "up")

	app := &App{Root: root}
	devices, err := app.discoverNetDevices()
	if err != nil {
		t.Fatalf("discover net devices: %v", err)
	}
	if len(devices) != 2 {
		t.Fatalf("expected virtual devices to be excluded, got %#v", devices)
	}
	if got := devices[0].Name; got != "eno-up" {
		t.Fatalf("expected carrier-up device first, got %s from %#v", got, devices)
	}
	if got := deviceLinkLabel(devices[0]); got != "up" {
		t.Fatalf("expected up link label, got %s", got)
	}
}

func TestShouldIgnoreNetDeviceNameFiltersNonCandidateInterfaces(t *testing.T) {
	ignored := []string{
		"lo",
		"bond0",
		"team0",
		"dummy0",
		"ifb0",
		"tap0",
		"tun0",
		"tunl0",
		"sit0",
		"gre0",
		"gretap0",
		"erspan0",
		"ip6gre0",
		"ip6tnl0",
		"ip_vti0",
		"vti0",
		"vxlan.calico",
		"vxlan100",
		"genev0",
		"flannel.1",
		"cni0",
		"kube-ipvs0",
		"weave",
		"cilium_host",
		"calico1234",
		"caliabcd",
		"docker0",
		"podman0",
		"nerdctl0",
		"containerd0",
		"vethabc",
		"lxcbr0",
		"lxdbr0",
		"virbr0",
		"br0",
		"br-1234",
		"ovs-system",
		"wg0",
		"tailscale0",
		"ztabcdef",
		"nebula1",
		"macvlan0",
		"ipvlan0",
	}
	for _, name := range ignored {
		if !shouldIgnoreNetDeviceName(name) {
			t.Fatalf("expected %s to be ignored", name)
		}
	}

	kept := []string{"eno1", "ens20f0np0", "enp0s1", "eth0", "ib0", "mlx5_0"}
	for _, name := range kept {
		if shouldIgnoreNetDeviceName(name) {
			t.Fatalf("did not expect %s to be ignored", name)
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
			MgmtIP:     "172.16.18.11",
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
	if !strings.Contains(got, "discover /dev/mst/*_pciconf* devices") {
		t.Fatalf("expected automatic mst discovery in describe output, got:\n%s", got)
	}
}

func TestMlxconfigDevicesFromGlobFiltersSubfunctions(t *testing.T) {
	root := t.TempDir()
	mstDir := filepath.Join(root, "dev", "mst")
	if err := os.MkdirAll(mstDir, 0o755); err != nil {
		t.Fatalf("mkdir mst dir: %v", err)
	}
	for _, name := range []string{"mt4129_pciconf0", "mt4129_pciconf0.1", "mt4129_pciconf1", "not-pciconf"} {
		if err := os.WriteFile(filepath.Join(mstDir, name), []byte{}, 0o644); err != nil {
			t.Fatalf("write mst device %s: %v", name, err)
		}
	}
	app := &App{Root: root}
	got, err := app.mlxconfigDevicesFromGlob(filepath.Join(root, "dev", "mst", "*"))
	if err != nil {
		t.Fatalf("mlxconfig devices from glob: %v", err)
	}
	want := []string{
		filepath.Join(root, "dev", "mst", "mt4129_pciconf0"),
		filepath.Join(root, "dev", "mst", "mt4129_pciconf1"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected devices: got=%v want=%v", got, want)
	}
}

func TestMlxconfigDevicesAutoDiscoversAndPersistsSelection(t *testing.T) {
	root := t.TempDir()
	mstDir := filepath.Join(root, "dev", "mst")
	if err := os.MkdirAll(mstDir, 0o755); err != nil {
		t.Fatalf("mkdir mst dir: %v", err)
	}
	for _, name := range []string{"mt4129_pciconf0", "mt4129_pciconf1.1", "mt4129_pciconf2"} {
		if err := os.WriteFile(filepath.Join(mstDir, name), []byte{}, 0o644); err != nil {
			t.Fatalf("write mst device %s: %v", name, err)
		}
	}
	var output strings.Builder
	app := &App{
		Root:   root,
		DryRun: true,
		Bundle: spec.Bundle{
			MlxConfig: spec.MlxConfig{
				Settings: map[string]string{"CNP_DSCP_P1": "48"},
			},
		},
		Output: &output,
	}
	got, err := app.mlxconfigDevices()
	if err != nil {
		t.Fatalf("mlxconfig devices: %v", err)
	}
	want := []string{"/dev/mst/mt4129_pciconf0", "/dev/mst/mt4129_pciconf2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected devices: got=%v want=%v", got, want)
	}
	if !strings.Contains(output.String(), "would write /var/lib/envinit/mst-devices.json with 2 MST device") {
		t.Fatalf("expected dry-run persistence log, got:\n%s", output.String())
	}
}

func TestMlxconfigDevicesUsesPersistedSelection(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dev", "mst"), 0o755); err != nil {
		t.Fatalf("mkdir mst dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "dev", "mst", "mt4129_pciconf0"), []byte{}, 0o644); err != nil {
		t.Fatalf("write mst device: %v", err)
	}
	selection := `{"mlxconfig_devices":[{"mst":"/dev/mst/mt4129_pciconf0","pci":"0000:41:00.0"}]}`
	selectionPath := filepath.Join(root, "var", "lib", "envinit", "mst-devices.json")
	if err := os.MkdirAll(filepath.Dir(selectionPath), 0o755); err != nil {
		t.Fatalf("mkdir selection dir: %v", err)
	}
	if err := os.WriteFile(selectionPath, []byte(selection), 0o644); err != nil {
		t.Fatalf("write selection: %v", err)
	}
	app := &App{Root: root, Output: ioDiscard{}}
	got, err := app.mlxconfigDevices()
	if err != nil {
		t.Fatalf("mlxconfig devices: %v", err)
	}
	want := []string{"/dev/mst/mt4129_pciconf0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected persisted devices: got=%v want=%v", got, want)
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

func mustWriteVirtualNetDevice(t *testing.T, root string, iface string, mac string) {
	t.Helper()
	netDir := filepath.Join(root, "sys/class/net", iface)
	if err := os.MkdirAll(netDir, 0o755); err != nil {
		t.Fatalf("mkdir virtual net dir %s: %v", netDir, err)
	}
	if err := os.WriteFile(filepath.Join(netDir, "address"), []byte(mac+"\n"), 0o644); err != nil {
		t.Fatalf("write mac %s: %v", iface, err)
	}
}

func mustWriteLinkState(t *testing.T, root string, iface string, carrier string, operstate string) {
	t.Helper()
	netDir := filepath.Join(root, "sys/class/net", iface)
	if err := os.WriteFile(filepath.Join(netDir, "carrier"), []byte(carrier+"\n"), 0o644); err != nil {
		t.Fatalf("write carrier %s: %v", iface, err)
	}
	if err := os.WriteFile(filepath.Join(netDir, "operstate"), []byte(operstate+"\n"), 0o644); err != nil {
		t.Fatalf("write operstate %s: %v", iface, err)
	}
}

func mustWriteNetDevice(t *testing.T, root string, iface string, mac string, pci string, driver string, devPort int, physPortName string) {
	mustWriteNetDeviceWithSpeed(t, root, iface, mac, pci, driver, devPort, physPortName, 100000)
}

func mustWriteNetDeviceWithSpeed(t *testing.T, root string, iface string, mac string, pci string, driver string, devPort int, physPortName string, speed int) {
	mustWriteNetDeviceWithModelAndSpeed(t, root, iface, mac, pci, driver, devPort, physPortName, speed, "0x15b3", "0x101d")
}

func mustWriteNetDeviceWithModelAndSpeed(t *testing.T, root string, iface string, mac string, pci string, driver string, devPort int, physPortName string, speed int, vendor string, deviceID string) {
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
	if err := os.WriteFile(filepath.Join(netDir, "speed"), []byte(fmt.Sprintf("%d\n", speed)), 0o644); err != nil {
		t.Fatalf("write speed %s: %v", iface, err)
	}
	if err := os.WriteFile(filepath.Join(netDir, "phys_port_name"), []byte(physPortName+"\n"), 0o644); err != nil {
		t.Fatalf("write phys_port_name %s: %v", iface, err)
	}
	deviceDir := filepath.Join(root, "sys/devices/pci0000:00", pci)
	if err := os.MkdirAll(deviceDir, 0o755); err != nil {
		t.Fatalf("mkdir device dir %s: %v", deviceDir, err)
	}
	if err := os.WriteFile(filepath.Join(deviceDir, "vendor"), []byte(vendor+"\n"), 0o644); err != nil {
		t.Fatalf("write vendor %s: %v", iface, err)
	}
	if err := os.WriteFile(filepath.Join(deviceDir, "device"), []byte(deviceID+"\n"), 0o644); err != nil {
		t.Fatalf("write device %s: %v", iface, err)
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

func netDeviceNames(devices []netDevice) []string {
	names := make([]string, 0, len(devices))
	for _, device := range devices {
		names = append(names, device.Name)
	}
	return names
}

func assertRenderedLinesFit(t *testing.T, rendered string, width int) {
	t.Helper()
	for idx, line := range strings.Split(rendered, "\n") {
		if len(line) > width {
			t.Fatalf("line %d exceeds width %d (%d chars): %q\n%s", idx+1, width, len(line), line, rendered)
		}
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
