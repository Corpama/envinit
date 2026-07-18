package main

import (
	"strings"
	"testing"
)

func TestMaterialProgressShowsAggregatePercentageBarBytesSpeedAndFiles(t *testing.T) {
	files := []materialFile{
		{RelativePath: "cached.rpm", Size: 1024},
		{RelativePath: "downloaded.rpm", Size: 2048},
	}
	var output strings.Builder
	progress := newMaterialProgress(&output, files)
	progress.start()
	progress.addExisting(1024)
	progress.completeFile()
	progress.addDownloaded(2048)
	progress.completeFile()
	progress.finish()

	for _, want := range []string{
		"[==============================]",
		"100.00%",
		"3.00 KiB/3.00 KiB",
		"/s",
		"2/2 files",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("progress output missing %q:\n%s", want, output.String())
		}
	}
}

func TestMaterialProgressUsesFilePercentageForZeroByteEntries(t *testing.T) {
	files := []materialFile{{RelativePath: "empty-a"}, {RelativePath: "empty-b"}}
	var output strings.Builder
	progress := newMaterialProgress(&output, files)
	progress.completeFile()
	progress.completeFile()
	progress.finish()

	if !strings.Contains(output.String(), "100.00%") || !strings.Contains(output.String(), "2/2 files") {
		t.Fatalf("zero-byte progress output:\n%s", output.String())
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		value int64
		want  string
	}{
		{value: 0, want: "0 B"},
		{value: 1024, want: "1.00 KiB"},
		{value: 3 * 1024 * 1024, want: "3.00 MiB"},
		{value: 5 * 1024 * 1024 * 1024, want: "5.00 GiB"},
	}
	for _, tt := range tests {
		if got := formatBytes(tt.value); got != tt.want {
			t.Fatalf("formatBytes(%d) = %q, want %q", tt.value, got, tt.want)
		}
	}
}
