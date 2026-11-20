//go:build linux

package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const probeRecoveryFile = "/var/lib/envinit/rdma-probe.json"

type probeRecovery struct {
	Version     int    `json:"version"`
	Interface   string `json:"interface"`
	MAC         string `json:"mac"`
	RestoreDown bool   `json:"restore_down"`
}

func (a *App) recoverInterruptedProbe() error {
	path := a.targetPath(probeRecoveryFile)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var state probeRecovery
	if err := json.Unmarshal(raw, &state); err != nil {
		return fmt.Errorf("needs attention: invalid RDMA probe recovery state %s: %w", path, err)
	}
	if state.Version != 1 || state.Interface == "" || state.MAC == "" {
		return fmt.Errorf("needs attention: invalid RDMA probe recovery identity in %s", path)
	}
	iface, err := net.InterfaceByName(state.Interface)
	if err != nil {
		return fmt.Errorf("needs attention: cannot recover probe interface %s: %w", state.Interface, err)
	}
	if !strings.EqualFold(iface.HardwareAddr.String(), state.MAC) {
		return fmt.Errorf("needs attention: probe recovery MAC mismatch for %s", state.Interface)
	}
	if state.RestoreDown && iface.Flags&net.FlagUp != 0 {
		showCtx, cancelShow := context.WithTimeout(context.Background(), probeCommandTimeout)
		out, err := exec.CommandContext(showCtx, "ip", "-j", "address", "show", "dev", state.Interface).Output()
		cancelShow()
		if err != nil {
			return err
		}
		var links []probeLink
		if err := json.Unmarshal(out, &links); err != nil {
			return err
		}
		for _, l := range links {
			if l.Master != "" {
				return fmt.Errorf("needs attention: %s changed after interrupted probe; refusing to bring it down", state.Interface)
			}
			for _, address := range l.Addresses {
				ip := net.ParseIP(address.Address)
				if ip != nil && !ip.IsLinkLocalUnicast() {
					return fmt.Errorf("needs attention: %s gained address %s after interrupted probe; refusing to bring it down", state.Interface, address.Address)
				}
			}
		}
		downCtx, cancelDown := context.WithTimeout(context.Background(), probeCommandTimeout)
		out, err = exec.CommandContext(downCtx, "ip", "link", "set", "dev", state.Interface, "down").CombinedOutput()
		cancelDown()
		if err != nil {
			return fmt.Errorf("recover probe link %s: %w: %s", state.Interface, err, out)
		}
	}
	return os.Remove(path)
}

func (a *App) prepareProbeLink(name, mac string, wasUp bool) (func() error, error) {
	if wasUp {
		return func() error { return nil }, nil
	}
	state := probeRecovery{Version: 1, Interface: name, MAC: mac, RestoreDown: true}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return nil, err
	}
	path := a.targetPath(probeRecoveryFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := atomicWriteFile(path, append(data, '\n'), 0o600); err != nil {
		return nil, err
	}
	upCtx, cancelUp := context.WithTimeout(context.Background(), probeCommandTimeout)
	out, err := exec.CommandContext(upCtx, "ip", "link", "set", "dev", name, "up").CombinedOutput()
	cancelUp()
	if err != nil {
		return nil, fmt.Errorf("bring probe link %s up: %w: %s", name, err, out)
	}
	return func() error {
		iface, err := net.InterfaceByName(name)
		if err != nil {
			return err
		}
		if !strings.EqualFold(iface.HardwareAddr.String(), mac) {
			return fmt.Errorf("probe cleanup MAC mismatch for %s", name)
		}
		downCtx, cancelDown := context.WithTimeout(context.Background(), probeCommandTimeout)
		out, err := exec.CommandContext(downCtx, "ip", "link", "set", "dev", name, "down").CombinedOutput()
		cancelDown()
		if err != nil {
			return fmt.Errorf("restore probe link %s: %w: %s", name, err, out)
		}
		return os.Remove(path)
	}, nil
}

func waitProbeCarrier(ctx context.Context, name string, wait time.Duration) (bool, error) {
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		data, err := os.ReadFile(filepath.Join("/sys/class/net", name, "carrier"))
		if err == nil && strings.TrimSpace(string(data)) == "1" {
			return true, nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, ctx.Err()
		case <-timer.C:
		}
	}
	return false, nil
}
