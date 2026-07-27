package checker

import (
	"context"
	"io"
	"strings"
	"sync"

	"envinit/internal/spec"
)

type synchronizedWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *synchronizedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(p)
}

type Options struct {
	Bundle       spec.Bundle
	Records      []spec.MachineRecord
	Hosts        []string
	RunBandwidth bool
	RunRDMAPing  bool
	RunXCCL      bool
	// BandwidthModes selects the connection setup used by ib_write_bw. An empty
	// slice preserves the historical behavior and runs RDMA-CM only.
	BandwidthModes  []string
	DryRun          bool
	LiveOutput      bool
	Output          io.Writer
	Context         context.Context
	CommandRunner   func(spec.CheckConfig, Target, string) (string, error)
	FileCopier      func(spec.CheckConfig, Target, string, string) error
	bandwidthLive   *bandwidthLiveTracker
	bandwidthSpeeds bandwidthSpeedInventory
	checkTUI        *checkTUIController
	aborts          *checkAbortManager
	bandwidthMode   string
	bandwidthStage  string
	counterStage    string
}

const (
	BandwidthModeVerbs  = "verbs"
	BandwidthModeRDMACM = "rdma_cm"
)

func normalizedBandwidthModes(modes []string) []string {
	if len(modes) == 0 {
		return []string{BandwidthModeRDMACM}
	}
	seen := map[string]bool{}
	var out []string
	for _, mode := range modes {
		switch strings.ToLower(strings.TrimSpace(mode)) {
		case BandwidthModeVerbs:
			if !seen[BandwidthModeVerbs] {
				seen[BandwidthModeVerbs] = true
				out = append(out, BandwidthModeVerbs)
			}
		case BandwidthModeRDMACM, "rdma-cm", "cm":
			if !seen[BandwidthModeRDMACM] {
				seen[BandwidthModeRDMACM] = true
				out = append(out, BandwidthModeRDMACM)
			}
		}
	}
	if len(out) == 0 {
		return []string{BandwidthModeRDMACM}
	}
	return out
}

func bandwidthStageForMode(mode string) string {
	if mode == BandwidthModeVerbs {
		return "BW Verbs"
	}
	return "BW RDMA-CM"
}

func (o Options) currentBandwidthMode() string {
	if o.bandwidthMode == BandwidthModeVerbs {
		return BandwidthModeVerbs
	}
	return BandwidthModeRDMACM
}

func (o Options) currentBandwidthStage() string {
	if o.bandwidthStage != "" {
		return o.bandwidthStage
	}
	return checkTUIStageBandwidth
}

type DiscoverOptions struct {
	Bundle        spec.Bundle
	Records       []spec.MachineRecord
	Hosts         []string
	InventoryPath string
	Confirm       bool
	DryRun        bool
	Output        io.Writer
	CommandRunner func(spec.CheckConfig, Target, string) (string, error)
}

type Target struct {
	Input              string
	Name               string
	ExpectedHostname   string
	DiscoveredHostname string
	InventoryIdentity  string
	InventoryMatched   bool
	ExplicitIdentity   bool
	ControlAddress     string
	Address            string
	RDMA               []spec.RDMARecord
	Local              bool
}

type Result struct {
	Server           Target
	Client           Target
	ServerGroup      spec.CheckRDMAGroup
	ClientGroup      spec.CheckRDMAGroup
	ServerRDMAIndex  int
	ClientRDMAIndex  int
	ServerXP         string
	ClientXP         string
	ServerTopology   string
	ClientTopology   string
	Degraded         bool
	Port             int
	GBits            float64
	Passed           bool
	Output           string
	ClientOutput     string
	ServerOutput     string
	ClientError      string
	ServerError      string
	ThresholdMode    string
	BaselineGBits    float64
	ThresholdGBits   float64
	ThresholdKnown   bool
	ThresholdError   string
	ClientMaxMbps    int
	ServerMaxMbps    int
	ClientNowMbps    int
	ServerNowMbps    int
	ClientSpeedError string
	ServerSpeedError string
}

type bandwidthResultRow struct {
	Status     string
	Client     string
	Server     string
	ClientNIC  string
	ServerNIC  string
	ClientIP   string
	ServerIP   string
	ClientDev  string
	ServerDev  string
	Port       string
	ClientXP   string
	ServerXP   string
	ClientTopo string
	ServerTopo string
	Bandwidth  string
	Degraded   bool
	Failure    bool
}

type checkStream struct {
	ServerGroup     spec.CheckRDMAGroup
	ServerRDMAIndex int
	ClientGroup     spec.CheckRDMAGroup
	ClientRDMAIndex int
	ServerOffset    string
	ClientOffset    string
	Port            int
	ServerSpeed     bandwidthNICSpeed
	ClientSpeed     bandwidthNICSpeed
}

type rdmaPingItem struct {
	SourceIndex      int
	DestinationIndex int
	SourceName       string
	SourceIP         string
	DestinationName  string
	DestinationIP    string
}

type rdmaPingResultRow struct {
	Status         string
	Source         string
	Destination    string
	SourceNIC      string
	DestinationNIC string
	SourceIP       string
	DestinationIP  string
	Payload        string
	Result         string
	Failure        bool
}

type nicCounterSnapshot struct {
	Interfaces map[string]map[string]int64
}

type rdmaDeviceCounterSnapshot struct {
	Devices map[string]map[string]map[string]int64
}

type resolvedRDMAGroups map[string][]spec.CheckRDMAGroup

type nicCounterRow struct {
	Status  string
	Node    string
	Iface   string
	Counter string
	Before  int64
	After   int64
	Delta   int64
	Failure bool
}

type rdmaDeviceCounterRow struct {
	Status  string
	Node    string
	Device  string
	Port    string
	Counter string
	Before  int64
	After   int64
	Delta   int64
	Failure bool
}

type xcclTargetPlan struct {
	Target          Target
	XPUCount        int
	XPUOrder        []int
	RDMANICs        []string
	RDMANICOrder    []string
	RDMADeviceOrder []string
	RDMALinkOrder   []string
	RDMARailOrder   []string
	Mapping         []string
	SocketInterface string
}

type xcclPerformanceRow struct {
	SizeBytes int64
	Count     int64
	DataType  string
	Operation string
	Mode      string
	TimeUS    float64
	AlgGBs    float64
	BusGBs    float64
}
