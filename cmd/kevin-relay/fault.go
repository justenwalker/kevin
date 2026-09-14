package main

import (
	"context"
	"fmt"

	"github.com/justenwalker/kevin/protos/pb"
)

// faultConfig is one ApplyFault call's parameters, decoded off the proto
// request - kept as a plain struct, not the proto message itself, so
// buildFaultQdisc's signature doesn't depend on pb.
type faultConfig struct {
	Interface        string
	DelayMS          int32
	JitterMS         int32
	LossPercent      float64
	CorruptPercent   float64
	DuplicatePercent float64
	ReorderPercent   float64
}

// faultTarget is one applied fault: enough to know where and what was
// applied, so ClearFault can remove exactly what ApplyFault installed.
type faultTarget struct {
	netnsPath string
	iface     string
}

// ApplyFault implements [pb.RelayControlServer]. It installs (or
// replaces) the netem qdisc for req.Id's namespace/interface, and records
// it only once that succeeds - mirroring RegisterCapture's own
// record-on-success discipline.
func (p *relayProcess) ApplyFault(ctx context.Context, req *pb.ApplyFaultRequest) (*pb.ApplyFaultResponse, error) {
	cfg := faultConfig{
		Interface:        req.GetInterface(),
		DelayMS:          req.GetDelayMs(),
		JitterMS:         req.GetJitterMs(),
		LossPercent:      req.GetLossPercent(),
		CorruptPercent:   req.GetCorruptPercent(),
		DuplicatePercent: req.GetDuplicatePercent(),
		ReorderPercent:   req.GetReorderPercent(),
	}
	if err := applyFault(req.GetNetnsPath(), cfg); err != nil {
		return nil, fmt.Errorf("relay: apply fault for %q: %w", req.GetId(), err)
	}

	p.mu.Lock()
	if p.faults == nil {
		p.faults = make(map[string]faultTarget)
	}
	p.faults[req.GetId()] = faultTarget{netnsPath: req.GetNetnsPath(), iface: req.GetInterface()}
	p.mu.Unlock()

	log.Ctx(ctx).Debug("relay: applied fault", "id", req.GetId(), "netns_path", req.GetNetnsPath())
	return &pb.ApplyFaultResponse{}, nil
}

// ClearFault implements [pb.RelayControlServer]. It removes the fault
// req.Id names, if one is currently applied - a no-op, not an error, when
// it isn't (already cleared, never applied, or the namespace it targeted
// is already gone).
func (p *relayProcess) ClearFault(ctx context.Context, req *pb.ClearFaultRequest) (*pb.ClearFaultResponse, error) {
	p.mu.Lock()
	target, ok := p.faults[req.GetId()]
	delete(p.faults, req.GetId())
	p.mu.Unlock()
	if !ok {
		return &pb.ClearFaultResponse{}, nil
	}

	if err := clearFault(target.netnsPath, target.iface); err != nil {
		log.Ctx(ctx).Debug("relay: clear fault failed, netns likely gone", "error", err, "id", req.GetId())
	}
	return &pb.ClearFaultResponse{}, nil
}
