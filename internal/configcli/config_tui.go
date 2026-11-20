package configcli

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"

	"envinit/internal/configure"
	"envinit/internal/inventory"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// configUI is retained for --plain and for source/confirmation prompts. The
// actual bundle policy editor is the responsive table below.
type configUI struct {
	plain  bool
	input  *bufio.Reader
	output io.Writer
}

func (ui *configUI) ask(title, body string) (string, error) {
	if ui.plain {
		fmt.Fprintf(ui.output, "%s\n%s\n> ", body, title)
		line, err := ui.input.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("config canceled: %w", err)
		}
		return strings.TrimSpace(line), nil
	}
	m := textPrompt{title: title, body: body, height: 24}
	result, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return "", err
	}
	answer := result.(textPrompt)
	if answer.canceled {
		return "", fmt.Errorf("config canceled")
	}
	return strings.TrimSpace(string(answer.value)), nil
}

type textPrompt struct {
	title, body string
	value       []rune
	height      int
	canceled    bool
}

func (m textPrompt) Init() tea.Cmd { return nil }
func (m textPrompt) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.height = msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.canceled = true
			return m, tea.Quit
		case "enter":
			return m, tea.Quit
		case "backspace", "ctrl+h":
			if len(m.value) > 0 {
				m.value = m.value[:len(m.value)-1]
			}
		default:
			if msg.Type == tea.KeyRunes {
				m.value = append(m.value, msg.Runes...)
			} else if msg.Type == tea.KeySpace {
				m.value = append(m.value, ' ')
			}
		}
	}
	return m, nil
}
func (m textPrompt) View() string {
	lines := strings.Split(m.body, "\n")
	if len(lines) > max(1, m.height-7) {
		lines = lines[:max(1, m.height-7)]
	}
	return lipgloss.NewStyle().Bold(true).Render("config · 离线规划") + "\n" +
		strings.Join(lines, "\n") + "\n\n" + m.title + "\n> " + string(m.value) +
		"\nEnter 确认 · Esc 取消"
}

type tableItem struct {
	category string
	row      *configure.PolicyEditorRow
}

type policyTableModel struct {
	bundle   []byte
	data     configure.PolicyEditorData
	items    []tableItem
	selected int
	top      int
	width    int
	height   int
	input    bool
	draft    []rune
	cursor   int
	pendingG bool
	status   string
	canceled bool
	saved    bool
}

func newPolicyTableModel(bundle []byte) (policyTableModel, error) {
	data, err := configure.PolicyEditor(bundle)
	if err != nil {
		return policyTableModel{}, err
	}
	m := policyTableModel{bundle: append([]byte(nil), bundle...), data: data, width: 120, height: 30}
	m.rebuildItems()
	return m, nil
}

func (m *policyTableModel) rebuildItems() {
	var selected tableItem
	if len(m.items) > 0 {
		selected = m.items[m.selected]
	}
	m.items = nil
	for _, category := range configure.PolicyCategories(m.data) {
		// Only the default network category starts open.
		m.items = append(m.items, tableItem{category: category})
		if !m.data.Expanded[category] {
			continue
		}
		for i := range m.data.Rows {
			if m.data.Rows[i].Category == category {
				m.items = append(m.items, tableItem{category: category, row: &m.data.Rows[i]})
			}
		}
	}
	for i, item := range m.items {
		if item.category == selected.category && (item.row == nil || selected.row != nil && item.row.Path == selected.row.Path) {
			m.selected = i
		}
	}
	if len(m.items) == 0 {
		m.status = "当前 bundle 没有可编辑的常用参数；按 q 取消，回到文件编辑高级配置。"
	}
	m.selected = max(0, min(m.selected, len(m.items)-1))
	m.ensureVisible()
}

func (m policyTableModel) Init() tea.Cmd { return nil }

func (m *policyTableModel) visibleRows() int {
	return max(1, m.height-8)
}

func (m *policyTableModel) ensureVisible() {
	page := m.visibleRows()
	if m.selected < m.top {
		m.top = m.selected
	}
	if m.selected >= m.top+page {
		m.top = m.selected - page + 1
	}
	m.top = max(0, min(m.top, max(0, len(m.items)-page)))
}

func (m *policyTableModel) move(delta int) {
	if len(m.items) == 0 {
		return
	}
	m.selected = max(0, min(m.selected+delta, len(m.items)-1))
	m.pendingG = false
	m.ensureVisible()
}

func (m *policyTableModel) movePage(delta int) {
	m.move(delta * m.visibleRows())
}

