package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const mstSelectionFile = "/var/lib/envinit/mst-devices.json"

type mstDevice struct {
	Path string `json:"mst"`
	PCI  string `json:"pci,omitempty"`
}

type mstSelection struct {
	Devices []mstDevice `json:"mlxconfig_devices"`
}

func (a *App) mlxconfigDevices() ([]string, error) {
	if glob := strings.TrimSpace(a.Bundle.MlxConfig.DeviceGlob); glob != "" {
		return a.mlxconfigDevicesFromGlob(glob)
	}
	if devices, err := a.loadPersistedMSTSelection(); err == nil && len(devices) > 0 {
		a.logf("use persisted MST mlxconfig devices from %s: %s", mstSelectionFile, strings.Join(devices, ", "))
		return devices, nil
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	candidates, err := a.discoverMSTDevices()
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, errors.New("no MST pciconf devices discovered under /dev/mst; run mst start and verify Mellanox devices are present")
	}
	selected, err := a.confirmMSTDevices(candidates)
	if err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		return nil, errors.New("no MST devices selected for mlxconfig")
	}
	if err := a.saveMSTSelection(selected); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(selected))
	for _, device := range selected {
		paths = append(paths, device.Path)
	}
	return paths, nil
}

func (a *App) mlxconfigDevicesFromGlob(glob string) ([]string, error) {
	devices, err := filepath.Glob(glob)
	if err != nil {
		return nil, fmt.Errorf("glob mlxconfig devices: %w", err)
	}
	filtered := filterMSTPciconfDevices(devices)
	if len(filtered) == 0 {
		return nil, fmt.Errorf("no mlxconfig devices matched %s", glob)
	}
	return filtered, nil
}

func (a *App) discoverMSTDevices() ([]mstDevice, error) {
	matches, err := filepath.Glob(a.targetPath("/dev/mst/*_pciconf*"))
	if err != nil {
		return nil, fmt.Errorf("scan MST devices: %w", err)
	}
	paths := make([]string, 0, len(matches))
	for _, match := range filterMSTPciconfDevices(matches) {
		if a.Root != "" && a.Root != "/" {
			rel, err := filepath.Rel(a.Root, match)
			if err == nil {
				match = "/" + rel
			}
		}
		paths = append(paths, match)
	}
	sort.Strings(paths)
	devices := make([]mstDevice, 0, len(paths))
	for _, path := range paths {
		devices = append(devices, mstDevice{
			Path: path,
			PCI:  mstPCIFromDeviceName(path),
		})
	}
	return devices, nil
}

