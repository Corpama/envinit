package checker

import (
	"io"

	"envinit/internal/spec"
)

type Options struct {
	Bundle       spec.Bundle
	Records      []spec.MachineRecord
	Hosts        []string
	RunBandwidth bool
	RunRDMAPing  bool
	DryRun       bool
	Output       io.Writer
}

type Target struct {
	Input            string
	Name             string
	ExpectedHostname string
	Address          string
	RDMA             []spec.RDMARecord
	Local            bool
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
	Port            int
	GBits           float64
	Passed          bool
	Output          string
}

type bandwidthResultRow struct {
	Status     string
	Client     string
	Server     string
	ClientRDMA string
	ServerRDMA string
	ClientDev  string
	ServerDev  string
	Port       string
	ClientXP   string
	ServerXP   string
	Bandwidth  string
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
	DestinationIP    string
}

type rdmaPingResultRow struct {
	Status          string
	Source          string
	Destination     string
	SourceRDMA      string
	DestinationRDMA string
	SourceIface     string
	DestinationIP   string
	Payload         string
	Result          string
	Failure         bool
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