func (m *policyTableModel) toggleCategory() {
	if len(m.items) == 0 {
		return
	}
	item := m.items[m.selected]
	if item.row != nil {
		// A row's space key toggles its enclosing category, which is convenient
		// on small keyboards and avoids needing a separate category selection.
		itemCategory := item.category
		m.data.Expanded[itemCategory] = !m.data.Expanded[itemCategory]
		m.rebuildItems()
		return
	}
	m.data.Expanded[item.category] = !m.data.Expanded[item.category]
	m.rebuildItems()
}

func (m *policyTableModel) beginEdit() {
	if len(m.items) == 0 || m.items[m.selected].row == nil {
		m.toggleCategory()
		return
	}
	m.input = true
	m.draft = []rune(m.items[m.selected].row.Current)
	m.cursor = len(m.draft)
	m.status = "输入新值；Enter 应用，Esc 放弃。布尔/数字可直接输入，列表请输入 JSON。"
}

func (m *policyTableModel) commitEdit() {
	if !m.input || m.items[m.selected].row == nil {
		return
	}
	row := m.items[m.selected].row
	updated, err := configure.EditPolicy(m.bundle, row.Path, strings.TrimSpace(string(m.draft)))
	if err != nil {
		m.status = "修改未应用：" + err.Error()
		return
	}
	data, err := configure.PolicyEditor(updated)
	if err != nil {
		m.status = "修改未应用：" + err.Error()
		return
	}
	m.bundle = updated
	data.Expanded = m.data.Expanded
	m.data = data
	m.input = false
	m.draft = nil
	m.cursor = 0
	m.status = "已修改；Ctrl+S 进入 inventory 规划表，q 取消。"
	m.rebuildItems()
}

func (m *policyTableModel) cancelEdit() {
	m.input = false
	m.draft = nil
	m.cursor = 0
	m.status = "已放弃本次输入。"
}

func (m *policyTableModel) resetBundle() {
	data, err := configure.PolicyEditor(m.bundle)
	if err != nil {
		m.status = err.Error()
		return
	}
	m.data = data
	m.status = "已重新读取当前值。"
	m.rebuildItems()
}

func (m policyTableModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ensureVisible()
	case tea.KeyMsg:
		if m.input {
			m.updateInput(msg)
			break
		}
		if msg.Type == tea.KeySpace {
			m.toggleCategory()
			break
		}
		// Bubble Tea represents shifted letters as runes on some terminals and
		// as named keys on others. Handle both so Vim-style gg/G is portable.
		if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 {
			switch msg.Runes[0] {
			case 'G':
				m.selected = max(0, len(m.items)-1)
				m.ensureVisible()
				m.pendingG = false
				return m, nil
			case 'g':
				if m.pendingG {
					m.selected = 0
					m.top = 0
					m.pendingG = false
				} else {
					m.pendingG = true
					m.status = "再按 g 回到第一项。"
				}
				return m, nil
			}
		}
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			m.canceled = true
			return m, tea.Quit
		case "ctrl+s", "s":
			m.saved = true
			return m, tea.Quit
		case "up", "k":
			m.move(-1)
		case "down", "j":
			m.move(1)
		case "pgup", "ctrl+u":
			m.movePage(-1)
		case "pgdown", "ctrl+d":
			m.movePage(1)
		case "home":
			m.selected = 0
			m.ensureVisible()
		case "end", "G":
			m.selected = max(0, len(m.items)-1)
			m.ensureVisible()
		case "g":
			if m.pendingG {
				m.selected = 0
				m.top = 0
				m.pendingG = false
			} else {
				m.pendingG = true
				m.status = "再按 g 回到第一项。"
			}
		case "enter":
			m.beginEdit()
		case "space":
			m.toggleCategory()
		case "right":
			if len(m.items) > 0 {
				category := m.items[m.selected].category
				m.data.Expanded[category] = true
				m.rebuildItems()
			}
		case "left":
			if len(m.items) > 0 {
				category := m.items[m.selected].category
				m.data.Expanded[category] = false
				m.rebuildItems()
			}
		case "r":
			m.resetBundle()
		default:
			m.pendingG = false
		}
	}
	return m, nil
}

