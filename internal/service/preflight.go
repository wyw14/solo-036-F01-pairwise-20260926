package service

import (
	"fmt"
	"time"

	"github.com/wyw14/solo-036/internal/model"
	"github.com/wyw14/solo-036/internal/route"
)

// launchGate is one decision shared by the real launch and the read-only
// preflight. err is the exact error the launch returns when the gate blocks,
// so a preflight "fail" lines up word for word with the launch re-check.
type launchGate struct {
	check model.PreflightCheck
	err   error
}

// launchGateView is the immutable state a launch rehearsal reads. It is
// captured without mutating runs, leases, closures, pressure or the journal.
type launchGateView struct {
	network  *route.Network
	stations map[string]model.Station
	routes   map[string]model.RouteState
	closures map[string]model.ClosureState
	revision string
}

func (r *Runtime) launchGateView() launchGateView {
	control := r.ensureControl()
	r.mu.Lock()
	defer r.mu.Unlock()
	view := launchGateView{
		network:  control.Network,
		stations: make(map[string]model.Station, len(control.Stations)),
		routes:   make(map[string]model.RouteState, len(control.Routes)),
		closures: make(map[string]model.ClosureState, len(control.Closures)),
		revision: control.Revision,
	}
	for id, station := range control.Stations {
		view.stations[id] = station
	}
	for id, routeState := range control.Routes {
		view.routes[id] = routeState
	}
	for id, closure := range control.Closures {
		view.closures[id] = closure
	}
	return view
}

func pass(gate string, category model.PreflightCategory, detail string) launchGate {
	return launchGate{check: model.PreflightCheck{
		Gate:     gate,
		Category: category,
		Status:   model.PreflightPass,
		Detail:   detail,
	}}
}

func failGate(gate string, category model.PreflightCategory, err error) launchGate {
	return launchGate{
		check: model.PreflightCheck{
			Gate:     gate,
			Category: category,
			Status:   model.PreflightFail,
			Detail:   err.Error(),
			Blocking: true,
		},
		err: err,
	}
}

func skip(gate string, category model.PreflightCategory) launchGate {
	return launchGate{check: model.PreflightCheck{
		Gate:     gate,
		Category: category,
		Status:   model.PreflightSkip,
		Detail:   "not evaluated: an earlier gate blocks the launch",
	}}
}

// evaluateValidationGates builds the gates ValidateLaunchRequest enforces,
// before any run is created: station, route identity, path reachability and
// pressure limit. blocked reports whether one of them rejects the launch.
func evaluateValidationGates(view launchGateView, request LaunchRequest) ([]launchGate, bool) {
	gates := make([]launchGate, 0, 4)
	blocked := false

	// Gate: station — mirrors the first checks of ValidateLaunchRequest.
	station, stationOK := view.stations[request.StationID]
	if !stationOK {
		blocked = true
		gates = append(gates, failGate("station.ready", model.PreflightStation,
			fmt.Errorf("unknown launch station %s", request.StationID)))
	} else if station.Role != model.StationLauncher || !station.Enabled {
		blocked = true
		gates = append(gates, failGate("station.ready", model.PreflightStation,
			fmt.Errorf("station is not an enabled launcher")))
	} else {
		gates = append(gates, pass("station.ready", model.PreflightStation,
			fmt.Sprintf("station %s is an enabled launcher", station.ID)))
	}

	// Gate: route identity — route exists and starts at the launcher.
	routeState, routeOK := view.routes[request.RouteID]
	if blocked {
		gates = append(gates, skip("route.ready", model.PreflightRoute))
	} else if !routeOK {
		blocked = true
		gates = append(gates, failGate("route.ready", model.PreflightRoute,
			fmt.Errorf("unknown route %s", request.RouteID)))
	} else if routeState.FromStation != station.ID {
		blocked = true
		gates = append(gates, failGate("route.ready", model.PreflightRoute,
			fmt.Errorf("route does not start at launcher")))
	} else {
		gates = append(gates, pass("route.ready", model.PreflightRoute,
			fmt.Sprintf("route %s runs from %s to %s", routeState.ID, routeState.FromStation, routeState.ToStation)))
	}

	// Gate: route reachability at the requested pressure — Network.Path.
	if blocked {
		gates = append(gates, skip("route.path", model.PreflightRoute))
	} else {
		path, err := view.network.Path(routeState.FromStation, routeState.ToStation, request.TargetPressure)
		if err != nil {
			blocked = true
			gates = append(gates, failGate("route.path", model.PreflightRoute, err))
		} else {
			gates = append(gates, pass("route.path", model.PreflightRoute,
				fmt.Sprintf("enabled path %v available at pressure %.2f", path.Segments, request.TargetPressure)))
		}
	}

	// Gate: pressure within station limit — checked by ValidateLaunchRequest
	// immediately after Network.Path, so it inherits the same short-circuit.
	if blocked {
		gates = append(gates, skip("pressure.limit", model.PreflightPressure))
	} else if request.TargetPressure <= 0 || request.TargetPressure > station.PressureLimit {
		blocked = true
		gates = append(gates, failGate("pressure.limit", model.PreflightPressure,
			fmt.Errorf("target pressure must be within 0 and %.2f", station.PressureLimit)))
	} else {
		gates = append(gates, pass("pressure.limit", model.PreflightPressure,
			fmt.Sprintf("target pressure %.2f is within station limit %.2f", request.TargetPressure, station.PressureLimit)))
	}

	return gates, blocked
}

