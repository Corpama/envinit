package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

const materialProgressBarWidth = 30

type materialProgress struct {
	mu             sync.Mutex
	output         io.Writer
	dynamic        bool
	totalBytes     int64
	completedBytes int64
	networkBytes   int64
	totalFiles     int
	completedFiles int
	networkStarted time.Time
	lastRender     time.Time
	lastLineWidth  int
}

func newMaterialProgress(output io.Writer, files []materialFile) *materialProgress {
	if output == nil {
		output = io.Discard
	}
	var totalBytes int64
	for _, file := range files {
		if file.Size > 0 && totalBytes <= (1<<63-1)-file.Size {
			totalBytes += file.Size
		}
	}
	return &materialProgress{
		output:     output,
		dynamic:    isInteractiveWriter(output),
		totalBytes: totalBytes,
		totalFiles: len(files),
	}
}

func (p *materialProgress) start() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.renderLocked(true)
	p.mu.Unlock()
}

func isInteractiveWriter(output io.Writer) bool {
	file, ok := output.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func (p *materialProgress) addExisting(delta int64) {
	if p == nil || delta == 0 {
		return
	}
	p.mu.Lock()
	p.completedBytes = clampProgressBytes(p.completedBytes+delta, p.totalBytes)
	p.renderLocked(false)
	p.mu.Unlock()
}

func (p *materialProgress) addDownloaded(delta int64) {
	if p == nil || delta <= 0 {
		return
	}
	p.mu.Lock()
	if p.networkStarted.IsZero() {
		p.networkStarted = time.Now()
	}
	p.networkBytes += delta
	p.completedBytes = clampProgressBytes(p.completedBytes+delta, p.totalBytes)
	p.renderLocked(false)
	p.mu.Unlock()
}

func (p *materialProgress) completeFile() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.completedFiles < p.totalFiles {
		p.completedFiles++
	}
	p.renderLocked(false)
	p.mu.Unlock()
}

func (p *materialProgress) warning(format string, args ...any) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.dynamic && p.lastLineWidth > 0 {
		fmt.Fprintf(p.output, "\r%s\r", strings.Repeat(" ", p.lastLineWidth))
	}
	fmt.Fprintf(p.output, "WARNING "+format+"\n", args...)
	p.lastLineWidth = 0
	p.renderLocked(true)
	p.mu.Unlock()
}

func (p *materialProgress) finish() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.renderLocked(true)
	if p.dynamic {
		fmt.Fprintln(p.output)
	}
	p.mu.Unlock()
}

func (p *materialProgress) renderLocked(force bool) {
	now := time.Now()
	if !force {
		if !p.dynamic || (!p.lastRender.IsZero() && now.Sub(p.lastRender) < 200*time.Millisecond) {
			return
		}
	}
	p.lastRender = now

	percentage := p.percentageLocked()
	filled := int(percentage * materialProgressBarWidth / 100)
	if filled < 0 {
		filled = 0
	}
	if filled > materialProgressBarWidth {
		filled = materialProgressBarWidth
	}
	bar := strings.Repeat("=", filled) + strings.Repeat(" ", materialProgressBarWidth-filled)
	speed := float64(0)
	if !p.networkStarted.IsZero() {
		elapsed := now.Sub(p.networkStarted).Seconds()
		if elapsed > 0 {
			speed = float64(p.networkBytes) / elapsed
		}
	}
	line := fmt.Sprintf("Material [%s] %6.2f%%  %s/%s  %s/s  %d/%d files",
		bar,
		percentage,
		formatBytes(p.completedBytes),
		formatBytes(p.totalBytes),
		formatBytes(int64(speed)),
		p.completedFiles,
		p.totalFiles,
	)
	if p.dynamic {
		padding := ""
		if p.lastLineWidth > len(line) {
			padding = strings.Repeat(" ", p.lastLineWidth-len(line))
		}
		fmt.Fprintf(p.output, "\r%s%s", line, padding)
		p.lastLineWidth = len(line)
		return
	}
	if force {
		fmt.Fprintln(p.output, line)
	}
}

func (p *materialProgress) percentageLocked() float64 {
	if p.totalBytes > 0 {
		return float64(p.completedBytes) * 100 / float64(p.totalBytes)
	}
	if p.totalFiles > 0 {
		return float64(p.completedFiles) * 100 / float64(p.totalFiles)
	}
	return 100
}

func clampProgressBytes(value, total int64) int64 {
	if value < 0 {
		return 0
	}
	if total > 0 && value > total {
		return total
	}
	return value
}

func formatBytes(value int64) string {
	if value < 0 {
		value = 0
	}
	const unit = int64(1024)
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	divisor := float64(unit)
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	for _, suffix := range units {
		amount := float64(value) / divisor
		if amount < 1024 || suffix == units[len(units)-1] {
			return fmt.Sprintf("%.2f %s", amount, suffix)
		}
		divisor *= 1024
	}
	return fmt.Sprintf("%d B", value)
}

type byteProgressWriter struct {
	add func(int64)
}

func (w byteProgressWriter) Write(data []byte) (int, error) {
	if w.add != nil {
		w.add(int64(len(data)))
	}
	return len(data), nil
}