func (m *policyTableModel) updateInput(msg tea.KeyMsg) {
	switch msg.String() {
	case "ctrl+c", "esc":
		m.cancelEdit()
	case "enter":
		m.commitEdit()
	case "backspace", "ctrl+h":
		if m.cursor > 0 {
			m.draft = append(m.draft[:m.cursor-1], m.draft[m.cursor:]...)
			m.cursor--
		}
	case "delete":
		if m.cursor < len(m.draft) {
			m.draft = append(m.draft[:m.cursor], m.draft[m.cursor+1:]...)
		}
	case "left", "ctrl+b":
		m.cursor = max(0, m.cursor-1)
	case "right", "ctrl+f":
		m.cursor = min(len(m.draft), m.cursor+1)
	case "home", "ctrl+a":
		m.cursor = 0
	case "end", "ctrl+e":
		m.cursor = len(m.draft)
	default:
		if msg.Type == tea.KeyRunes {
			m.draft, m.cursor = insertDraft(m.draft, m.cursor, msg.Runes)
		} else if msg.Type == tea.KeySpace {
			m.draft, m.cursor = insertDraft(m.draft, m.cursor, []rune{' '})
		}
	}
}

func (m policyTableModel) View() string {
	if m.width <= 0 {
		m.width = 120
	}
	if m.height <= 0 {
		m.height = 30
	}
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")).Render("config · 配置参数表")
	subtitle := "Enter 编辑 · Ctrl+S 下一页 · ↑↓/jk 移动 · Space 折叠 · PgUp/PgDn 翻页 · gg/G 首末 · q 取消"
	if m.input {
		subtitle = "正在编辑 · Enter 应用  Esc 放弃  ←→移动  Home/End跳转"
	}
	compact := m.width < 70
	categoryWidth, nameWidth, descWidth, defaultWidth, inputWidth := responsiveWidths(m.width)
	lines := []string{title, lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render(subtitle)}
	header := responsiveLine(compact, categoryWidth, nameWidth, descWidth, defaultWidth, inputWidth, "分类", "参数", "作用", "当前值", "修改为")
	lines = append(lines, header, strings.Repeat("─", max(1, min(m.width, lipgloss.Width(header)))))
	page := m.visibleRows()
	end := min(len(m.items), m.top+page)
	for index := m.top; index < end; index++ {
		item := m.items[index]
		if item.row == nil {
			marker := "▶"
			if m.data.Expanded[item.category] {
				marker = "▼"
			}
			category := marker + " " + item.category
			line := responsiveLine(compact, categoryWidth, nameWidth, descWidth, defaultWidth, inputWidth, category, "", "按 Enter/Space 展开或折叠", "", "")
			lines = append(lines, m.decorateLine(index == m.selected, line))
			continue
		}
		row := *item.row
		value := ""
		if m.input && index == m.selected {
			value = draftViewport(m.draft, m.cursor, inputWidth)
		}
		current := row.Current
		if !row.Explicit {
			current = "未设置"
		}
		line := responsiveLine(compact, categoryWidth, nameWidth, descWidth, defaultWidth, inputWidth, "", row.Label, row.Description, current, value)
		lines = append(lines, m.decorateLine(index == m.selected, line))
	}
	if end == m.top {
		lines = append(lines, "（没有需要配置的普通参数；复杂配置请直接编辑 bundle.json）")
	}
	status := m.status
	if status == "" {
		status = fmt.Sprintf("第 %d-%d / 共 %d 项", min(m.top+1, len(m.items)), end, len(m.items))
	} else {
		status = fmt.Sprintf("%s    (%d-%d/%d)", status, min(m.top+1, len(m.items)), end, len(m.items))
	}
	detail := ""
	if len(m.items) > 0 && m.items[m.selected].row != nil {
		row := m.items[m.selected].row
		detail = row.Path + " · " + row.Description
	}
	lines = append(lines, detail, lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render(status))
	return fitTableView(lines, m.width, m.height)
}

func (m policyTableModel) decorateLine(selected bool, line string) string {
	if selected {
		return lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("255")).Render(line)
	}
	return line
}

func tableLine(categoryWidth, nameWidth, descWidth, defaultWidth, inputWidth int, category, name, description, defaultValue, inputValue string) string {
	return strings.Join([]string{
		cell(category, categoryWidth),
		cell(name, nameWidth),
		cell(description, descWidth),
		cell(defaultValue, defaultWidth),
		cell(inputValue, inputWidth),
	}, " │ ")
}

func responsiveWidths(width int) (category, name, description, defaultValue, input int) {
	if width < 70 {
		inner := max(5, width-6)
		name = max(1, inner*4/10)
		defaultValue = max(1, (inner-name)/2)
		input = max(1, inner-name-defaultValue)
		return 0, name, 0, defaultValue, input
	}
	category = min(12, max(10, width/10))
	name = min(28, max(12, width/6))
	description = min(40, max(10, width/5))
	remaining := width - category - name - description - 12
	defaultValue = remaining / 2
	input = remaining - defaultValue
	return
}

