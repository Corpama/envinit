// Package configcli implements the offline planning editor shared by both CLIs.
package configcli

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"envinit/internal/configure"
	"envinit/internal/inventory"
	"envinit/internal/startup"

	"github.com/charmbracelet/x/term"
)

func Run(command string, args []string) error {
	fs := flag.NewFlagSet(command+" config", flag.ContinueOnError)
	project := fs.String("project", "", "Project directory (defaults to current directory/planning)")
	bundlePath := fs.String("bundle", "", "Bundle path in the planning directory")
	invPath := fs.String("inventory", "", "Inventory path; XLSX/TSV/TXT is imported to inventory.csv in the same directory")
	bundleTemplate := fs.String("bundle-template", "", "Bundle template used when missing, or with --replace")
	invTemplate := fs.String("inventory-template", "", "External inventory template/import (CSV/TSV/TXT/XLSX)")
	sheet := fs.String("sheet", "", "XLSX sheet (default: first)")
	checkOnly := fs.Bool("check-only", false, "Read-only template validation and normalized CSV suggestion")
	plain := fs.Bool("plain", false, "Use text prompts and preview")
	yes := fs.Bool("yes", false, "Confirm save without interactive review; validation still applies")
	replace := fs.Bool("replace", false, "Explicitly select supplied templates over existing planning; originals are backed up")
	save := fs.String("save", "both", "Save both, bundle, or inventory")
	recover := fs.Bool("recover", false, "Explicitly recover an interrupted planning transaction and exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected config arguments: %s", strings.Join(fs.Args(), " "))
	}
	if *save != "both" && *save != "bundle" && *save != "inventory" {
		return fmt.Errorf("--save must be both, bundle, or inventory")
	}
	if *recover && *checkOnly {
		return fmt.Errorf("--recover cannot be used with --check-only")
	}
	p, err := configure.ResolvePaths(*project, *bundlePath, *invPath)
	if err != nil {
		return err
	}
	if !*checkOnly {
		unlock, err := configure.Lock(p)
		if err != nil {
			return err
		}
		defer unlock()
	}
	pending, err := configure.PendingTransaction(p)
	if err != nil {
		return err
	}
	if *recover {
		if !pending {
			fmt.Fprintln(os.Stdout, "没有待恢复的事务。")
			return nil
		}
		if err := configure.Recover(p); err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, "已恢复旧规划；请重新运行 config 检查。")
		return nil
	}
	if pending {
		return fmt.Errorf("interrupted planning transaction; inspect backups and run config with the same paths and --recover")
	}
	var snapshot, cachedBundle, cachedInventory, profile string
	err = startup.Run(command, args,
		startup.Step{Label: "读取规划快照", Load: func() error { var e error; snapshot, e = configure.Snapshot(p.Directory); return e }},
		startup.Step{Label: "定位项目模板", Load: func() error {
			var e error
			cachedBundle, cachedInventory, profile, e = configure.CachedTemplates(p.Project)
			return e
		}},
	)
	if err != nil {
		return err
	}
	ui := &configUI{plain: *plain || !term.IsTerminal(os.Stdin.Fd()) || !term.IsTerminal(os.Stdout.Fd()), input: bufio.NewReader(os.Stdin), output: os.Stdout}
	interactive := !*yes && !*checkOnly
	choose := func(target, explicit, cached, kind string) (string, error) {
		_, err := os.Stat(target)
		exists := err == nil
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if exists && explicit != "" && !*replace && interactive {
			answer, err := ui.ask("Enter 沿用已有文件；输入 replace 使用模板", fmt.Sprintf("%s 已有: %s\n模板: %s\n替换保存时保留原文件备份。", kind, target, explicit))
			if err != nil {
				return "", err
			}
			if answer == "replace" {
				return explicit, nil
			}
			if answer != "" {
				return "", fmt.Errorf("expected Enter or replace")
			}
		}
		if exists && (!*replace || explicit == "") {
			return target, nil
		}
		if explicit != "" {
			return explicit, nil
		}
		if cached != "" {
			if _, err := os.Stat(cached); err == nil {
				return cached, nil
			} else if !os.IsNotExist(err) {
				return "", err
			}
		}
		if interactive {
			prompt := "请输入 " + kind + " 模板路径"
			if kind == "inventory" {
				prompt += "；Enter 创建空白表头模板"
			}
			path, err := ui.ask(prompt, "缺少 "+target)
			if err != nil {
				return "", err
			}
			if path != "" {
				return path, nil
			}
		}
		if kind == "inventory" {
			return "", nil
		}
		return "", fmt.Errorf("missing %s; supply --%s-template", target, kind)
	}
	bSource, err := choose(p.Bundle, *bundleTemplate, cachedBundle, "bundle")
	if err != nil {
		return err
	}
	// Explicit non-CSV --inventory is an import source. An existing CSV remains
	// authoritative unless replacement was explicitly selected.
	iExplicit := *invTemplate
	if iExplicit == "" && p.InventoryInput != p.Inventory {
		iExplicit = p.InventoryInput
	}
	iSource, err := choose(p.Inventory, iExplicit, cachedInventory, "inventory")
	if err != nil {
		return err
	}
	for _, source := range []string{bSource, iSource} {
		if source == "" {
			continue
		}
		if err := configure.RejectSymlinks(source); err != nil {
			return err
		}
	}
	bData, err := os.ReadFile(bSource)
	if err != nil {
		return err
	}
	var table *inventory.TemplateTable
	if iSource == "" {
		table, err = inventory.ParseTemplate([]byte("hostname,mgmt_mode,mgmt_target_name,mgmt_ip,mgmt_prefix,mgmt_gateway,mgmt_iface1,mgmt_mac1,mgmt_iface2,mgmt_mac2\n"))
		iSource = "空白表头模板（按需增加 rdmaN_* 列）"
	} else {
		table, err = inventory.LoadTemplate(iSource, *sheet)
	}
	if err != nil {
		return err
	}
	originalBundle, err := readConfigOptional(p.Bundle)
	if err != nil {
		return err
	}
	originalInventory, err := readConfigOptional(p.Inventory)
	if err != nil {
		return err
	}
	initialBundle := append([]byte{}, bData...)
	sources := fmt.Sprintf("项目: %s\n输出: %s\nBundle 来源: %s\nInventory 来源: %s\nProfile（只读）: %s\n", p.Project, p.Directory, bSource, iSource, profile)
	for {
		t, err := configure.PrepareTemplate(bData, table)
		if err != nil {
			return fmt.Errorf("阻断: %w", err)
		}
		if err := t.InspectMaterials(p.Project); err != nil {
			return err
		}
		if *checkOnly {
			fmt.Fprint(os.Stdout, sources, t.Report(), configure.Diff(p.Bundle, originalBundle, t.BundleJSON, true))
			fmt.Fprintln(os.Stdout, "\n规范化 CSV 建议（只读，未保存）:\n"+string(t.InventoryCSV))
			return nil
		}
		if interactive && !ui.plain {
			editedBundle, action, err := runPolicyTable(bData)
			if err != nil {
				return err
			}
			if action == "cancel" {
				fmt.Fprintln(os.Stdout, "已取消。")
				return nil
			}
			if len(editedBundle) > 0 {
				bData = editedBundle
			}
			editedTable, inventoryAction, err := runInventoryTable(bData, table)
			if err != nil {
				return err
			}
			if inventoryAction == "cancel" {
				fmt.Fprintln(os.Stdout, "已取消。")
				return nil
			}
			if editedTable != nil {
				table = editedTable
			}
			// Rebuild both the report and saved bytes from the edited pair.
			t, err = configure.PrepareTemplate(bData, table)
			if err != nil {
				return fmt.Errorf("编辑后的配置: %w", err)
			}
			if err := t.InspectMaterials(p.Project); err != nil {
				return err
			}
		}
		if interactive {
			answer, err := ui.ask("Enter 进入保存预览；输入 reset 恢复来源策略；q 取消", sources+"\n"+t.Report())
			if err != nil {
				return err
			}
			if answer == "q" {
				fmt.Fprintln(os.Stdout, "已取消。")
				return nil
			}
			if answer == "reset" {
				bData = append([]byte{}, initialBundle...)
				continue
			}
			if answer != "" {
				continue
			}
			answer, err = ui.ask("Enter 保存 "+*save+"；或输入 both / bundle / inventory；back 返回；q 取消", "选择保存目标。单文件保存会校验与另一份原文件的组合。")
			if err != nil {
				return err
			}
			if answer == "back" {
				continue
			}
			if answer == "q" {
				return nil
			}
			if answer != "" {
				if answer != "both" && answer != "bundle" && answer != "inventory" {
					continue
				}
				*save = answer
			}
		}
		finalBundle, finalInventory := t.BundleJSON, t.InventoryCSV
		if *save == "bundle" {
			for _, info := range t.Info {
				if strings.Contains(info, "<- legacy defaults:") {
					return fmt.Errorf("legacy bundle defaults must be migrated into inventory; select --save both to preserve planning semantics")
				}
			}
			finalInventory = originalInventory
		}
		if *save == "inventory" {
			finalBundle = originalBundle
		}
		if len(finalBundle) == 0 || len(finalInventory) == 0 {
			return fmt.Errorf("single-file save requires the other planning file to exist; select both")
		}
		finalTable, err := inventory.ParseTemplate(finalInventory)
		if err != nil {
			return err
		}
		final, err := configure.PrepareTemplate(finalBundle, finalTable)
		if err != nil {
			return fmt.Errorf("final saved combination: %w", err)
		}
		if err := final.InspectMaterials(p.Project); err != nil {
			return err
		}
		review := sources + "\n" + final.Report() + "\n备份：原目录作为独立备份保留；无变化不生成备份。\n"
		files := map[string][]byte{}
		if *save != "inventory" {
			files[filepath.Base(p.Bundle)] = finalBundle
			review += configure.Diff(p.Bundle, originalBundle, finalBundle, true)
		}
		if *save != "bundle" {
			files[filepath.Base(p.Inventory)] = finalInventory
			review += configure.Diff(p.Inventory, originalInventory, finalInventory, false)
		}
		if interactive {
			answer, err := ui.ask("Enter 确认保存；back 返回修改；q 取消", review)
			if err != nil {
				return err
			}
			if answer == "back" {
				continue
			}
			if answer == "q" {
				return nil
			}
			if answer != "" {
				continue
			}
		} else {
			fmt.Fprint(os.Stdout, review)
		}
		backup, err := configure.Save(p, files, snapshot)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "\n模板保存完成 (%s)\nBundle: %s\nInventory: %s\n", *save, p.Bundle, p.Inventory)
		if backup != "" {
			fmt.Fprintln(os.Stdout, "备份:", backup)
		}
		fmt.Fprint(os.Stdout, final.Report())
		return nil
	}
}

func readConfigOptional(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return data, err
}
