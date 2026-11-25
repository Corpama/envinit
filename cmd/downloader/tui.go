package main

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type downloaderTUI struct {
	manifest   releaseManifest
	profiles   []manifestProfile
	selected   int
	outputDir  string
	jobs       int
	phase      string
	detail     string
	progress   float64
	bytes      int64
	totalBytes int64
	files      int
	totalFiles int
	started    bool
	completed  bool
	cancelled  bool
	err        error
	width      int
	height     int
	start      func(int) tea.Cmd
}

func runManifestTUI(opts runOptions, manifest releaseManifest) error {
	selected := 0
	if opts.Profile != "" {
		found := false
		for i, p := range manifest.Profiles {
			if p.ID == opts.Profile {
				selected = i
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("unknown profile %q; run with --list-profiles to see supported profiles", opts.Profile)
		}
	}
	m := downloaderTUI{
		manifest: manifest, profiles: manifest.Profiles, selected: selected,
		outputDir: normalizedOutputDir(opts.OutputDir), jobs: opts.Jobs,
		phase: "选择交付系统",
	}
	var program *tea.Program
	m.start = func(index int) tea.Cmd {
		return func() tea.Msg {
			profile := manifest.Profiles[index]
			reporter := &tuiReporter{send: program.Send}
			err := downloadSelectedProfile(opts, manifest, profile, reporter)
			if err != nil {
				return downloadErrorMsg{err: err}
			}
			return downloadDoneMsg{}
		}
	}
	program = tea.NewProgram(m, tea.WithAltScreen())
	result, err := program.Run()
	if err != nil {
		return err
	}
	model := result.(downloaderTUI)
	if model.cancelled {
		return fmt.Errorf("download canceled")
	}
	if model.completed {
		// Print after Bubble Tea restores the terminal so the command stays visible.
		printConfigHint(opts)
	}
	return model.err
}

func normalizedOutputDir(value string) string {
	if strings.TrimSpace(value) == "" {
		return defaultOutputDir
	}
	return value
}

func isInteractiveDownloader() bool {
	for _, file := range []*os.File{os.Stdin, os.Stdout, os.Stderr} {
		info, err := file.Stat()
		if err != nil || info.Mode()&os.ModeCharDevice == 0 {
			return false
		}
	}
	return os.Getenv("TERM") != "dumb" && os.Getenv("ENVINIT_NO_TUI") != "1"
}

func (m downloaderTUI) Init() tea.Cmd { return nil }

func (m downloaderTUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if !m.started && m.selected > 0 {
				m.selected--
			}
		case "down", "j":
			if !m.started && m.selected+1 < len(m.profiles) {
				m.selected++
			}
		case "enter":
			if !m.started {
				m.started = true
				m.phase = "准备下载"
				return m, m.start(m.selected)
			}
		case "esc", "q", "ctrl+c":
			m.cancelled = true
			return m, tea.Quit
		}
	case downloadProgressMsg:
		m.phase, m.detail = msg.phase, msg.detail
		m.progress, m.bytes, m.totalBytes = msg.progress, msg.bytes, msg.totalBytes
		m.files, m.totalFiles = msg.files, msg.totalFiles
	case downloadDoneMsg:
		m.completed = true
		m.phase, m.progress = "交付目录已就绪", 1
		return m, tea.Quit
	case downloadErrorMsg:
		m.err = msg.err
		return m, tea.Quit
	}
	return m, nil
}