func responsiveLine(compact bool, categoryWidth, nameWidth, descWidth, defaultWidth, inputWidth int, category, name, description, defaultValue, inputValue string) string {
	if compact {
		if name == "" && category != "" {
			return cell(category, nameWidth+defaultWidth+inputWidth+6)
		}
		return strings.Join([]string{cell(name, nameWidth), cell(defaultValue, defaultWidth), cell(inputValue, inputWidth)}, " │ ")
	}
	return tableLine(categoryWidth, nameWidth, descWidth, defaultWidth, inputWidth, category, name, description, defaultValue, inputValue)
}

func fitTableView(lines []string, width, height int) string {
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "…")
	}
	return strings.Join(lines[:min(len(lines), height)], "\n")
}

func insertDraft(draft []rune, cursor int, text []rune) ([]rune, int) {
	result := make([]rune, 0, len(draft)+len(text))
	result = append(result, draft[:cursor]...)
	result = append(result, text...)
	result = append(result, draft[cursor:]...)
	return result, cursor + len(text)
}

func draftViewport(draft []rune, cursor, width int) string {
	start := cursor
	used := 1 // Keep the insertion cursor visible even in narrow cells.
	for start > 0 {
		w := ansi.StringWidth(string(draft[start-1]))
		if used+w > width {
			break
		}
		used += w
		start--
	}
	return string(draft[start:cursor]) + "▌" + ansi.Truncate(string(draft[cursor:]), max(0, width-used), "")
}

func cell(value string, width int) string {
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\t", " ")
	if width < 1 {
		return ""
	}
	if ansi.StringWidth(value) > width {
		value = truncateCell(value, width)
	}
	return value + strings.Repeat(" ", max(0, width-ansi.StringWidth(value)))
}

func truncateCell(value string, width int) string {
	if width <= 1 {
		return "…"
	}
	var out []rune
	used := 0
	for _, r := range value {
		part := string(r)
		w := ansi.StringWidth(part)
		if used+w > width-1 {
			break
		}
		out = append(out, r)
		used += w
	}
	return string(out) + "…"
}

func runPolicyTable(bundle []byte) ([]byte, string, error) {
	model, err := newPolicyTableModel(bundle)
	if err != nil {
		return nil, "", err
	}
	program := tea.NewProgram(model, tea.WithAltScreen())
	result, err := program.Run()
	if err != nil {
		return nil, "", err
	}
	final := result.(policyTableModel)
	if final.canceled {
		return nil, "cancel", nil
	}
	if !final.saved {
		return nil, "cancel", nil
	}
	return final.bundle, "save", nil
}

type inventoryItem struct {
	category    string
	row         int
	column      string
	label       string
	description string
	current     string
}

type inventoryTableModel struct {
	table    *inventory.TemplateTable
	bundle   []byte
	items    []inventoryItem
	selected int
	top      int
	width    int
	height   int
	input    bool
	draft    []rune
	cursor   int
	pendingG bool
	status   string
	canceled bool
	saved    bool
	expanded map[string]bool
	initial  map[string]string
}

var inventoryFieldHelp = map[string][2]string{
	"hostname":         {"主机名", "这台服务器的唯一名称；可留空待填，已填名称不能重复。"},
	"mgmt_mode":        {"管理网模式", "single 单网卡、bond 双网卡绑定、auto 自动判断。"},
	"mgmt_target_name": {"管理网目标名", "single 为网卡名，bond 为 bond 名，例如 bond0。"},
	"mgmt_ip":          {"管理网 IP", "服务器管理网络的规划地址，装机前填写。"},
	"mgmt_prefix":      {"管理网前缀", "管理网掩码位数，例如 24。"},
	"mgmt_gateway":     {"管理网网关", "管理网络的网关地址。"},
	"mgmt_iface1":      {"管理网卡 1", "管理网第一张物理网卡名称；未知时可留空待现场确认。"},
	"mgmt_mac1":        {"管理 MAC 1", "管理网第一张网卡 MAC；未知时可留空待现场确认。"},
	"mgmt_iface2":      {"管理网卡 2", "bond 模式的第二张管理网卡名称。"},
	"mgmt_mac2":        {"管理 MAC 2", "第二张管理网卡 MAC；未知时可留空待现场确认。"},
}

