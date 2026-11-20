package checker

import (
	"fmt"
	"net"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"envinit/internal/spec"
	tea "github.com/charmbracelet/bubbletea"
)

type gidCandidate struct {
	Device, Address, Netdev, Version string
	Port, Index                      int
}

func gidProbeCommand(device string, port int) string {
	return fmt.Sprintf(`p=/sys/class/infiniband/%s/ports/%d
for f in "$p"/gids/*; do
  [ -f "$f" ] || continue
  i=${f##*/}
  printf 'GID|%%s|%%s|%%s|%%s\n' "$i" "$(cat "$f")" "$(cat "$p/gid_attrs/ndevs/$i" 2>/dev/null)" "$(cat "$p/gid_attrs/types/$i" 2>/dev/null)"
done`, shellQuote(device), port)
}

func parseGIDCandidates(output, device string, port int) []gidCandidate {
	var out []gidCandidate
	ibPort := port
	ibIndex := regexp.MustCompile(`GID\[\s*([0-9]+)\s*\]:\s*(\S+)`)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "port:") {
			ibPort, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "port:")))
			continue
		}
		c := gidCandidate{Device: device, Port: port}
		if fields := strings.Split(line, "|"); len(fields) == 5 && fields[0] == "GID" {
			index, err := strconv.Atoi(fields[1])
			if err != nil || index < 0 {
				continue
			}
			c.Index, c.Address, c.Netdev, c.Version = index, fields[2], fields[3], fields[4]
		} else if match := ibIndex.FindStringSubmatch(line); len(match) == 3 {
			c.Index, _ = strconv.Atoi(match[1])
			c.Address, c.Port = match[2], ibPort
		} else {
			fields := strings.Fields(line)
			if len(fields) < 5 || fields[0] != device {
				continue
			}
			p, e1 := strconv.Atoi(fields[1])
			i, e2 := strconv.Atoi(fields[2])
			if e1 != nil || e2 != nil || i < 0 {
				continue
			}
			c.Port, c.Index, c.Address = p, i, fields[3]
			for _, f := range fields[4:] {
				if ip := net.ParseIP(f); ip != nil && ip.To4() != nil {
					c.Address = ip.String()
				}
				if f == "v1" || f == "v2" {
					c.Version = f
				}
			}
			c.Netdev = fields[len(fields)-1]
		}
		ip := net.ParseIP(c.Address)
		if c.Port != port || ip == nil || ip.IsUnspecified() {
			continue
		}
		c.Address = ip.String()
		out = append(out, c)
	}
	return out
}

func preferredGID(candidates []gidCandidate, iface, address string) (int, bool) {
	var matches []gidCandidate
	for _, c := range candidates {
		ip := net.ParseIP(c.Address)
		if ip == nil || ip.IsLinkLocalUnicast() {
			continue
		}
		if iface != "" && c.Netdev != "" && c.Netdev != iface {
			continue
		}
		if address != "" && !ip.Equal(net.ParseIP(address)) {
			continue
		}
		matches = append(matches, c)
	}
	sort.SliceStable(matches, func(i, j int) bool {
		v2 := func(v string) bool { return strings.Contains(strings.ToLower(v), "v2") }
		if v2(matches[i].Version) != v2(matches[j].Version) {
			return v2(matches[i].Version)
		}
		return matches[i].Index < matches[j].Index
	})
	if len(matches) == 0 {
		return 0, false
	}
	return matches[0].Index, true
}

