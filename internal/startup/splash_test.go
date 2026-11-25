package startup

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestSplashFitsTerminalSizes(t *testing.T) {
	for _, size := range [][2]int{{12, 5}, {24, 10}, {40, 18}, {80, 24}, {160, 50}} {
		model := screen{tool: "env_init", steps: []Step{{Label: "读取配置"}}, width: size[0], height: size[1]}
		view := model.View()
		if got := lipgloss.Width(view); got > size[0] {
			t.Fatalf("terminal %dx%d: rendered width %d", size[0], size[1], got)
		}
		if got := strings.Count(view, "\n") + 1; got > size[1] {
			t.Fatalf("terminal %dx%d: rendered height %d", size[0], size[1], got)
		}
	}
}