func inventoryHelp(column string) (string, string) {
	if help, ok := inventoryFieldHelp[column]; ok {
		return help[0], help[1]
	}
	if strings.HasPrefix(column, "rdma") {
		rest := strings.TrimPrefix(column, "rdma")
		parts := strings.SplitN(rest, "_", 2)
		slot := parts[0]
		field := "规划值"
		if len(parts) == 2 {
			field = map[string]string{
				"name": "接口名", "ip": "IP 地址", "prefix": "网络前缀", "gateway": "网关",
				"mac": "MAC 地址", "rail_id": "Rail 编号", "table": "路由表号", "route_cidr": "路由网段",
			}[parts[1]]
			if field == "" {
				field = parts[1]
			}
		}
		return fmt.Sprintf("RDMA%s %s", slot, field), fmt.Sprintf("第 %s 个 RDMA 端口的%s；未知时可留空待现场确认。", slot, field)
	}
	return column, "项目自定义列，值会原样保留。"
}

func newInventoryTableModel(bundle []byte, table *inventory.TemplateTable) inventoryTableModel {
	m := inventoryTableModel{bundle: append([]byte(nil), bundle...), table: table, width: 120, height: 30, expanded: map[string]bool{}, initial: map[string]string{}}
	m.rebuildItems()
	return m
}

func (m *inventoryTableModel) rebuildItems() {
	var selected inventoryItem
	if len(m.items) > 0 {
		selected = m.items[m.selected]
	}
	m.items = nil
	m.items = append(m.items, inventoryItem{category: "初始化", row: -1})
	if m.expanded["初始化"] {
		for _, column := range []string{"mgmt_mode", "mgmt_target_name", "mgmt_prefix", "mgmt_gateway", "rdma_slots"} {
			label, description := inventoryHelp(column)
			current := m.initial[column]
			if column == "rdma_slots" {
				label, description = "RDMA 最大个数", "生成 rdma1_* 至 rdmaN_* 列组，0..64；缩减时仅移除全空槽位。"
				current = strconv.Itoa(configure.InventorySlots(m.table))
			} else {
				label += "（初始化）"
				description += " 初始化仅填已有机器的空值，并用于本次新增机器。"
			}
			m.items = append(m.items, inventoryItem{category: "初始化", row: -1, column: column, label: label, description: description, current: current})
		}
	}
	for row := range m.table.Rows {
		category := fmt.Sprintf("机器 %d", row+1)
		if _, ok := m.expanded[category]; !ok {
			m.expanded[category] = true
		}
		m.items = append(m.items, inventoryItem{category: category, row: row})
		for i, value := range m.table.Rows[row] {
			if i >= len(m.table.Header) {
				continue
			}
			column := m.table.Header[i]
			if hiddenInventoryColumn(column) {
				continue
			}
			label, description := inventoryHelp(column)
			if m.expanded[category] {
				m.items = append(m.items, inventoryItem{category: category, row: row, column: column, label: label, description: description, current: value})
			}
		}
	}
	for i, item := range m.items {
		if item.category == selected.category && (item.column == "" || item.column == selected.column) {
			m.selected = i
		}
	}
	m.selected = max(0, min(m.selected, len(m.items)-1))
	m.ensureVisible()
}

func hiddenInventoryColumn(column string) bool {
	return strings.HasSuffix(column, "_table") || strings.HasSuffix(column, "_route_cidr")
}

func (m inventoryTableModel) Init() tea.Cmd { return nil }

func (m *inventoryTableModel) visibleRows() int { return max(1, m.height-8) }

func (m *inventoryTableModel) ensureVisible() {
	page := m.visibleRows()
	if m.selected < m.top {
		m.top = m.selected
	}
	if m.selected >= m.top+page {
		m.top = m.selected - page + 1
	}
	m.top = max(0, min(m.top, max(0, len(m.items)-page)))
}

func (m *inventoryTableModel) move(delta int) {
	if len(m.items) == 0 {
		return
	}
	m.selected = max(0, min(m.selected+delta, len(m.items)-1))
	m.ensureVisible()
}

func (m *inventoryTableModel) movePage(delta int) { m.move(delta * m.visibleRows()) }

func (m *inventoryTableModel) beginEdit() {
	if len(m.items) == 0 {
		return
	}
	item := m.items[m.selected]
	if item.column == "" {
		m.expanded[item.category] = !m.expanded[item.category]
		m.rebuildItems()
		return
	}
	m.input = true
	m.draft = []rune(item.current)
	m.cursor = len(m.draft)
	m.status = "输入规划值；Enter 应用，Esc 放弃。空值表示待填写或现场确认。"
}

