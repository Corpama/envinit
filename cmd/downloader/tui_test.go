package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestDownloaderTUIFitsSmallAndLargeTerminals(t *testing.T) {
	model := downloaderTUI{
		profiles: []manifestProfile{
			{ID: "ubuntu22.04-x86_64", Name: "Ubuntu 22.04 x86_64", Description: "Ubuntu apt/deb material profile"},
			{ID: "kylin10sp3-x86_64", Name: "Kylin V10 SP3 x86_64", Description: "Kylin yum/rpm material profile"},
		},
		outputDir:  "/a/very/long/custom/delivery/directory/that/must/be/truncated",
		phase:      "下载基础交付文件",
		detail:     "/releases/v0.4.1/env_tool-base-v0.4.1.tar",
		started:    true,
		progress:   0.42,
		bytes:      512 << 20,
		totalBytes: 2 << 30,
		files:      42,
		totalFiles: 1473,
	}
	for _, size := range [][2]int{{20, 6}, {40, 10}, {80, 24}, {160, 50}} {
		model.width, model.height = size[0], size[1]
		view := model.View()
		if got := lipgloss.Width(view); got > size[0] {
			t.Fatalf("terminal %dx%d: rendered width %d\n%s", size[0], size[1], got, view)
		}
		if got := strings.Count(view, "\n") + 1; got > size[1] {
			t.Fatalf("terminal %dx%d: rendered height %d", size[0], size[1], got)
		}
	}
}
