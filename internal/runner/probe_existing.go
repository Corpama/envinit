package runner

import (
	"fmt"
	"net"
	"strings"
)

type probeAddress struct {
	Address string `json:"local"`
	Scope   string `json:"scope"`
}

type probeLink struct {
	Name      string         `json:"ifname"`
	MAC       string         `json:"address"`
	Master    string         `json:"master"`
	Flags     []string       `json:"flags"`
	Addresses []probeAddress `json:"addr_info"`
	WasUp     bool           `json:"-"`
}

// Resolve already-configured addresses before active probing. Existing address
// ownership is useful evidence, but it does not verify gateway connectivity, so
// even a unique physical owner remains reviewable. Never send probes using an
// address already on this host.
func resolveExistingProbeBindings(bindings []interfaceBinding, slots []int, links []probeLink, physical map[string]netDevice, used map[string]bool) []int {
	var pending []int
	for _, idx := range slots {
		b := &bindings[idx]
		ipText, _, _ := strings.Cut(b.Address, "/")
		source := net.ParseIP(ipText)
		var owners []probeLink
		if source != nil {
			for _, link := range links {
				for _, addr := range link.Addresses {
					if existing := net.ParseIP(addr.Address); existing != nil && existing.Equal(source) {
						owners = append(owners, link)
						break
					}
				}
			}
		}
		if len(owners) == 0 {
			pending = append(pending, idx)
			continue
		}
		b.MAC, b.CurrentName = "", ""
		b.NeedsReview = true
		b.Confidence = "ambiguous"
		b.Reason = "existing address ownership needs manual selection (connectivity unverified)"
		b.ProbeWarning = "RDMA gateway probe skipped because a planned address already exists on the host; connectivity is unverified, so review the current ownership"
		for _, owner := range owners {
			b.ProbeDetails = append(b.ProbeDetails, fmt.Sprintf("planned address %s already exists on %s; active probe skipped", ipText, owner.Name))
		}
		if len(owners) != 1 {
			continue
		}
		owner := owners[0]
		device, ok := physical[owner.Name]
		mac := ""
		if ok {
			mac = strings.ToLower(device.MAC)
		}
		if !ok || mac == "" || !strings.EqualFold(device.MAC, mac) || owner.Master != "" || used[mac] {
			continue
		}
		b.MAC, b.CurrentName = mac, owner.Name
		b.NeedsReview = true
		b.Reason = "existing address mapping; connectivity unverified"
		b.Confidence = "ambiguous"
		used[mac] = true
	}
	return pending
}