func (m downloaderTUI) View() string {
	bg := lipgloss.Color("#002B32")
	base := lipgloss.NewStyle().Foreground(lipgloss.Color("#E0F2F2"))
	cyan := base.Foreground(lipgloss.Color("#00D7EB"))
	dim := base.Foreground(lipgloss.Color("#79A9AD"))
	width, height := m.width, m.height
	if width <= 0 {
		width = 80
	}
	if height <= 0 {
		height = 24
	}
	padding := minInt(4, maxInt(0, (width-2)/10))
	innerWidth := maxInt(1, width-padding*2)
	fit := func(value string) string { return ansi.Truncate(value, innerWidth, "…") }
	styled := func(style lipgloss.Style, value string) string { return style.Render(fit(value)) }
	var b strings.Builder
	lines := make([]string, 0, 16)
	lines = append(lines, styled(cyan.Bold(true), "KUNLUN · Downloader"))
	if height >= 8 {
		lines = append(lines, styled(dim, "离线交付物料组装工具"))
	}
	if innerWidth >= 32 && height >= 10 {
		lines = append(lines, cyan.Render(strings.Repeat("─", minInt(62, innerWidth))))
	}
	if !m.started {
		lines = append(lines, styled(base.Bold(true), "选择交付系统"), "")
		for i, p := range m.profiles {
			mark := "  "
			if i == m.selected {
				mark = "▸ "
			}
			label := p.ID
			if p.Name != "" {
				label = p.Name
			}
			line := fmt.Sprintf("%s%d. %s", mark, i+1, label)
			if i == m.selected {
				line = styled(cyan, line)
			} else {
				line = fit(line)
			}
			lines = append(lines, line)
			if i == m.selected && p.Description != "" && height >= 14 {
				lines = append(lines, styled(dim, "     "+p.Description))
			}
		}
		lines = append(lines, "", styled(dim, "↑/↓ 选择 · Enter 开始 · Esc 退出"))
	} else {
		lines = append(lines, styled(cyan, m.phase), styled(dim, "目录: "+m.outputDir))
		if m.detail != "" {
			lines = append(lines, styled(dim, m.detail))
		}
		n := minInt(52, maxInt(3, innerWidth-7))
		fill := int(float64(n) * m.progress)
		if fill < 0 {
			fill = 0
		}
		if fill > n {
			fill = n
		}
		lines = append(lines, "", styled(cyan, "["+strings.Repeat("▰", fill)+strings.Repeat("─", n-fill)+"] "+fmt.Sprintf("%3.0f%%", clamp01(m.progress)*100)))
		if m.totalFiles > 0 {
			lines = append(lines, styled(base, fmt.Sprintf("%s / %s · %d/%d 文件", formatBytes(m.bytes), formatBytes(m.totalBytes), m.files, m.totalFiles)))
		}
		if height >= 10 {
			lines = append(lines, "", styled(dim, "已完成文件会保留；再次运行可继续下载"))
		}
	}
	verticalPadding := 0
	if height >= 5 {
		verticalPadding = 1
	}
	maxLines := maxInt(1, height-verticalPadding*2)
	if len(lines) > maxLines {
		if maxLines == 1 {
			lines = lines[:1]
		} else {
			lines = append(append([]string{}, lines[:maxLines-1]...), styled(dim, "…"))
		}
	}
	b.WriteString(strings.Join(lines, "\n"))
	content := lipgloss.NewStyle().Background(bg).Padding(verticalPadding, padding).Render(b.String())
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, content, lipgloss.WithWhitespaceBackground(bg))
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func clamp01(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

type downloadProgressMsg struct {
	phase, detail     string
	progress          float64
	bytes, totalBytes int64
	files, totalFiles int
}
type downloadDoneMsg struct{}
type downloadErrorMsg struct{ err error }

type tuiReporter struct{ send func(tea.Msg) }

func (r *tuiReporter) update(phase, detail string, progress float64, bytes, totalBytes int64, files, totalFiles int) {
	if r != nil && r.send != nil {
		r.send(downloadProgressMsg{phase, detail, progress, bytes, totalBytes, files, totalFiles})
	}
}

var quietDownloaderOutput bool

func downloaderPrintf(format string, args ...any) {
	if !quietDownloaderOutput {
		fmt.Printf(format, args...)
	}
}

func downloaderPrintln(args ...any) {
	if !quietDownloaderOutput {
		fmt.Println(args...)
	}
}
