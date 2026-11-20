package configure

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

func (t *Template) Report() string {
	var out strings.Builder
	fmt.Fprintf(&out, "目标平台（只读）: %s / %s / %s\n机器行: %d\n", t.Bundle.Platform.OSFamily, t.Bundle.Platform.PackageManager, t.Bundle.Platform.NetworkBackend, len(t.Table.Rows))
	for _, group := range []struct {
		label string
		items []string
	}{{"提示", t.Info}, {"需 review", t.Review}, {"待填写/确认", t.Pending}} {
		fmt.Fprintf(&out, "\n%s (%d):\n", group.label, len(group.items))
		for _, item := range group.items {
			fmt.Fprintf(&out, "  - %s\n", item)
		}
	}
	fmt.Fprintln(&out, "\n可通过 config 参数表或外部编辑器补齐 inventory；这是配置模板，补齐规划后再用于部署。")
	return out.String()
}

// Diff renders complete before/after lines; JSON secret-bearing fields are
// redacted in both sides. Data written to disk is never redacted.
func Diff(name string, before, after []byte, isJSON bool) string {
	if bytes.Equal(before, after) {
		return name + ": 无变化\n"
	}
	if isJSON {
		before = redactJSON(before)
		after = redactJSON(after)
	}
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s (原内容)\n+++ %s (拟保存)\n", name, name)
	for _, side := range []struct {
		prefix string
		data   []byte
	}{{"-", before}, {"+", after}} {
		for _, line := range strings.Split(strings.TrimSuffix(string(side.data), "\n"), "\n") {
			if line != "" {
				fmt.Fprintln(&out, side.prefix+line)
			}
		}
	}
	return out.String()
}

func redactJSON(data []byte) []byte {
	if len(data) == 0 {
		return nil
	}
	var value any
	if json.Unmarshal(data, &value) != nil {
		return []byte("[unparseable original JSON; content hidden]")
	}
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for key, item := range v {
				lower := strings.ToLower(key)
				if strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "token") || lower == "environment" || lower == "options" || lower == "command" {
					v[key] = "[redacted]"
				} else {
					walk(item)
				}
			}
		case []any:
			for _, item := range v {
				walk(item)
			}
		}
	}
	walk(value)
	result, _ := json.MarshalIndent(value, "", "  ")
	return result
}
