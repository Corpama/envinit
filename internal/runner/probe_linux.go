//go:build linux

package runner

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Raw Ethernet probes never install addresses/routes or move the physical NIC.
// Thus cancellation, SIGKILL and power loss leave no persistent probe objects.
func (a *App) probeNICBindings(ctx context.Context, bindings []interfaceBinding) ([]interfaceBinding, error) {
	if a.DryRun || (a.Root != "" && a.Root != "/") || !a.Bundle.RDMAConfigureIPRoute() || !a.Bundle.Defaults.RDMAGatewayProbe.IsEnabled() {
		return bindings, nil
	}
	var slots []int
	used := map[string]bool{}
	for i, b := range bindings {
		if b.Kind == "rdma" && b.Confidence != "exact" {
			slots = append(slots, i)
		} else {
			used[strings.ToLower(b.MAC)] = true
		}
	}
	if len(slots) == 0 {
		return bindings, nil
	}
	cmd := exec.CommandContext(ctx, "ip", "-j", "address", "show")
	raw, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("probe command 'ip -j address show' exceeded %s and was terminated: %w", probeCommandTimeout, err)
		}
		return nil, fmt.Errorf("cannot verify probe isolation: %w", err)
	}
	var links []probeLink
	if err := json.Unmarshal(raw, &links); err != nil {
		return nil, err
	}
	devices, err := a.discoverNetDevices()
	if err != nil {
		return nil, err
	}
	physical := map[string]netDevice{}
	for _, d := range devices {
		if d.HasInfiniband || d.Driver == "mlx5_core" {
			physical[d.Name] = d
		}
	}
	// iproute reports the runtime address, which can be the bond MAC on every
	// slave. Use the stable physical MAC for candidate identity and binding.
	for idx := range links {
		if device, ok := physical[links[idx].Name]; ok {
			links[idx].MAC = device.MAC
		}
	}
	slots = resolveExistingProbeBindings(bindings, slots, links, physical, used)
	if len(slots) == 0 {
		return bindings, nil
	}
	routeCmd := exec.CommandContext(ctx, "ip", "-j", "route", "show", "table", "all")
	routeRaw, err := routeCmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("probe command 'ip -j route show table all' exceeded %s and was terminated: %w", probeCommandTimeout, err)
		}
		return nil, fmt.Errorf("cannot inspect existing routes before probe: %w", err)
	}
	var routes []struct {
		Dev string `json:"dev"`
		Dst string `json:"dst"`
	}
	if err := json.Unmarshal(routeRaw, &routes); err != nil {
		return nil, err
	}

	var allCandidates []probeLink
	for _, l := range links {
		if _, ok := physical[l.Name]; !ok || used[strings.ToLower(l.MAC)] {
			continue
		}
		// Keep every unused physical NIC as a manual-review candidate. A route,
		// existing address, or enslaved state prevents a safe active probe, but
		// does not make the NIC invalid for the eventual user-confirmed binding.
		allCandidates = append(allCandidates, l)
	}
	allCandidates = hardwareOrderProbeCandidates(allCandidates, physical)
	var candidates []probeLink
	var skipped []string
	for _, l := range allCandidates {
		reason := ""
		for _, r := range routes {
			if r.Dev == l.Name {
				reason = "existing route"
			}
		}
		if l.Master != "" {
			reason = "enslaved interface"
		}
		for _, addr := range l.Addresses {
			ip := net.ParseIP(addr.Address)
			if ip != nil && !ip.IsLinkLocalUnicast() {
				reason = "existing address"
			}
		}
		if _, err := os.Stat(filepath.Join("/sys/class/net", l.Name, "device")); err != nil {
			reason = "not a physical device"
		}
		up := false
		for _, flag := range l.Flags {
			if flag == "UP" {
				up = true
			}
		}
		l.WasUp = up
		if reason != "" {
			a.logf("probe skip %s: %s", l.Name, reason)
			skipped = append(skipped, fmt.Sprintf("%s: skipped (%s)", l.Name, reason))
			continue
		}
		candidates = append(candidates, l)
	}
	p := a.Bundle.Defaults.RDMAGatewayProbe.Effective()
	probeEvidenceIncomplete := false
	edges := make([][]bool, len(slots))
	for row, idx := range slots {
		b := bindings[idx]
		ipText, _, _ := strings.Cut(b.Address, "/")
		source, gw := net.ParseIP(ipText).To4(), net.ParseIP(b.Gateway).To4()
		if source == nil || gw == nil {
			return nil, fmt.Errorf("probe %s requires a valid planned IPv4 and gateway", b.Name)
		}
		edges[row] = make([]bool, len(candidates))
		bindings[idx].ProbeDetails = append([]string{fmt.Sprintf("gateway %s (%s)", b.Gateway, b.GatewaySource)}, skipped...)
		for col, l := range candidates {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			cleanup, err := a.prepareProbeLink(l.Name, l.MAC, l.WasUp)
			if err != nil {
				return nil, fmt.Errorf("probe blocked on %s: %w", l.Name, err)
			}
			carrier, carrierErr := waitProbeCarrier(ctx, l.Name, time.Duration(p.LinkWaitSeconds)*time.Second)
			if carrierErr != nil {
				cleanupErr := cleanup()
				if cleanupErr != nil {
					return nil, fmt.Errorf("probe cancelled on %s and cleanup failed: %v: %w", l.Name, cleanupErr, carrierErr)
				}
				return nil, carrierErr
			}
			if !carrier {
				probeEvidenceIncomplete = true
				cleanupErr := cleanup()
				bindings[idx].ProbeDetails = append(bindings[idx].ProbeDetails, fmt.Sprintf("%s: link not ready", l.Name))
				a.logf("probe result %s on %s: link not ready", b.Name, l.Name)
				if cleanupErr != nil {
					return nil, fmt.Errorf("probe cleanup blocked on %s: %w", l.Name, cleanupErr)
				}
				continue
			}
			a.logf("probe %s (%s -> %s) on %s", b.Name, ipText, b.Gateway, l.Name)
			arp, icmp, err := probeEthernet(ctx, l.Name, source, gw, p.Attempts, time.Duration(p.TimeoutSeconds)*time.Second)
			cleanupErr := cleanup()
			if err != nil {
				return nil, fmt.Errorf("probe blocked on %s: %w", l.Name, err)
			}
			if cleanupErr != nil {
				return nil, fmt.Errorf("probe cleanup blocked on %s: %w", l.Name, cleanupErr)
			}
			edges[row][col] = arp && icmp
			if arp != icmp {
				probeEvidenceIncomplete = true
			}
			bindings[idx].ProbeDetails = append(bindings[idx].ProbeDetails, fmt.Sprintf("%s: ARP=%t ICMP=%t", l.Name, arp, icmp))
			a.logf("probe result %s on %s: ARP=%t ICMP=%t", b.Name, l.Name, arp, icmp)
		}
	}
	assignment, unique := uniqueProbeMapping(edges)
	// A unique matching over only the actively probeable subset is not enough
	// when other physical NICs were skipped. Those ports may be equally valid
	// owners, so require the complete physical candidate set before calling a
	// connectivity result strong.
	unique = unique && len(allCandidates) == len(candidates)
	if unique {
		for row, idx := range slots {
			l := candidates[assignment[row]]
			bindings[idx].MAC = l.MAC
			bindings[idx].CurrentName = l.Name
			bindings[idx].Reason = "unique ARP+ICMP mapping (connectivity only)"
			bindings[idx].Confidence = "strong"
			bindings[idx].NeedsReview = false
		}
	} else {
		applyHardwareOrderProbeFallback(bindings, slots, allCandidates)
		warning := probeFallbackWarning(edges, len(allCandidates), len(candidates), probeEvidenceIncomplete)
		for _, idx := range slots {
			bindings[idx].ProbeWarning = warning
			bindings[idx].ProbeDetails = append(bindings[idx].ProbeDetails,
				"connectivity did not prove a unique port; hardware order is a review candidate only")
		}
	}
	return bindings, nil
}

