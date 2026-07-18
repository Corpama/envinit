package checker

import (
	"io"

	"envinit/internal/spec"
)

type Options struct {
	Bundle        spec.Bundle
	Records       []spec.MachineRecord
	Hosts         []string
	RunBandwidth  bool
	RunRDMAPing   bool
	RunXCCL       bool
	DryRun        bool
	Output        io.Writer
	CommandRunner func(spec.CheckConfig, Target, string) (string, error)
	FileCopier    func(spec.CheckConfig, Target, string, string) error
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
	Server          Target
	Client          Target
	ServerGroup     spec.CheckRDMAGroup
	ClientGroup     spec.CheckRDMAGroup
	ServerRDMAIndex int
	ClientRDMAIndex int
	ServerXP        string
	ClientXP        string
	ServerTopology  string
	ClientTopology  string
	Degraded        bool
	Port            int
	GBits           float64
	Passed          bool
	Output          string
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
	RDMANICs        []string
	RDMANICOrder    []string
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
