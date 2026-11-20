// Package startup provides the short interactive welcome screen for both tools.
package startup

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

type Step struct {
	Label string
	Load  func() error
}

func visible(args []string) bool {
	if os.Getenv("ENVINIT_NO_SPLASH") == "1" || os.Getenv("TERM") == "dumb" || !term.IsTerminal(os.Stdin.Fd()) || !term.IsTerminal(os.Stdout.Fd()) || !term.IsTerminal(os.Stderr.Fd()) {
		return false
	}
	for _, arg := range args {
		name, _, _ := strings.Cut(arg, "=")
		switch strings.TrimLeft(name, "-") {
		case "h", "help", "list-profiles", "plain", "no-tui", "yes", "check-only", "dry-run", "recover", "__network-worker":
			return false
		}
	}
	return true
}

// Run executes the same preparation steps with or without a terminal. Keep I/O
// on the caller goroutine so it never outlives the command or its project lock.
func Run(tool string, args []string, steps ...Step) error {
	if !visible(args) {
		for _, step := range steps {
			if err := step.Load(); err != nil {
				return err
			}
		}
		return nil
	}
	p := tea.NewProgram(screen{tool: tool, steps: steps}, tea.WithAltScreen(), tea.WithInput(nil))
	exit := make(chan error, 1)
	go func() {
		m, err := p.Run()
		if err == nil && !m.(screen).ready() {
			err = fmt.Errorf("startup interrupted")
		}
		exit <- err
	}()
	for i, step := range steps {
		select {
		case err := <-exit:
			return err
		default:
		}
		if err := step.Load(); err != nil {
			p.Quit()
			<-exit
			return err
		}
		p.Send(progressMsg(i + 1))
	}
	return <-exit
}

type screen struct {
	tool                     string
	steps                    []Step
	width, height, completed int
	minTime                  bool
}

type progressMsg int
type minTimeMsg struct{}

func (s screen) ready() bool { return s.minTime && s.completed == len(s.steps) }
func (s screen) Init() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return minTimeMsg{} })
}

func (s screen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
	case progressMsg:
		s.completed = int(msg)
	case minTimeMsg:
		s.minTime = true
	}
	if s.ready() {
		return s, tea.Quit
	}
	return s, nil
}

func (s screen) View() string {
	if s.width <= 0 || s.height <= 0 {
		return ""
	}
	bg := lipgloss.Color("#002B32")
	space := lipgloss.WithWhitespaceBackground(bg)
	base := lipgloss.NewStyle().Background(bg).Foreground(lipgloss.Color("#E0F2F2"))
	cyan := base.Foreground(lipgloss.Color("#00D7EB"))
	name, sub, features := "EnvInit", "机器环境初始化工具", "系统配置  │  依赖安装  │  网络配置  │  环境校验"
	if s.tool != "env_init" {
		name, sub, features = "Downloader", "离线交付组装工具", "配置规划  │  物料下载  │  文件校验  │  交付组装"
	}
	var lines []string
	if s.height >= 22 && s.width >= 32 {
		// Each row is centered separately to form the dotted dome around the stack.
		for _, line := range []string{
			"· · · · ·",
			"·               ·",
			"·                     ·",
			"·       ──{⚙}──       ·",
			"╱      ╱         ╲      ╲",
			"┊     ╲───────────╱     ┊",
			"┊     ╱           ╲     ┊",
			"┊     ╲───────────╱     ┊",
			"╲     ╱           ╲     ╱",
			"·    ╲───────────╱    ·",
			"·┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄·",
		} {
			lines = append(lines, cyan.Render(line))
		}
	} else if s.height >= 15 && s.width >= 18 {
		for _, line := range []string{"╭┄┄{⚙}┄┄╮", "┊ ╲─────╱ ┊", "┊ ╲─────╱ ┊", "╰┄╲─────╱┄╯"} {
			lines = append(lines, cyan.Render(line))
		}
	}
	title := cyan.Bold(true).Render("KUNLUN · ") + base.Bold(true).Render(name)
	if s.width >= 36 {
		title = cyan.Render("┌─  ") + title + cyan.Render("  ─┐")
	}
	lines = append(lines, title)
	if s.height >= 8 {
		lines = append(lines, base.Render(sub), "")
	}
	if s.width >= 52 && s.height >= 12 {
		rule := cyan.Render(strings.Repeat("─", min(64, s.width-4)))
		lines = append(lines, rule, base.Render(features), rule, "")
	}
	if len(s.steps) > 0 {
		status := "启动就绪"
		if s.completed < len(s.steps) {
			status = "加载中 · " + s.steps[s.completed].Label
		}
		n := min(42, max(0, s.width-10))
		fill := n * s.completed / len(s.steps)
		lines = append(lines, base.Render(status), cyan.Render("▕"+strings.Repeat("▰", fill))+base.Render(strings.Repeat("─", n-fill)+fmt.Sprintf("▏ %d%%", 100*s.completed/len(s.steps))))
	}
	if s.height >= 9 {
		lines = append(lines, "", base.Italic(true).Render("—  By Bill Wang  —"))
	}
	for i, line := range lines {
		lines[i] = lipgloss.PlaceHorizontal(s.width, lipgloss.Center, ansi.Truncate(line, s.width, ""), space)
	}
	return lipgloss.PlaceVertical(s.height, lipgloss.Center, strings.Join(lines[:min(s.height, len(lines))], "\n"), space)
}