func (m *inventoryTableModel) commitEdit() {
	if !m.input || len(m.items) == 0 {
		return
	}
	item := m.items[m.selected]
	value := string(m.draft)
	if item.row == -1 {
		values := make(map[string]string, len(m.initial))
		for key, value := range m.initial {
			values[key] = value
		}
		slots := configure.InventorySlots(m.table)
		if item.column == "rdma_slots" {
			var err error
			slots, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				m.status = "修改未应用：RDMA 最大个数请输入 0..64 的整数。"
				return
			}
		} else {
			values[item.column] = strings.TrimSpace(value)
		}
		if !m.initialize(values, slots, item.column) {
			return
		}
		m.input, m.draft, m.cursor = false, nil, 0
		return
	}
	if item.column == "hostname" || strings.HasPrefix(item.column, "mgmt_") || strings.HasPrefix(item.column, "rdma") {
		value = strings.TrimSpace(value)
	}
	old := m.table.Rows[item.row][columnIndex(m.table, item.column)]
	m.table.Set(item.row, item.column, value)
	// PrepareTemplate performs the filled-value conflict checks while still
	// allowing normal blank planning fields.
	checked, err := configure.PrepareTemplate(m.bundle, m.table)
	if err != nil {
		m.table.Set(item.row, item.column, old)
		m.status = "修改未应用：" + err.Error()
		return
	}
	m.items[m.selected].current = value
	m.input = false
	m.draft = nil
	m.cursor = 0
	m.status = fmt.Sprintf("已修改；待填 %d 项，需 review %d 项。Ctrl+S 进入保存预览。", len(checked.Pending), len(checked.Review))
}

func (m *inventoryTableModel) cancelEdit() {
	m.input = false
	m.draft = nil
	m.cursor = 0
	m.status = "已放弃本次输入。"
}

func columnIndex(table *inventory.TemplateTable, column string) int {
	for i, name := range table.Header {
		if name == column {
			return i
		}
	}
	return -1
}

func (m *inventoryTableModel) addMachine() {
	if !m.initialize(m.initial, configure.InventorySlots(m.table), "") {
		return
	}
	m.table.Rows = append(m.table.Rows, make([]string, len(m.table.Header)))
	for column, key := range m.table.Header {
		m.table.Rows[len(m.table.Rows)-1][column] = m.initial[key]
	}
	m.rebuildItems()
	for i, item := range m.items {
		if item.row == len(m.table.Rows)-1 && item.column == "hostname" {
			m.selected = i
			break
		}
	}
	m.ensureVisible()
	m.beginEdit()
}

func (m *inventoryTableModel) deleteMachine() {
	if len(m.items) == 0 {
		return
	}
	row := m.items[m.selected].row
	if row < 0 || row >= len(m.table.Rows) {
		m.status = "请先选择要删除的机器行。"
		return
	}
	hostname := ""
	if column := columnIndex(m.table, "hostname"); column >= 0 && column < len(m.table.Rows[row]) {
		hostname = strings.TrimSpace(m.table.Rows[row][column])
	}
	m.table.Rows = append(m.table.Rows[:row], m.table.Rows[row+1:]...)
	// Rebuild from scratch because row numbers are part of the visible
	// category labels. Select the adjacent machine after deletion.
	m.items = nil
	m.selected, m.top = 0, 0
	m.rebuildItems()
	if len(m.table.Rows) > 0 {
		row = min(row, len(m.table.Rows)-1)
		for i, item := range m.items {
			if item.row == row && item.column == "" {
				m.selected = i
				break
			}
		}
		m.ensureVisible()
	}
	if hostname == "" {
		hostname = fmt.Sprintf("机器 %d", row+1)
	}
	m.status = fmt.Sprintf("已删除 %s；Ctrl+S 进入保存预览。", hostname)
}

func (m *inventoryTableModel) addRDMASlot() {
	slot := configure.InventorySlots(m.table)
	row := m.items[m.selected].row
	prefix := fmt.Sprintf("rdma%d_", slot+1)
	if !m.initialize(m.initial, slot+1, "") {
		return
	}
	m.expanded[m.items[m.selected].category] = true
	m.rebuildItems()
	for i, item := range m.items {
		if item.row == row && item.column == prefix+"name" {
			m.selected = i
			break
		}
	}
	m.ensureVisible()
	m.status = fmt.Sprintf("已添加 RDMA%d 列组；所有机器对应空槽位可继续填写。", slot+1)
}

