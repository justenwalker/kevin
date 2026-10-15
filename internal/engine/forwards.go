package engine

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/justenwalker/kevin/internal/output"
	"github.com/justenwalker/kevin/internal/portforward"
	"github.com/justenwalker/kevin/internal/session"
	"github.com/justenwalker/kevin/protos/pb"
)

// forward is a local loopback listener that r.up opened for a relay-routed
// exposed port - a [portforward.TCP] or a [portforward.UDP] - closed at
// session teardown by closeForwards.
type forward interface {
	Addr() net.Addr
	Close() error
}

var (
	_ forward = (*portforward.TCP)(nil)
	_ forward = (*portforward.UDP)(nil)
)

// addForward records f under step for closeForwards to close at session
// teardown. Concurrent DAG steps call this from separate goroutines during
// r.up.
func (r *run) addForward(step string, f forward) {
	r.state.AddForward(step, f)
}

// closeStepForwards closes the forwards a previous Up of step opened, so a
// rerun does not stack listeners.
func (r *run) closeStepForwards(step string) {
	for _, f := range r.state.TakeForwards(step) {
		_ = f.Close()
	}
}

// exposePorts runs exposePort for each of eps, and reports the step failed on
// the first error.
func (r *run) exposePorts(ctx context.Context, name string, eps []*pb.ExposedPort, systemThis output.Map) error {
	for _, ep := range eps {
		if err := r.exposePort(ctx, name, ep, systemThis); err != nil {
			r.reportUpFailure(ctx, name, err)
			return err
		}
	}
	return nil
}

// exposePort records ep's upstream as a system output, and - for a
// relay-routed entry - opens a local forward for it (a TCP dial-per-
// connection forward, or a UDP ASSOCIATE session, by ep's protocol),
// recording that too. A failed UDP forward is an error; a failed TCP one is
// only a warning.
func (r *run) exposePort(ctx context.Context, name string, ep *pb.ExposedPort, systemThis output.Map) error {
	r.emit(name, fmt.Sprintf("exposing %s %s at %s", ep.GetProtocol(), ep.GetName(), ep.GetUpstream()))
	systemThis["expose_"+ep.GetName()] = output.Value{String: ep.GetUpstream()}

	if !ep.GetRelay() {
		return nil
	}
	spec := portforward.Spec{
		Name:          ep.GetName(),
		Upstream:      ep.GetUpstream(),
		HostPort:      int(ep.GetHostPort()),
		RelayUDPAddrs: ep.GetRelayUdpAddrs(),
	}
	var pf forward
	var fwdErr error
	if ep.GetProtocol() == "udp" {
		pf, fwdErr = portforward.StartUDP(ctx, spec)
	} else {
		pf, fwdErr = portforward.StartTCP(ctx, spec)
	}
	if fwdErr != nil {
		if ep.GetProtocol() == "udp" {
			return fmt.Errorf("local forward for %s: %w", ep.GetName(), fwdErr)
		}
		r.emit(name, "warning: local forward for "+ep.GetName()+": "+fwdErr.Error())
		return nil
	}
	r.addForward(name, pf)
	addr := pf.Addr().String()
	r.emit(name, fmt.Sprintf("forwarding %s at %s", ep.GetName(), addr))
	r.store.AddStepDetail(name, session.Detail{
		Label: ep.GetName() + " (local)", Value: addr, Copyable: true,
	})
	systemThis["forward_"+ep.GetName()] = output.Value{String: addr}
	return nil
}

// closeForwards closes every forward listener r.up opened.
func (r *run) closeForwards() error {
	forwards := r.state.TakeAllForwards()
	var errs []error
	for _, fs := range forwards {
		for _, pf := range fs {
			errs = append(errs, pf.Close())
		}
	}
	return errors.Join(errs...)
}
