package checker

import (
	"fmt"
	"strconv"
	"strings"
)

const bandwidthAutoThresholdRatio = 0.70

type bandwidthNICSpeed struct {
	Interface   string
	CurrentMbps int
	MaximumMbps int
	Error       string
}

type bandwidthSpeedInventory map[string]map[int]bandwidthNICSpeed

func collectBandwidthNICSpeeds(opts Options, targets []Target) bandwidthSpeedInventory {
	inventory := bandwidthSpeedInventory{}
	for _, target := range targets {
		byIndex := map[int]bandwidthNICSpeed{}
		for index, record := range target.RDMA {
			byIndex[index] = bandwidthNICSpeed{Interface: strings.TrimSpace(record.Name), Error: "speed probe returned no data"}
		}
		inventory[target.Name] = byIndex
		if len(target.RDMA) == 0 {
			continue
		}
		output, err := runCheckCommand(opts, target, bandwidthSpeedProbeCommand(target))
		if err != nil {
			for index, speed := range byIndex {
				speed.Error = compactTableCell(err.Error())
				byIndex[index] = speed
			}
			fmt.Fprintf(opts.Output, "WARN bandwidth speed probe %s: %v\n", target.Name, err)
			continue
		}
		for index, speed := range parseBandwidthSpeedProbe(output) {
			if _, ok := byIndex[index]; ok {
				byIndex[index] = speed
			}
		}
		for index, speed := range byIndex {
			if speed.MaximumMbps > 0 {
				speed.Error = ""
			} else if speed.Error == "" {
				speed.Error = "maximum supported speed is unavailable"
			}
			byIndex[index] = speed
			fmt.Fprintf(opts.Output, "INFO bandwidth speed: %s rdma%d iface=%s max=%s current=%s\n",
				target.Name, index+1, firstNonEmpty(speed.Interface, "-"), bandwidthSpeedLabel(speed.MaximumMbps), bandwidthSpeedLabel(speed.CurrentMbps))
		}
	}
	return inventory
}

func bandwidthSpeedProbeCommand(target Target) string {
	var commands []string
	for index, record := range target.RDMA {
		iface := strings.TrimSpace(record.Name)
		if iface == "" {
			commands = append(commands, fmt.Sprintf("printf 'SPEED|%d||0|0\\n'", index))
			continue
		}
		commands = append(commands, fmt.Sprintf(`n=%s; current=$(cat /sys/class/net/"$n"/speed 2>/dev/null || true); max=$(ethtool "$n" 2>/dev/null | awk '/Supported link modes:/{seen=1} seen{for(i=1;i<=NF;i++){if(match($i,/^[0-9]+base/)){v=substr($i,RSTART,RLENGTH-4)+0;if(v>m)m=v}}} /^Supported pause frame use:/{seen=0} END{print m+0}'); printf 'SPEED|%d|%%s|%%s|%%s\n' "$n" "$current" "$max"`, shellQuote(iface), index))
	}
	return strings.Join(commands, "; ")
}

func parseBandwidthSpeedProbe(output string) map[int]bandwidthNICSpeed {
	result := map[int]bandwidthNICSpeed{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "|")
		if len(fields) != 5 || fields[0] != "SPEED" {
			continue
		}
		index, err := strconv.Atoi(fields[1])
		if err != nil || index < 0 {
			continue
		}
		current, _ := positiveInt(fields[3])
		maximum, _ := positiveInt(fields[4])
		speed := bandwidthNICSpeed{Interface: strings.TrimSpace(fields[2]), CurrentMbps: current, MaximumMbps: maximum}
		if maximum <= 0 {
			speed.Error = "maximum supported speed is unavailable"
		}
		result[index] = speed
	}
	return result
}

func bandwidthSpeedLabel(mbps int) string {
	if mbps <= 0 {
		return "unknown"
	}
	if mbps%1000 == 0 {
		return fmt.Sprintf("%dG", mbps/1000)
	}
	return fmt.Sprintf("%.3fG", float64(mbps)/1000)
}

func bandwidthStreamsForTargets(opts Options, server, client Target, groups resolvedRDMAGroups) []checkStream {
	streams := bandwidthStreamsForGroups(opts.Bundle.Check.Bandwidth, groups[server.Name], groups[client.Name])
	for index := range streams {
		streams[index].ServerSpeed = opts.bandwidthSpeeds.speed(server.Name, streams[index].ServerRDMAIndex)
		streams[index].ClientSpeed = opts.bandwidthSpeeds.speed(client.Name, streams[index].ClientRDMAIndex)
	}
	return streams
}

func (inventory bandwidthSpeedInventory) speed(target string, index int) bandwidthNICSpeed {
	if speed, ok := inventory[target][index]; ok {
		return speed
	}
	return bandwidthNICSpeed{Error: "speed was not probed"}
}
