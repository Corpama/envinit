package runner

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// A topology decision is separate from the later per-slot binding review.
// A one-member bond requires an explicit Enter even when the candidate is exact.
func selectManagementNICs(devices []netDevice, singleBond bool) ([]netDevice, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("management NIC selection requires a TUI; fill inventory/physical binding information: %w", err)
	}
	defer tty.Close()
	m := managementSelection{devices: devices, selected: make(map[int]bool), singleBond: singleBond}
	for i := 0; i < len(devices) && i < 2; i++ {
		m.selected[i] = true
	}
	result, err := tea.NewProgram(m, tea.WithInput(tty), tea.WithOutput(tty), tea.WithAltScreen()).Run()
	if err != nil {
		return nil, err
	}
	final := result.(managementSelection)
	if !final.accepted {
		return nil, fmt.Errorf("management NIC selection cancelled; fill inventory binding information")
	}
	var selected []netDevice
	for i, d := range devices {
		if final.selected[i] {
			selected = append(selected, d)
		}
	}
	return selected, nil
}

type managementSelection struct {
	devices              []netDevice
	selected             map[int]bool
	cursor               int
	singleBond, accepted bool
	message              string
}

func (m managementSelection) Init() tea.Cmd { return nil }
func (m managementSelection) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor+1 < len(m.devices) {
				m.cursor++
			}
		case " ":
			if m.selected[m.cursor] {
				delete(m.selected, m.cursor)
			} else if len(m.selected) < 2 {
				m.selected[m.cursor] = true
			}
		case "enter":
			if len(m.selected) == 0 {
				m.message = "Select one or two NICs"
			} else {
				m.accepted = true
				return m, tea.Quit
			}
		}
	}
	return m, nil
}
func (m managementSelection) View() string {
	var b strings.Builder
	fmt.Fprintln(&b, "Management NIC selection (link-up ports recommended)")
	if m.singleBond {
		fmt.Fprintln(&b, "WARNING: only ONE physical NIC is available. Enter confirms a single-member bond.")
	}
	for i, d := range m.devices {
		cursor, tick := " ", " "
		if i == m.cursor {
			cursor = ">"
		}
		if m.selected[i] {
			tick = "x"
		}
		fmt.Fprintf(&b, "%s [%s] %-16s %-18s link=%s\n", cursor, tick, d.Name, d.MAC, deviceLinkLabel(d))
	}
	fmt.Fprintln(&b, "Up/Down: move | Space: select 1 or 2 | Enter: confirm | q: cancel", m.message)
	return b.String()
}