func (m *inventoryTableModel) initialize(values map[string]string, slots int, changed string) bool {
	previous := make(map[string]string, len(m.initial))
	for key, value := range m.initial {
		previous[key] = value
	}
	working := &inventory.TemplateTable{
		Header:   append([]string(nil), m.table.Header...),
		Warnings: append([]string(nil), m.table.Warnings...),
	}
	working.Rows = make([][]string, len(m.table.Rows))
	for row, cells := range m.table.Rows {
		working.Rows[row] = append([]string(nil), cells...)
	}
	// Initialization values are also the shared management-network settings.
	// Apply an explicit change to existing rows before validation so changing a
	// shared prefix/gateway or renaming a target is reflected by every machine.
	if isLinkedManagementInitializationField(changed) {
		m.linkManagementInitialization(working, values)
	}
	table, err := configure.InitializeInventory(m.bundle, working, values, slots)
	if err != nil {
		m.status = "初始化未应用：" + err.Error()
		return false
	}
	if _, err := configure.PrepareTemplate(m.bundle, table); err != nil {
		m.status = "初始化未应用：" + err.Error()
		return false
	}
	*m.table = *table
	m.initial = values
	m.rebuildItems()
	m.status = "初始化已应用：补齐表头及空字段；本次新增机器使用这些初始值。Ctrl+S 预览。"
	return true
}

func (m *inventoryTableModel) linkManagementInitialization(table *inventory.TemplateTable, values map[string]string) {
	fields := []string{"mgmt_mode", "mgmt_target_name", "mgmt_prefix", "mgmt_gateway"}
	columns := make(map[string]int, len(fields))
	for _, field := range fields {
		columns[field] = columnIndex(table, field)
	}
	mode := strings.ToLower(strings.TrimSpace(values["mgmt_mode"]))
	for row := range table.Rows {
		for _, field := range fields {
			column := columns[field]
			if column < 0 {
				continue
			}
			value := strings.TrimSpace(values[field])
			if field == "mgmt_mode" {
				value = mode
			}
			table.Rows[row][column] = value
		}
		if mode == "single" {
			target := strings.TrimSpace(values["mgmt_target_name"])
			if ifaceColumn := columnIndex(table, "mgmt_iface1"); ifaceColumn >= 0 {
				table.Rows[row][ifaceColumn] = target
			}
		}
	}
}

func isLinkedManagementInitializationField(field string) bool {
	switch field {
	case "mgmt_mode", "mgmt_target_name", "mgmt_prefix", "mgmt_gateway":
		return true
	default:
		return false
	}
}

func (m *inventoryTableModel) moveMachine(delta int) {
	if len(m.items) == 0 {
		return
	}
	row := max(0, min(m.items[m.selected].row+delta, len(m.table.Rows)-1))
	for i, item := range m.items {
		if item.row == row && item.column == "" {
			m.selected = i
			break
		}
	}
	m.ensureVisible()
}

func (m inventoryTableModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ensureVisible()
	case tea.KeyMsg:
		if m.input {
			m.updateInput(msg)
			break
		}
		if msg.Type == tea.KeySpace {
			if len(m.items) > 0 {
				category := m.items[m.selected].category
				m.expanded[category] = !m.expanded[category]
				m.rebuildItems()
			}
			m.pendingG = false
			break
		}
		if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 {
			switch msg.Runes[0] {
			case 'G':
				m.selected = max(0, len(m.items)-1)
				m.ensureVisible()
				m.pendingG = false
				return m, nil
			case 'g':
				if m.pendingG {
					m.selected = 0
					m.top = 0
					m.status = ""
				} else {
					m.status = "等待第二个 g"
				}
				m.pendingG = !m.pendingG
				return m, nil
			}
		}
		m.pendingG = false
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			m.canceled = true
			return m, tea.Quit
		case "ctrl+s", "s":
			if _, err := configure.PrepareTemplate(m.bundle, m.table); err != nil {
				m.status = "请修正后继续：" + err.Error()
				break
			}
			m.saved = true
			return m, tea.Quit
		case "a":
			m.addMachine()
		case "d", "delete":
			m.deleteMachine()
		case "i":
			m.expanded["初始化"] = true
			m.rebuildItems()
			m.selected, m.top = 1, 0
		case "+":
			m.addRDMASlot()
		case "tab", "]":
			m.moveMachine(1)
		case "shift+tab", "[":
			m.moveMachine(-1)
		case "up", "k":
			m.move(-1)
		case "down", "j":
			m.move(1)
		case "pgup", "ctrl+u":
			m.movePage(-1)
		case "pgdown", "ctrl+d":
			m.movePage(1)
		case "home":
			m.selected = 0
			m.ensureVisible()
		case "end":
			m.selected = max(0, len(m.items)-1)
			m.ensureVisible()
		case "space":
			if len(m.items) > 0 {
				item := m.items[m.selected]
				m.expanded[item.category] = !m.expanded[item.category]
				m.rebuildItems()
			}
		case "right":
			if len(m.items) > 0 {
				m.expanded[m.items[m.selected].category] = true
				m.rebuildItems()
			}
		case "left":
			if len(m.items) > 0 {
				m.expanded[m.items[m.selected].category] = false
				m.rebuildItems()
			}
		case "enter":
			m.beginEdit()
		}
	}
	return m, nil
}