func htons(v uint16) uint16 { return v<<8 | v>>8 }
func checksum(b []byte) uint16 {
	var s uint32
	for len(b) > 1 {
		s += uint32(binary.BigEndian.Uint16(b))
		b = b[2:]
	}
	if len(b) > 0 {
		s += uint32(b[0]) << 8
	}
	for s>>16 != 0 {
		s = (s & 65535) + (s >> 16)
	}
	return ^uint16(s)
}

func probeEthernet(ctx context.Context, name string, source, gw net.IP, attempts int, timeout time.Duration) (bool, bool, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return false, false, err
	}
	if len(iface.HardwareAddr) != 6 {
		return false, false, errors.New("not an Ethernet MAC")
	}
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, int(htons(unix.ETH_P_ALL)))
	if err != nil {
		return false, false, err
	}
	defer unix.Close(fd)
	if err := unix.Bind(fd, &unix.SockaddrLinklayer{Ifindex: iface.Index, Protocol: htons(unix.ETH_P_ALL)}); err != nil {
		return false, false, err
	}
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Usec: 100000}); err != nil {
		return false, false, err
	}
	send := func(frame []byte) error {
		return unix.Sendto(fd, frame, 0, &unix.SockaddrLinklayer{Ifindex: iface.Index, Protocol: htons(binary.BigEndian.Uint16(frame[12:14])), Halen: 6})
	}
	receive := func(frame []byte, match func([]byte) bool) (bool, error) {
		for i := 0; i < attempts; i++ {
			if err := ctx.Err(); err != nil {
				return false, err
			}
			if err := send(frame); err != nil {
				return false, err
			}
			deadline := time.Now().Add(timeout / time.Duration(attempts))
			buf := make([]byte, 2048)
			for time.Now().Before(deadline) {
				if err := ctx.Err(); err != nil {
					return false, err
				}
				n, from, err := unix.Recvfrom(fd, buf, 0)
				if err == unix.EAGAIN || err == unix.EINTR {
					continue
				}
				if err != nil {
					return false, err
				}
				ll, ok := from.(*unix.SockaddrLinklayer)
				if !ok || ll.Ifindex != iface.Index || ll.Pkttype == unix.PACKET_OUTGOING {
					continue
				}
				if match(buf[:n]) {
					return true, nil
				}
			}
		}
		return false, nil
	}
	arpFrame := func(sender, target net.IP) []byte {
		b := make([]byte, 42)
		for i := 0; i < 6; i++ {
			b[i] = 255
		}
		copy(b[6:12], iface.HardwareAddr)
		binary.BigEndian.PutUint16(b[12:14], 0x806)
		binary.BigEndian.PutUint16(b[14:16], 1)
		binary.BigEndian.PutUint16(b[16:18], 0x800)
		b[18] = 6
		b[19] = 4
		binary.BigEndian.PutUint16(b[20:22], 1)
		copy(b[22:28], iface.HardwareAddr)
		copy(b[28:32], sender)
		copy(b[38:42], target)
		return b
	}
	arpReply := func(targetMAC []byte, sender, target net.IP) []byte {
		b := make([]byte, 42)
		copy(b[0:6], targetMAC)
		copy(b[6:12], iface.HardwareAddr)
		binary.BigEndian.PutUint16(b[12:14], 0x806)
		binary.BigEndian.PutUint16(b[14:16], 1)
		binary.BigEndian.PutUint16(b[16:18], 0x800)
		b[18] = 6
		b[19] = 4
		binary.BigEndian.PutUint16(b[20:22], 2)
		copy(b[22:28], iface.HardwareAddr)
		copy(b[28:32], sender)
		copy(b[32:38], targetMAC)
		copy(b[38:42], target)
		return b
	}
	validARP := func(b []byte) bool {
		return len(b) >= 42 && binary.BigEndian.Uint16(b[12:14]) == 0x806 && binary.BigEndian.Uint16(b[14:16]) == 1 && binary.BigEndian.Uint16(b[16:18]) == 0x800 && b[18] == 6 && b[19] == 4 && !bytes.Equal(b[22:28], iface.HardwareAddr)
	}
	conflict, err := receive(arpFrame(net.IPv4zero.To4(), source), func(b []byte) bool {
		return validARP(b) && (net.IP(b[28:32]).Equal(source) || (net.IP(b[28:32]).IsUnspecified() && net.IP(b[38:42]).Equal(source)))
	})
	if err != nil {
		return false, false, err
	}
	if conflict {
		return false, false, fmt.Errorf("address conflict for %s", source)
	}
	var gatewayMAC []byte
	arp, err := receive(arpFrame(source, gw), func(b []byte) bool {
		if validARP(b) && binary.BigEndian.Uint16(b[20:22]) == 2 && net.IP(b[28:32]).Equal(gw) && net.IP(b[38:42]).Equal(source) {
			gatewayMAC = append([]byte(nil), b[22:28]...)
			return true
		}
		return false
	})
	if err != nil || !arp {
		return arp, false, err
	}
	packet := make([]byte, 14+20+8+16)
	copy(packet[:6], gatewayMAC)
	copy(packet[6:12], iface.HardwareAddr)
	binary.BigEndian.PutUint16(packet[12:14], 0x800)
	ip := packet[14:34]
	ip[0] = 0x45
	binary.BigEndian.PutUint16(ip[2:4], uint16(len(packet)-14))
	ip[8] = 64
	ip[9] = 1
	copy(ip[12:16], source)
	copy(ip[16:20], gw)
	binary.BigEndian.PutUint16(ip[10:12], checksum(ip))
	icmp := packet[34:]
	icmp[0] = 8
	if _, err := rand.Read(icmp[4:]); err != nil {
		return false, false, err
	}
	binary.BigEndian.PutUint16(icmp[2:4], checksum(icmp))
	var arpReplyErr error
	ok, err := receive(packet, func(b []byte) bool {
		// Some gateways actively resolve the planned source before returning
		// ICMP even after seeing our ARP request. Reply on this bound raw socket
		// without installing the address in the host network stack.
		if validARP(b) && binary.BigEndian.Uint16(b[20:22]) == 1 && net.IP(b[28:32]).Equal(gw) && net.IP(b[38:42]).Equal(source) {
			if sendErr := send(arpReply(b[22:28], source, gw)); sendErr != nil {
				arpReplyErr = sendErr
			}
			return false
		}
		if len(b) < 58 || binary.BigEndian.Uint16(b[12:14]) != 0x800 {
			return false
		}
		header := int(b[14]&15) * 4
		if b[14]>>4 != 4 || header < 20 || len(b) < 14+header+len(icmp) || b[23] != 1 || !net.IP(b[26:30]).Equal(gw) || !net.IP(b[30:34]).Equal(source) {
			return false
		}
		r := b[14+header : 14+header+len(icmp)]
		return r[0] == 0 && r[1] == 0 && checksum(r) == 0 && string(r[4:]) == string(icmp[4:])
	})
	if arpReplyErr != nil {
		return true, false, arpReplyErr
	}
	return true, ok, err
}