func filterMSTPciconfDevices(paths []string) []string {
	out := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		base := filepath.Base(path)
		if !strings.Contains(base, "_pciconf") {
			continue
		}
		if strings.HasSuffix(base, ".1") || strings.HasSuffix(base, ".2") {
			continue
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

func mstPCIFromDeviceName(path string) string {
	base := filepath.Base(path)
	if idx := strings.LastIndex(base, "_pciconf"); idx >= 0 {
		value := strings.TrimPrefix(base[idx+len("_pciconf"):], "_")
		if strings.Count(value, ":") >= 1 || strings.Count(value, ".") >= 1 {
			return value
		}
	}
	return ""
}

func (a *App) loadPersistedMSTSelection() ([]string, error) {
	data, err := os.ReadFile(a.targetPath(mstSelectionFile))
	if err != nil {
		return nil, err
	}
	var selection mstSelection
	if err := json.Unmarshal(data, &selection); err != nil {
		return nil, fmt.Errorf("parse %s: %w", mstSelectionFile, err)
	}
	paths := make([]string, 0, len(selection.Devices))
	for _, device := range selection.Devices {
		path := strings.TrimSpace(device.Path)
		if path == "" {
			continue
		}
		if _, err := os.Stat(a.targetPath(path)); err != nil {
			return nil, fmt.Errorf("persisted MST device %s is not available: %w", path, err)
		}
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("%s does not contain any usable mlxconfig devices", mstSelectionFile)
	}
	return paths, nil
}

func (a *App) saveMSTSelection(devices []mstDevice) error {
	if a.DryRun {
		a.logf("dry-run: would write %s with %d MST device(s)", mstSelectionFile, len(devices))
		return nil
	}
	selection := mstSelection{Devices: devices}
	data, err := json.MarshalIndent(selection, "", "  ")
	if err != nil {
		return fmt.Errorf("encode MST selection: %w", err)
	}
	return a.writeManagedFile(mstSelectionFile, string(data)+"\n", 0o644)
}

func (a *App) confirmMSTDevices(candidates []mstDevice) ([]mstDevice, error) {
	if a.DryRun {
		a.logf("dry-run: would review %d discovered MST device(s) for mlxconfig", len(candidates))
		return candidates, nil
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		a.logf("skip interactive MST device review: /dev/tty is not available; using all discovered MST devices")
		return candidates, nil
	}
	defer tty.Close()
	return runMSTDeviceReview(tty, candidates)
}

type mstDeviceReview struct {
	Devices  []mstDevice
	Selected map[int]bool
	Index    int
	Message  string
}

func runMSTDeviceReview(tty *os.File, candidates []mstDevice) ([]mstDevice, error) {
	review := mstDeviceReview{
		Devices:  append([]mstDevice(nil), candidates...),
		Selected: map[int]bool{},
	}
	for idx := range candidates {
		review.Selected[idx] = true
	}
	if err := withRawTerminal(tty, func() error {
		for {
			renderMSTDeviceReview(tty, &review)
			key, err := readReviewKey(tty)
			if err != nil {
				return err
			}
			switch key {
			case "up":
				if review.Index > 0 {
					review.Index--
				}
			case "down":
				if review.Index < len(review.Devices)-1 {
					review.Index++
				}
			case "left", "right", "reset", "toggle":
				review.Selected[review.Index] = !review.Selected[review.Index]
				review.Message = ""
			case "accept":
				if len(selectedMSTDevices(&review)) == 0 {
					review.Message = "select at least one MST device"
					continue
				}
				renderNICBindingAccepted(tty)
				return nil
			case "abort":
				renderNICBindingAccepted(tty)
				return errors.New("MST device review aborted")
			}
		}
	}); err != nil {
		return nil, err
	}
	return selectedMSTDevices(&review), nil
}

func selectedMSTDevices(review *mstDeviceReview) []mstDevice {
	out := make([]mstDevice, 0, len(review.Devices))
	for idx, device := range review.Devices {
		if review.Selected[idx] {
			out = append(out, device)
		}
	}
	return out
}

func renderMSTDeviceReview(tty *os.File, review *mstDeviceReview) {
	fmt.Fprint(tty, "\033[2J\033[H")
	fmt.Fprintln(tty, "MST Device Review")
	fmt.Fprintln(tty)
	fmt.Fprintln(tty, "Select devices that should receive mlxconfig settings.")
	fmt.Fprintln(tty)
	fmt.Fprintln(tty, "    Use  MST Device                    PCI")
	fmt.Fprintln(tty, "    ---  ----------------------------  -------------")
	for idx, device := range review.Devices {
		cursor := "  "
		if idx == review.Index {
			cursor = "> "
		}
		checked := "[ ]"
		if review.Selected[idx] {
			checked = "[x]"
		}
		fmt.Fprintf(tty, "%s  %s  %-28s  %s\n", cursor, checked, device.Path, valueOrDash(device.PCI))
	}
	fmt.Fprintln(tty)
	fmt.Fprintln(tty, "Keys: Up/Down select | Space/Left/Right toggle | Enter accept | q abort")
	if review.Message != "" {
		fmt.Fprintln(tty)
		fmt.Fprintln(tty, review.Message)
	}
}
