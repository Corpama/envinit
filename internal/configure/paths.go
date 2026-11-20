package configure

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Paths struct{ Project, Directory, Bundle, Inventory, InventoryInput string }

func ResolvePaths(project, bundle, inventory string) (Paths, error) {
	var p Paths
	var err error
	if project != "" {
		p.Project, err = filepath.Abs(project)
		if err != nil {
			return p, err
		}
	}
	if bundle != "" {
		p.Bundle, err = filepath.Abs(bundle)
		if err != nil {
			return p, err
		}
		p.Directory = filepath.Dir(p.Bundle)
	}
	if inventory != "" {
		p.InventoryInput, err = filepath.Abs(inventory)
		if err != nil {
			return p, err
		}
		dir := filepath.Dir(p.InventoryInput)
		if p.Directory != "" && dir != p.Directory {
			return p, fmt.Errorf("bundle and inventory must share one planning directory; use --inventory-template to import an external file")
		}
		p.Directory = dir
		p.Inventory = p.InventoryInput
		if !strings.EqualFold(filepath.Ext(p.Inventory), ".csv") {
			p.Inventory = filepath.Join(dir, "inventory.csv")
		}
	}
	if p.Project != "" {
		dir := filepath.Join(p.Project, "planning")
		if p.Directory != "" && p.Directory != dir {
			return p, fmt.Errorf("--project conflicts with explicit planning paths")
		}
		p.Directory = dir
	}
	if p.Directory == "" {
		p.Directory, err = filepath.Abs("planning")
		if err != nil {
			return p, err
		}
	}
	if p.Project == "" {
		if filepath.Base(p.Directory) == "planning" {
			p.Project = filepath.Dir(p.Directory)
		} else {
			p.Project = p.Directory
		}
	}
	if p.Bundle == "" {
		p.Bundle = filepath.Join(p.Directory, "bundle.json")
	}
	if p.Inventory == "" {
		p.Inventory = filepath.Join(p.Directory, "inventory.csv")
	}
	if p.InventoryInput == "" {
		p.InventoryInput = p.Inventory
	}
	if p.Bundle == p.Inventory {
		return p, fmt.Errorf("bundle and inventory must be different files")
	}
	if p.Directory == filepath.Dir(p.Directory) {
		return p, fmt.Errorf("planning directory cannot be the filesystem root")
	}
	for _, path := range []string{p.Bundle, p.Inventory, p.InventoryInput} {
		if err := RejectSymlinks(path); err != nil {
			return p, err
		}
	}
	return p, nil
}

func RejectSymlinks(path string) error {
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path traverses symlink: %s", current)
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if filepath.Dir(current) == current {
			return nil
		}
	}
}

// CachedTemplates selects only the downloader's recorded profile/release.
func CachedTemplates(project string) (string, string, string, error) {
	statePath := filepath.Join(project, ".envinit-downloads", "project.json")
	if err := RejectSymlinks(statePath); err != nil {
		return "", "", "", err
	}
	data, err := os.ReadFile(statePath)
	if os.IsNotExist(err) {
		return "", "", "", nil
	}
	if err != nil {
		return "", "", "", err
	}
	var state struct {
		Schema           int
		Profile, Release string
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return "", "", "", err
	}
	for _, value := range []string{state.Profile, state.Release} {
		if value == "" || value == "." || value == ".." || strings.ContainsAny(value, `/\:`) {
			return "", "", "", fmt.Errorf("invalid downloader project identity")
		}
	}
	if state.Schema != 1 {
		return "", "", "", fmt.Errorf("unsupported downloader project schema %d", state.Schema)
	}
	dir := filepath.Join(project, ".envinit-downloads", "templates", state.Release, state.Profile)
	return filepath.Join(dir, "bundle.json"), filepath.Join(dir, "inventory.csv"), state.Release + " / " + state.Profile, nil
}

func transactionPrefix(dir string) string {
	hash := sha256.Sum256([]byte(dir))
	return filepath.Join(filepath.Dir(dir), fmt.Sprintf(".envinit-config-%x", hash[:8]))
}