// evaluateLaunchGates runs the same decisions Runtime.Launch walks, in the same
// order, without performing any of the launch's side effects. It appends the
// gates the launch enforces after run creation: route occupancy and the closure
// interlock. Once a gate blocks, later gates are "skip", mirroring the launch's
// short-circuit. The returned errors are the exact launch rejection errors.
func (r *Runtime) evaluateLaunchGates(request LaunchRequest) (launchGateView, []launchGate) {
	view := r.launchGateView()
	gates, blocked := evaluateValidationGates(view, request)

	// Gate: occupancy — the same ownership predicate Routes.Reserve enforces.
	// A prospective run is never the holder, so any lease is a conflict.
	if blocked {
		gates = append(gates, skip("occupancy.route", model.PreflightOccupancy))
	} else if owner, owned := r.Routes.Owner(request.RouteID); owned {
		blocked = true
		gates = append(gates, failGate("occupancy.route", model.PreflightOccupancy, route.OwnerConflict(owner)))
	} else {
		gates = append(gates, pass("occupancy.route", model.PreflightOccupancy,
			fmt.Sprintf("route %s is not fenced to another run", request.RouteID)))
	}

	// Gate: closure interlock — the ConfirmClosure + CanLaunch decision. The
	// real launch issues a fresh BeginClosure and authorizes pressure for the
	// new run, so the verdict is read without driving them; the detail still
	// names any lock currently held for awareness.
	if blocked {
		gates = append(gates, skip("closure.interlock", model.PreflightClosure))
	} else {
		gates = append(gates, r.closureGate(view, request.StationID))
	}

	return view, gates
}

// closureGate renders the read-only closure/interlock verdict. The launch
// drives a brand-new closure command (epoch derived from the new run) and then
// authorizes pressurization, so a healthy, wired launcher always passes this
// gate; this helper must never report a failure the launch itself would not.
func (r *Runtime) closureGate(view launchGateView, stationID string) launchGate {
	detail := "launch will issue a fresh closure command at a new epoch and re-authorize pressurization"
	if closure, ok := view.closures[stationID]; ok && closure.Locked && closure.RunID != "" {
		detail = fmt.Sprintf(
			"closure currently locked for %s at epoch %d; launch drives a fresh closure command and re-authorizes",
			closure.RunID, closure.CommandEpoch)
	}
	return pass("closure.interlock", model.PreflightClosure, detail)
}

// Preflight rehearses a launch without changing field state: it creates no run,
// takes no lease, drives no closure, applies no pressure and appends no event.
// Each item names the gate the real launch re-checks, and Revision pins the
// control-state version the conclusions were drawn from.
func (r *Runtime) Preflight(request LaunchRequest) model.PreflightReport {
	view, gates := r.evaluateLaunchGates(request)
	checks := make([]model.PreflightCheck, 0, len(gates))
	ready := true
	for _, gate := range gates {
		if gate.check.Status == model.PreflightFail {
			ready = false
		}
		checks = append(checks, gate.check)
	}
	return model.PreflightReport{
		StationID:      request.StationID,
		RouteID:        request.RouteID,
		TargetPressure: request.TargetPressure,
		Ready:          ready,
		Revision:       view.revision,
		CheckedAt:      time.Now().UTC(),
		Checks:         checks,
	}
}

// firstBlockingError returns the exact launch rejection error for the first
// gate that blocks, preserving ValidateLaunchRequest's message and ordering.
func firstBlockingError(gates []launchGate) error {
	for _, gate := range gates {
		if gate.err != nil {
			return gate.err
		}
	}
	return nil
}
