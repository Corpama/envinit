package checker

import (
	"fmt"
	"strings"
)

func shellJoin(args []string) string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func sanitizeName(value string) string {
	value = strings.TrimSpace(value)
	var b strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "rdma"
	}
	return b.String()
}

func maxInt(values ...int) int {
	max := 0
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return max
}

func streamLabel(stream checkStream) string {
	return resultLabel(Result{ClientGroup: stream.ClientGroup, ServerGroup: stream.ServerGroup, Port: stream.Port})
}

func resultLabel(result Result) string {
	label := result.ClientGroup.IBDevice
	if result.ServerGroup.IBDevice != "" && result.ServerGroup.IBDevice != result.ClientGroup.IBDevice {
		label = result.ClientGroup.IBDevice + "->" + result.ServerGroup.IBDevice
	}
	if result.Port > 0 {
		return fmt.Sprintf("%s port=%d", label, result.Port)
	}
	return label
}