func (m *inventoryTableModel) updateInput(msg tea.KeyMsg) {
	switch msg.String() {
	case "ctrl+c", "esc":
		m.cancelEdit()
	case "enter":
		m.commitEdit()
	case "backspace", "ctrl+h":
		if m.cursor > 0 {
			m.draft = append(m.draft[:m.cursor-1], m.draft[m.cursor:]...)
			m.cursor--
		}
	case "delete":
		if m.cursor < len(m.draft) {
			m.draft = append(m.draft[:m.cursor], m.draft[m.cursor+1:]...)
		}
	case "left", "ctrl+b":
		m.cursor = max(0, m.cursor-1)
	case "right", "ctrl+f":
		m.cursor = min(len(m.draft), m.cursor+1)
	case "home", "ctrl+a":
		m.cursor = 0
	case "end", "ctrl+e":
		m.cursor = len(m.draft)
	default:
		if msg.Type == tea.KeyRunes {
			m.draft, m.cursor = insertDraft(m.draft, m.cursor, msg.Runes)
		} else if msg.Type == tea.KeySpace {
			m.draft, m.cursor = insertDraft(m.draft, m.cursor, []rune{' '})
		}
	}
}

func (m inventoryTableModel) View() string {
	if m.width <= 0 {
		m.width = 120
	}
	if m.height <= 0 {
		m.height = 30
	}
	compact := m.width < 70
	categoryWidth, nameWidth, descriptionWidth, currentWidth, inputWidth := responsiveWidths(m.width)
	subtitle := "Enter 编辑 · Ctrl+S 预览 · i 初始化 · a 新机器 · d 删除机器 · + RDMA · Tab 换机器 · Space 折叠 · PgUp/PgDn 翻页 · gg/G 首末 · q 取消"
	if m.input {
		subtitle = "正在编辑 · Enter 应用 · Esc 放弃 · ←→移动 · Home/End跳转"
	}
	lines := []string{
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")).Render("config · inventory 规划表"),
		lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render(subtitle),
	}
	header := responsiveLine(compact, categoryWidth, nameWidth, descriptionWidth, currentWidth, inputWidth, "机器", "字段", "作用", "当前值", "修改为")
	lines = append(lines, header, strings.Repeat("─", max(1, min(m.width, lipgloss.Width(header)))))
	page := m.visibleRows()
	end := min(len(m.items), m.top+page)
	for index := m.top; index < end; index++ {
		item := m.items[index]
		if item.column == "" {
			marker := "▶"
			if m.expanded[item.category] {
				marker = "▼"
			}
			line := responsiveLine(compact, categoryWidth, nameWidth, descriptionWidth, currentWidth, inputWidth, marker+" "+item.category, "", "Enter/Space 展开或折叠", "", "")
			if index == m.selected {
				line = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("255")).Render(line)
			}
			lines = append(lines, line)
			continue
		}
		value := ""
		if m.input && index == m.selected {
			value = draftViewport(m.draft, m.cursor, inputWidth)
		}
		line := responsiveLine(compact, categoryWidth, nameWidth, descriptionWidth, currentWidth, inputWidth, "", item.label, item.description, item.current, value)
		if index == m.selected {
			line = lipgloss.NewStyle().Background(lipgloss.Color("236")).Foreground(lipgloss.Color("255")).Render(line)
		}
		lines = append(lines, line)
	}
	status := m.status
	if status == "" {
		status = "空值可以保留，表示后续继续规划或现场发现"
	}
	detail := "按 a 添加机器，按 d 删除当前机器，按 + 添加 RDMA 槽位列组；Tab/Shift+Tab 切换机器。"
	if len(m.items) > 0 {
		item := m.items[m.selected]
		detail = item.category + " · " + item.column + " · " + item.description
	}
	lines = append(lines, detail, lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render(fmt.Sprintf("%s    (%d-%d/%d)", status, min(m.top+1, len(m.items)), end, len(m.items))))
	return fitTableView(lines, m.width, m.height)
}

func runInventoryTable(bundle []byte, table *inventory.TemplateTable) (*inventory.TemplateTable, string, error) {
	model := newInventoryTableModel(bundle, table)
	result, err := tea.NewProgram(model, tea.WithAltScreen()).Run()
	if err != nil {
		return nil, "", err
	}
	final := result.(inventoryTableModel)
	if final.canceled || !final.saved {
		return nil, "cancel", nil
	}
	return final.table, "save", nil
}