func resolveBandwidthGIDs(opts Options, targets []Target, groups resolvedRDMAGroups) error {
	if !opts.Bundle.Check.Bandwidth.GIDIndexAuto {
		return nil
	}
	verbs := false
	for _, mode := range normalizedBandwidthModes(opts.BandwidthModes) {
		verbs = verbs || mode == BandwidthModeVerbs
	}
	if !verbs {
		return nil
	}
	for _, target := range targets {
		for i := range groups[target.Name] {
			group := &groups[target.Name][i]
			port := group.IBPort
			if port == 0 {
				port = 1
			}
			if opts.DryRun {
				fmt.Fprintf(opts.Output, "dry-run GID auto: %s %s port %d (resolved on target at execution)\n", target.Name, group.IBDevice, port)
				continue
			}
			iface, address := targetRDMAInterfaceName(opts.Bundle, target, i), ""
			if i < len(target.RDMA) {
				address = target.RDMA[i].IP
			}
			var candidates []gidCandidate
			var evidence []string
			var selected int
			found := false
			for _, cmd := range []string{gidProbeCommand(group.IBDevice, port), "show_gids", "ibv_devinfo -v -d " + shellQuote(group.IBDevice)} {
				output, err := runCheckCommand(opts, target, cmd)
				if err != nil {
					evidence = append(evidence, err.Error())
				}
				candidates = append(candidates, parseGIDCandidates(output, group.IBDevice, port)...)
				if selected, found = preferredGID(candidates, iface, address); found {
					break
				}
			}
			if !found {
				if err := checkCancellationError(opts); err != nil {
					return err
				}
				var err error
				selected, err = selectGIDIndex(opts, target, *group, candidates, strings.Join(evidence, "\n"))
				if err != nil {
					return err
				}
			}
			group.GIDIndex = new(int)
			*group.GIDIndex = selected
			fmt.Fprintf(opts.Output, "GID %s %s port=%d interface=%s index=%d\n", target.Name, group.IBDevice, port, iface, selected)
		}
	}
	return nil
}

func selectGIDIndex(opts Options, target Target, group spec.CheckRDMAGroup, candidates []gidCandidate, evidence string) (int, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return 0, fmt.Errorf("GID auto failed on %s/%s: TUI unavailable; specify check.bandwidth.gid_index: %w", target.Name, group.IBDevice, err)
	}
	defer tty.Close()
	if opts.checkTUI != nil {
		if err := opts.checkTUI.program.ReleaseTerminal(); err != nil {
			return 0, err
		}
		defer opts.checkTUI.program.RestoreTerminal()
	}
	m := gidSelection{title: fmt.Sprintf("GID auto failed: %s / %s; type an index, then Enter", target.Name, group.IBDevice), candidates: candidates, evidence: evidence}
	final, err := tea.NewProgram(m, tea.WithInput(tty), tea.WithOutput(tty), tea.WithAltScreen(), tea.WithContext(checkContext(opts))).Run()
	if err != nil {
		return 0, err
	}
	result := final.(gidSelection)
	if !result.accepted {
		return 0, fmt.Errorf("GID selection cancelled for %s/%s", target.Name, group.IBDevice)
	}
	return strconv.Atoi(result.value)
}

type gidSelection struct {
	title, value, evidence string
	candidates             []gidCandidate
	accepted               bool
}

func (m gidSelection) Init() tea.Cmd { return nil }
func (m gidSelection) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "backspace":
			if len(m.value) > 0 {
				m.value = m.value[:len(m.value)-1]
			}
		case "enter":
			if n, err := strconv.Atoi(m.value); err == nil && n >= 0 {
				m.accepted = true
				return m, tea.Quit
			}
		default:
			if len(key.String()) == 1 && key.String() >= "0" && key.String() <= "9" {
				m.value += key.String()
			}
		}
	}
	return m, nil
}
func (m gidSelection) View() string {
	var b strings.Builder
	fmt.Fprintln(&b, m.title)
	for _, c := range m.candidates {
		fmt.Fprintf(&b, "index=%d port=%d %s %s %s\n", c.Index, c.Port, c.Netdev, c.Address, c.Version)
	}
	if len(m.candidates) == 0 {
		fmt.Fprintln(&b, "No usable candidates returned. Inspect target GID table before entering an override.")
	}
	fmt.Fprintf(&b, "%s\nIndex: %s\nEnter confirm | q cancel\n", m.evidence, m.value)
	return b.String()
}
