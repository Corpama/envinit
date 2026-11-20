package configcli

import (
	"strings"
	"testing"

	"envinit/internal/configure"
	"envinit/internal/inventory"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestResponsiveWidthsFitTerminal(t *testing.T) {
	for width := 20; width <= 180; width++ {
		category, name, description, defaultValue, input := responsiveWidths(width)
		line := responsiveLine(width < 70, category, name, description, defaultValue, input, "默认网络", "管理网 MTU", "管理网数据包长度", "1500", "1500")
		if len([]rune(line)) == 0 {
			t.Fatal("empty responsive row")
		}
		if ansi.StringWidth(line) > width {
			t.Fatalf("row overflows: width=%d display=%d", width, ansi.StringWidth(line))
		}
	}
	_, _, description, current, input := responsiveWidths(280)
	if description > 40 || current <= description || input <= description {
		t.Fatal("wide terminal space must favor current value and input")
	}
}

func TestPolicyTableNavigationAndCollapse(t *testing.T) {
	model, err := newPolicyTableModel([]byte(`{"platform":{"os_family":"ubuntu"},"defaults":{"mgmt_mtu":1500,"configure_management_network":true},"post_power_action":{"action":"none"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(model.View(), "配置参数表") || len(model.items) == 0 {
		t.Fatal("table did not render")
	}
	for _, category := range []string{"物料", "超时时间", "check", "部署后"} {
		if model.data.Expanded[category] {
			t.Fatalf("%s must start collapsed", category)
		}
	}
	model = updatePolicyModel(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	model = updatePolicyModel(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if model.selected != 0 {
		t.Fatal("gg did not move to first row")
	}
	model = updatePolicyModel(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	if model.selected != len(model.items)-1 {
		t.Fatal("G did not move to last row")
	}
	model.selected = 0
	before := len(model.items)
	wasExpanded := model.data.Expanded[model.items[0].category]
	model = updatePolicyModel(model, tea.KeyMsg{Type: tea.KeySpace})
	if wasExpanded {
		if len(model.items) >= before {
			t.Fatal("space did not collapse category")
		}
	} else if len(model.items) <= before {
		t.Fatal("space did not expand category")
	}
	model = updatePolicyModel(model, tea.KeyMsg{Type: tea.KeyRight})
	if len(model.items) < before {
		t.Fatal("right did not expand category")
	}
	model = updatePolicyModel(model, tea.KeyMsg{Type: tea.KeyLeft})
	if model.data.Expanded[model.items[0].category] {
		t.Fatal("left did not collapse category")
	}
}

func updatePolicyModel(model policyTableModel, msg tea.KeyMsg) policyTableModel {
	updated, _ := model.Update(msg)
	return updated.(policyTableModel)
}

func TestInventoryTableEditsOneValueAndKeepsBlankValues(t *testing.T) {
	table, err := inventory.ParseTemplate([]byte("hostname,mgmt_ip,mgmt_prefix,Owner\nnode-a,,24,team-a\n"))
	if err != nil {
		t.Fatal(err)
	}
	model := newInventoryTableModel([]byte(`{"platform":{"os_family":"ubuntu"},"defaults":{"configure_management_network":true},"post_power_action":{"action":"none"}}`), table)
	if !strings.Contains(model.View(), "inventory 规划表") || len(model.items) == 0 {
		t.Fatal("inventory table did not render")
	}
	for i, item := range model.items {
		if item.column == "mgmt_ip" {
			model.selected = i
			break
		}
	}
	model.beginEdit()
	model.draft = []rune("10.0.0.2")
	model.cursor = len(model.draft)
	model.commitEdit()
	if table.Rows[0][1] != "10.0.0.2" {
		t.Fatalf("inventory edit not applied: %+v", table.Rows)
	}
	if !strings.Contains(string(table.CSV()), "node-a,10.0.0.2,24,team-a") {
		t.Fatal("inventory CSV did not contain edited value")
	}
	prepared, err := configure.PrepareTemplate(model.bundle, model.table)
	if err != nil || !strings.Contains(string(prepared.InventoryCSV), "node-a,10.0.0.2,24,team-a") {
		t.Fatalf("save preparation lost edited value: %v", err)
	}
	if strings.Count(model.View(), "10.0.0.2") != 1 {
		t.Fatal("current value duplicated into inactive input column")
	}
	model.beginEdit()
	model.draft = []rune("invalid-ip")
	model.cursor = len(model.draft)
	model.commitEdit()
	if !model.input || table.Rows[0][1] != "10.0.0.2" || !strings.Contains(model.status, "修改未应用") {
		t.Fatal("invalid IP did not keep editor open and roll back value")
	}
	model.draft = nil
	model.cursor = 0
	model.commitEdit()
	if model.input || table.Rows[0][1] != "" {
		t.Fatal("normal blank planning value was rejected")
	}
}

func TestInventoryNavigationResizeAndNewPlanning(t *testing.T) {
	table, err := inventory.ParseTemplate([]byte("hostname,mgmt_ip\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := newInventoryTableModel([]byte(`{"platform":{"os_family":"ubuntu"}}`), table)
	key := func(msg tea.KeyMsg) {
		next, _ := m.Update(msg)
		m = next.(inventoryTableModel)
	}
	key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if !m.input || len(table.Rows) != 1 {
		t.Fatal("empty inventory cannot start a machine")
	}
	key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("node-a")})
	key(tea.KeyMsg{Type: tea.KeyEnter})
	key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("+")})
	if columnIndex(table, "rdma1_ip") < 0 {
		t.Fatal("missing new RDMA planning fields")
	}
	key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	key(tea.KeyMsg{Type: tea.KeyEsc})
	key(tea.KeyMsg{Type: tea.KeyShiftTab})
	key(tea.KeyMsg{Type: tea.KeyDown})
	key(tea.KeyMsg{Type: tea.KeySpace})
	if m.items[m.selected].row != 0 || m.items[m.selected].column != "" || m.expanded["机器 1"] {
		t.Fatal("collapsing a field must retain focus on its machine heading")
	}
	key(tea.KeyMsg{Type: tea.KeyRight})
	for _, width := range []int{20, 40, 69, 70, 120} {
		next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 12})
		m = next.(inventoryTableModel)
		view := m.View()
		if !strings.Contains(view, "机器 1") || len(strings.Split(view, "\n")) > 12 {
			t.Fatalf("category or height broken at %d: %s", width, view)
		}
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > width {
				t.Fatalf("view overflows terminal at %d", width)
			}
		}
	}
	key(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.selected == 0 || m.selected < m.top || m.selected >= m.top+m.visibleRows() {
		t.Fatal("page navigation lost selection")
	}
	key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("G")})
	if m.selected != len(m.items)-1 {
		t.Fatal("G did not select last item")
	}
	key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	if m.selected != 0 || m.top != 0 {
		t.Fatal("gg did not select first item")
	}
	key(tea.KeyMsg{Type: tea.KeyCtrlS})
	if !m.saved {
		t.Fatalf("pending inventory cannot proceed: %s", m.status)
	}
}

func TestInventoryRejectsDuplicateHostnameAndPreservesMetadata(t *testing.T) {
	table, err := inventory.ParseTemplate([]byte("hostname,Owner\nnode-a,  team-a  \nnode-b,team-b\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := newInventoryTableModel([]byte(`{"platform":{"os_family":"ubuntu"}}`), table)
	for i, item := range m.items {
		if item.row == 1 && item.column == "hostname" {
			m.selected = i
		}
	}
	m.beginEdit()
	m.draft = []rune("node-a")
	m.cursor = len(m.draft)
	m.commitEdit()
	if !m.input || table.Rows[1][0] != "node-b" {
		t.Fatal("duplicate hostname was applied")
	}
	m.cancelEdit()
	for i, item := range m.items {
		if item.row == 0 && item.column == "Owner" {
			m.selected = i
		}
	}
	m.beginEdit()
	m.commitEdit()
	if table.Rows[0][1] != "  team-a  " {
		t.Fatal("metadata whitespace changed on no-op edit")
	}
}

func TestInventoryInitializationControls(t *testing.T) {
	table, _ := inventory.ParseTemplate([]byte("hostname\n"))
	m := newInventoryTableModel([]byte(`{"platform":{"os_family":"ubuntu"}}`), table)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	m = updated.(inventoryTableModel)
	for _, field := range []struct{ key, value string }{{"mgmt_mode", "bond"}, {"mgmt_target_name", "bond0"}, {"mgmt_prefix", "24"}, {"rdma_slots", "4"}} {
		for i, item := range m.items {
			if item.row == -1 && item.column == field.key {
				m.selected = i
			}
		}
		m.beginEdit()
		m.draft, m.cursor = []rune(field.value), len([]rune(field.value))
		m.commitEdit()
		if m.input {
			t.Fatalf("initialization failed: %s", m.status)
		}
	}
	m.addMachine()
	for key, value := range m.initial {
		if table.Rows[0][columnIndex(table, key)] != value {
			t.Fatalf("new machine did not inherit %s", key)
		}
	}
	if configure.InventorySlots(table) != 4 || columnIndex(table, "rdma4_gateway") < 0 {
		t.Fatal("RDMA maximum did not initialize headers")
	}
}

func TestInventoryInitializationLinksManagementTargetName(t *testing.T) {
	table, err := inventory.ParseTemplate([]byte("hostname,mgmt_mode,mgmt_target_name,mgmt_iface1\nnode-a,bond,bond0,eth0\nnode-b,bond,bond0,eth1\nnode-c,single,eth0,eth0\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := newInventoryTableModel([]byte(`{"platform":{"os_family":"ubuntu"}}`), table)
	m.initial = map[string]string{"mgmt_mode": "bond", "mgmt_target_name": "bond0"}
	if !m.initialize(map[string]string{"mgmt_mode": "bond", "mgmt_target_name": "bond10"}, configure.InventorySlots(table), "mgmt_target_name") {
		t.Fatalf("linked management target update failed: %s", m.status)
	}
	target := columnIndex(table, "mgmt_target_name")
	iface := columnIndex(table, "mgmt_iface1")
	for row := 0; row < 2; row++ {
		if table.Rows[row][target] != "bond10" {
			t.Fatalf("row %d target was not linked: %#v", row, table.Rows[row])
		}
	}
	if table.Rows[0][iface] != "eth0" || table.Rows[1][iface] != "eth1" || table.Rows[2][target] != "bond10" {
		t.Fatalf("management interface/target linkage was incorrect: %#v", table.Rows)
	}

	m.initial = map[string]string{"mgmt_mode": "single", "mgmt_target_name": "eth0"}
	if !m.initialize(map[string]string{"mgmt_mode": "single", "mgmt_target_name": "eth1"}, configure.InventorySlots(table), "mgmt_target_name") {
		t.Fatalf("single management target update failed: %s", m.status)
	}
	if table.Rows[2][target] != "eth1" || table.Rows[2][iface] != "eth1" {
		t.Fatalf("single management target did not follow interface: %#v", table.Rows[2])
	}
}

func TestInventoryInitializationLinksManagementPrefixAndGateway(t *testing.T) {
	table, err := inventory.ParseTemplate([]byte("hostname,mgmt_mode,mgmt_target_name,mgmt_ip,mgmt_prefix,mgmt_gateway\nnode-a,bond,bond0,10.0.0.2,26,10.0.0.1\nnode-b,bond,bond0,10.0.0.3,26,10.0.0.1\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := newInventoryTableModel([]byte(`{"platform":{"os_family":"ubuntu"}}`), table)
	m.initial = map[string]string{
		"mgmt_mode":        "bond",
		"mgmt_target_name": "bond0",
		"mgmt_prefix":      "26",
		"mgmt_gateway":     "10.0.0.1",
	}
	values := map[string]string{
		"mgmt_mode":        "bond",
		"mgmt_target_name": "bond0",
		"mgmt_prefix":      "24",
		"mgmt_gateway":     "10.0.0.254",
	}
	if !m.initialize(values, configure.InventorySlots(table), "mgmt_prefix") {
		t.Fatalf("linked management network update failed: %s", m.status)
	}
	prefix := columnIndex(table, "mgmt_prefix")
	gateway := columnIndex(table, "mgmt_gateway")
	for row, cells := range table.Rows {
		if cells[prefix] != "24" || cells[gateway] != "10.0.0.254" {
			t.Fatalf("row %d management network was not linked: %#v", row, cells)
		}
	}
}

func TestInventoryDeletesSelectedMachine(t *testing.T) {
	table, err := inventory.ParseTemplate([]byte("hostname,Owner\nnode-a,a\nnode-b,b\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := newInventoryTableModel([]byte(`{"platform":{"os_family":"ubuntu"}}`), table)
	for i, item := range m.items {
		if item.row == 1 && item.column == "" {
			m.selected = i
			break
		}
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("d")})
	m = updated.(inventoryTableModel)
	if len(table.Rows) != 1 || table.Rows[0][0] != "node-a" {
		t.Fatalf("selected machine was not deleted: %#v", table.Rows)
	}
	if !strings.Contains(m.status, "node-b") {
		t.Fatalf("delete status did not identify machine: %s", m.status)
	}
}

func TestInventoryHidesDerivedRouteFields(t *testing.T) {
	table, err := inventory.ParseTemplate([]byte("hostname,rdma1_ip,rdma1_prefix,rdma1_table,rdma1_route_cidr\nnode-a,10.0.0.2,24,101,10.0.0.0/24\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := newInventoryTableModel([]byte(`{"platform":{"os_family":"ubuntu"}}`), table)
	for _, item := range m.items {
		if item.column == "rdma1_table" || item.column == "rdma1_route_cidr" {
			t.Fatalf("derived route field was displayed: %#v", item)
		}
	}
	if !strings.Contains(string(table.CSV()), "rdma1_table") {
		t.Fatal("legacy route field was not retained for compatibility")
	}
}

func TestPolicyTableCurrentValuesAndEditingViewport(t *testing.T) {
	m, err := newPolicyTableModel([]byte(`{"platform":{"os_family":"ubuntu"},"defaults":{"mgmt_mtu":1600}}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(m.View(), "1600") != 1 || !strings.Contains(m.View(), "未设置") {
		t.Fatal("current values must appear once, without derived defaults")
	}
	for i, item := range m.items {
		if item.row != nil && item.row.Path == "defaults.mgmt_mtu" {
			m.selected = i
		}
	}
	m.beginEdit()
	m.draft = []rune("1700")
	m.cursor = len(m.draft)
	m.commitEdit()
	if m.input || m.items[m.selected].row.Path != "defaults.mgmt_mtu" || !strings.Contains(string(m.bundle), "1700") {
		t.Fatal("policy edit lost selection or value")
	}
	for _, width := range []int{20, 60, 80, 120} {
		m.width, m.height = width, 12
		m.selected, m.top = 0, 0
		view := m.View()
		if !strings.Contains(view, "默认网络") {
			t.Fatalf("category hidden at width %d", width)
		}
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > width {
				t.Fatalf("policy view exceeds width %d", width)
			}
		}
	}
	draft, cursor := insertDraft([]rune("服务器地址"), 2, []rune("新"))
	if string(draft) != "服务新器地址" || cursor != 3 {
		t.Fatalf("middle insertion failed: %s at %d", string(draft), cursor)
	}
	view := draftViewport(draft, len(draft), 5)
	if !strings.Contains(view, "▌") || ansi.StringWidth(view) > 5 {
		t.Fatalf("narrow input hides cursor: %s", view)
	}
}
