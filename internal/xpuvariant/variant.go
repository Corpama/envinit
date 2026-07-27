package xpuvariant

import (
	"bufio"
	"errors"
	"fmt"
	"strings"
)

const (
	PartNumberVC = "B00100300110112"
	PartNumberVD = "B00100300110312"
)

// ClassifyPartNumbers applies the same P800 VC/VD identification used by the
// XRE apply stage. A host containing mixed or unknown variants is rejected.
func ClassifyPartNumbers(output string) (string, []string, error) {
	partNumbers := make([]string, 0)
	var variant string
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.Contains(line, "XPU Part Number") {
			continue
		}
		_, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(value) == "" {
			return "", nil, fmt.Errorf("invalid XPU Part Number line: %q", line)
		}
		partNumber := strings.TrimSpace(value)
		currentVariant := ""
		switch partNumber {
		case PartNumberVC:
			currentVariant = "VC"
		case PartNumberVD:
			currentVariant = "VD"
		default:
			return "", nil, fmt.Errorf("unknown P800 XPU Part Number %q", partNumber)
		}
		if variant != "" && variant != currentVariant {
			return "", nil, fmt.Errorf("mixed P800 XPU variants detected: %s and %s", variant, currentVariant)
		}
		variant = currentVariant
		partNumbers = append(partNumbers, partNumber)
	}
	if err := scanner.Err(); err != nil {
		return "", nil, fmt.Errorf("parse xpu-smi output: %w", err)
	}
	if len(partNumbers) == 0 {
		return "", nil, errors.New("xpu-smi -q did not report any XPU Part Number")
	}
	return variant, partNumbers, nil
}
