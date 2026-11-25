package main

import (
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strings"
)

var simpleCommandArg = regexp.MustCompile(`^[a-zA-Z0-9_./:-]+$`)

func printConfigHint(opts runOptions) {
	if opts.Stdout == nil {
		return
	}
	exe := os.Args[0]
	prefix := ""
	if runtime.GOOS == "windows" {
		// In PowerShell a quoted executable path needs the call operator.
		prefix = "& "
		if !strings.ContainsAny(exe, `/\:`) {
			if _, err := os.Stat(exe); err == nil {
				exe = `.\` + exe
			}
		}
	}
	fmt.Fprintf(opts.Stdout, "下载完成。请根据项目实际情况，在当前目录执行以下命令配置物料：\n%s%s config -project %s\n",
		prefix, quoteCommandArg(exe), quoteCommandArg(normalizedOutputDir(opts.OutputDir)))
}

func quoteCommandArg(arg string) string {
	if simpleCommandArg.MatchString(arg) {
		return arg
	}
	if runtime.GOOS == "windows" {
		return "'" + strings.ReplaceAll(arg, "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}
