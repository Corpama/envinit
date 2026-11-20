package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type projectPaths struct{ BundleTemplate, InventoryTemplate, PlanningDir, StateDir, OutputDir string }
type projectState struct {
	Schema  int    `json:"schema"`
	Profile string `json:"profile"`
	Release string `json:"release"`
	Status  string `json:"status"`
}

func prepareProject(output string, manifest releaseManifest, profile manifestProfile) (projectPaths, projectState, error) {
	var p projectPaths
	var state projectState
	root, err := filepath.Abs(output)
	if err != nil {
		return p, state, err
	}
	if err = os.MkdirAll(root, 0o755); err != nil {
		return p, state, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return p, state, err
	}
	version := valueOrDefault(manifest.Version, releaseVersion)
	for _, s := range []string{version, profile.ID} {
		if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "/\\:") {
			return p, state, fmt.Errorf("invalid project release/profile %q", s)
		}
	}
	p = projectPaths{OutputDir: root, StateDir: filepath.Join(root, ".envinit-downloads"), PlanningDir: filepath.Join(root, "planning")}
	cache := filepath.Join(p.StateDir, "templates", version, profile.ID)
	p.BundleTemplate = filepath.Join(cache, "bundle.json")
	p.InventoryTemplate = filepath.Join(cache, "inventory.csv")
	for _, dir := range []string{root, p.StateDir, p.PlanningDir, filepath.Dir(p.BundleTemplate)} {
		if err = rejectSymlinkParents(dir); err != nil {
			return p, state, err
		}
	}
	data, err := os.ReadFile(filepath.Join(p.StateDir, "project.json"))
	if err == nil {
		if err = json.Unmarshal(data, &state); err != nil {
			return p, state, err
		}
		if state.Schema != 1 || state.Profile != profile.ID || state.Release != version {
			return p, state, fmt.Errorf("directory belongs to %s / %s; requested %s / %s: use a new --output-dir (automatic profile/release migration is not supported)", state.Release, state.Profile, version, profile.ID)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return p, state, err
	} else {
		// Do not infer the identity of an old assembled delivery directory.
		for _, name := range []string{"data", "env_init"} {
			if _, err := os.Stat(filepath.Join(root, name)); err == nil {
				return p, state, fmt.Errorf("existing unmanaged delivery %s: use a new --output-dir and import its inventory", root)
			} else if !errors.Is(err, os.ErrNotExist) {
				return p, state, err
			}
		}
		state = projectState{Schema: 1, Profile: profile.ID, Release: version, Status: "preparing"}
		if err = writeProjectState(p, state); err != nil {
			return p, state, err
		}
	}
	return p, state, nil
}

func writeProjectState(p projectPaths, s projectState) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeProjectFile(filepath.Join(p.StateDir, "project.json"), append(data, '\n'))
}

func rejectSymlinkParents(path string) error {
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("project path must not traverse symlink: %s", current)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return nil
}

func downloadProjectTemplates(p projectPaths, profile manifestProfile, token string) error {
	for _, item := range []struct {
		asset manifestAsset
		name  string
	}{{profile.Bundle, "bundle.json"}, {profile.Inventory, "inventory.csv"}} {
		if item.asset.Path == "" {
			if item.name == "inventory.csv" {
				continue
			} // legacy manifests supplied only a bundle
			return fmt.Errorf("profile %s has no %s template", profile.ID, item.name)
		}
		item.asset.Name = item.name
		if err := downloadManifestAsset(filepath.Dir(p.BundleTemplate), item.asset, token); err != nil {
			return err
		}
	}
	return nil
}

func seedPlanningTemplates(p projectPaths) error {
	for _, pair := range [][2]string{{p.BundleTemplate, "bundle.json"}, {p.InventoryTemplate, "inventory.csv"}} {
		target := filepath.Join(p.PlanningDir, pair[1])
		if _, err := os.Stat(target); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		data, err := os.ReadFile(pair[0])
		if errors.Is(err, os.ErrNotExist) && pair[1] == "inventory.csv" {
			continue
		}
		if err != nil {
			return err
		}
		if err = writeProjectFile(target, data); err != nil {
			return err
		}
	}
	return nil
}

func protectedProjectPath(path string) bool {
	path = filepath.ToSlash(filepath.Clean(path))
	if path == ".envinit-project.lock" || strings.HasPrefix(path, ".planning-stage-") {
		return true
	}
	for _, prefix := range []string{"planning", ".envinit-downloads"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func writeProjectFile(path string, data []byte) error {
	if err := rejectSymlinkParents(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".envinit-write-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
