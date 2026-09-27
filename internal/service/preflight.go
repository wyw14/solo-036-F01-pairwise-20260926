package service

import (
	"fmt"
	"strings"
	"time"

	"github.com/wyw14/solo-036/internal/launcher"
	"github.com/wyw14/solo-036/internal/model"
	"github.com/wyw14/solo-036/internal/route"
)

// Stable check and gate identifiers. The same identifiers appear in a
// read-only preflight report and in the checks re-run against the real run
// during launch, so operators can correlate the two versions item by item.
const (
	checkStation  = "station"
	checkRoute    = "route"
	checkPressure = "pressure"
	checkResource = "resource"
	checkClosure  = "closure"
)

const (
	gateValidate      = "validate"
	gateRouteReserve  = "route.Reserve"
	gateClosureProof  = "launcher.ConfirmClosure"
	gateInterlocks    = "launcher.CanLaunch"
	gatePressureApply = "pressure.Apply"
	gateRunStart      = "run.Start"
)

// LaunchCheck is one item of the launch gate evaluation. Ready is whether this
// item would block the launch; Gate names the exact launch-chain step that
// enforces the same condition when the run is really created.
type LaunchCheck struct {
	Name   string `json:"name"`
	Gate   string `json:"gate"`
	Ready  bool   `json:"ready"`
	Detail string `json:"detail"`
}

// LaunchPreflightReport is the side-effect-free verdict returned before any
// operation exists: no run is created, no route is fenced, no valve or
// compressor state changes and no journal event is appended.
type LaunchPreflightReport struct {
	Request     LaunchRequest `json:"request"`
	Ready       bool          `json:"ready"`
	Checks      []LaunchCheck `json:"checks"`
	Blocking    []string      `json:"blocking"`
	Revision    string        `json:"state_revision"`
	EvaluatedAt time.Time     `json:"evaluated_at"`
}

// LaunchResponse carries the created operation together with the checks that
// were re-evaluated against the real run (real run id and closure epoch), so
// they line up one-to-one with a preceding preflight report.
type LaunchResponse struct {
	Operation       model.OperationRecord `json:"operation"`
	PreflightChecks []LaunchCheck         `json:"preflight_checks"`
	LaunchChecks    []LaunchCheck         `json:"launch_checks"`
}

// ChecksBlockedError reports that one or more launch gates blocked the
// operation. It carries the per-item evaluation instead of a single opaque
// error so值守界面 can show exactly which check stopped the launch.
type ChecksBlockedError struct {
	Checks []LaunchCheck
}

func (e *ChecksBlockedError) Error() string {
	names := make([]string, 0)
	for _, check := range e.Checks {
		if !check.Ready {
			names = append(names, check.Name)
		}
	}
	return "launch blocked by checks: " + strings.Join(names, ", ")
}

// probeRunID is the synthetic run identity used by read-only probes. It is
// never inserted into the run store or the journal, and cannot collide with a
// real UUID-based run id.
func probeRunID(request LaunchRequest) string {
	return "preflight:" + request.StationID + "/" + request.RouteID
}

func checkReady(name, gate, detail string) LaunchCheck {
	return LaunchCheck{Name: name, Gate: gate, Ready: true, Detail: detail}
}

func checkBlocked(name, gate, detail string) LaunchCheck {
	return LaunchCheck{Name: name, Gate: gate, Ready: false, Detail: detail}
}

func blockingChecks(checks []LaunchCheck) []LaunchCheck {
	blocked := make([]LaunchCheck, 0)
	for _, check := range checks {
		if !check.Ready {
			blocked = append(blocked, check)
		}
	}
	return blocked
}

func blockingNames(checks []LaunchCheck) []string {
	blocked := blockingChecks(checks)
	names := make([]string, 0, len(blocked))
	for _, check := range blocked {
		names = append(names, check.Name)
	}
	return names
}

// PreflightLaunch evaluates every launch gate without changing field state.
func (r *Runtime) PreflightLaunch(request LaunchRequest) LaunchPreflightReport {
	// A brand-new run always starts at epoch 1, so its closure command would
	// be issued at epoch 2: the probe uses exactly that epoch.
	const freshClosureEpoch int64 = 2
	revision, checks := r.evaluateLaunchChecks(request, probeRunID(request), freshClosureEpoch)
	return LaunchPreflightReport{
		Request:     request,
		Ready:       len(blockingChecks(checks)) == 0,
		Checks:      checks,
		Blocking:    blockingNames(checks),
		Revision:    revision,
		EvaluatedAt: time.Now().UTC(),
	}
}

// evaluateLaunchChecks is the single source of launch-gate verdicts shared by
// preflight and by the real launch chain. runID/closureEpoch identify either
// the synthetic probe or the real created run. The closure item drives the
// interlock controllers through a probe that restores every touched state.
func (r *Runtime) evaluateLaunchChecks(request LaunchRequest, runID string, closureEpoch int64) (string, []LaunchCheck) {
	snapshot := r.Snapshot()
	stations := snapshot.StationIndex()
	routes := make(map[string]model.RouteState, len(snapshot.Routes))
	for _, routeState := range snapshot.Routes {
		routes[routeState.ID] = routeState
	}

	checks := make([]LaunchCheck, 0, 5)

	// 1. Station: must exist, be an enabled launcher.
	station, stationOK := stations[request.StationID]
	switch {
	case !stationOK:
		checks = append(checks, checkBlocked(checkStation, gateValidate,
			fmt.Sprintf("unknown launch station %s", request.StationID)))
	case station.Role != model.StationLauncher || !station.Enabled:
		checks = append(checks, checkBlocked(checkStation, gateValidate,
			"station is not an enabled launcher"))
	default:
		checks = append(checks, checkReady(checkStation, gateValidate,
			fmt.Sprintf("%s is an enabled launcher with pressure limit %.2f", station.Name, station.PressureLimit)))
	}

	// 2. Route: must exist, start at the launcher, and offer an enabled path
	// at the requested pressure.
	routeState, routeOK := routes[request.RouteID]
	var path route.Path
	switch {
	case !routeOK:
		checks = append(checks, checkBlocked(checkRoute, gateValidate,
			fmt.Sprintf("unknown route %s", request.RouteID)))
	case !stationOK:
		checks = append(checks, checkBlocked(checkRoute, gateValidate,
			"cannot verify route start: launch station is unknown"))
	case routeState.FromStation != station.ID:
		checks = append(checks, checkBlocked(checkRoute, gateValidate,
			"route does not start at launcher"))
	case request.TargetPressure <= 0:
		checks = append(checks, checkReady(checkRoute, gateValidate,
			"route starts at launcher; path resolution awaits a positive target pressure"))
	default:
		var err error
		path, err = r.ensureControl().Network.Path(routeState.FromStation, routeState.ToStation, request.TargetPressure)
		if err != nil {
			checks = append(checks, checkBlocked(checkRoute, gateValidate, err.Error()))
		} else {
			checks = append(checks, checkReady(checkRoute, gateValidate,
				fmt.Sprintf("path %s via %d segments (%.1f km) supports pressure %.2f",
					strings.Join(path.Stations, "->"), len(path.Segments), path.Length, path.MaximumPressure)))
		}
	}

	// 3. Pressure: requested value within the launcher's limit (segment limits
	// are already enforced by the route path above).
	switch {
	case request.TargetPressure <= 0:
		checks = append(checks, checkBlocked(checkPressure, gateValidate,
			"target pressure must be positive"))
	case stationOK && request.TargetPressure > station.PressureLimit:
		checks = append(checks, checkBlocked(checkPressure, gateValidate,
			fmt.Sprintf("target pressure %.2f exceeds launcher limit %.2f", request.TargetPressure, station.PressureLimit)))
	default:
		detail := fmt.Sprintf("target pressure %.2f is within launcher limit %.2f", request.TargetPressure, station.PressureLimit)
		if len(path.Segments) > 0 {
			detail += fmt.Sprintf(" and path ceiling %.2f", path.MaximumPressure)
		}
		checks = append(checks, checkReady(checkPressure, gatePressureApply, detail))
	}

	// 4. Resource occupancy: same ownership predicate enforced by
	// route.Coordinator.Reserve — a foreign lease is the one thing that call
	// rejects. The lookup is read-only.
	if !routeOK {
		checks = append(checks, checkBlocked(checkResource, gateRouteReserve,
			fmt.Sprintf("cannot inspect occupancy: unknown route %s", request.RouteID)))
	} else if lease, owned := r.Routes.Owner(request.RouteID); owned && lease.RunID != runID {
		checks = append(checks, checkBlocked(checkResource, gateRouteReserve,
			fmt.Sprintf("route fenced to %s at epoch %d", lease.RunID, lease.Epoch)))
	} else if owned {
		checks = append(checks, checkReady(checkResource, gateRouteReserve,
			fmt.Sprintf("route already fenced to %s at epoch %d", lease.RunID, lease.Epoch)))
	} else {
		checks = append(checks, checkReady(checkResource, gateRouteReserve, "route is free"))
	}

	// 5. Closure/interlocks: drive BeginClosure -> ConfirmClosure -> CanLaunch
	// through a rollback probe so the verdict matches the real chain without
	// leaving a closure receipt, pressure authorization or permit mark behind.
	probe := r.Launcher.ProbeClosure(runID, closureEpoch)
	switch probe.Verdict {
	case launcher.ClosureRejected:
		checks = append(checks, checkBlocked(checkClosure, gateClosureProof,
			fmt.Sprintf("closure proof rejected at epoch %d", closureEpoch)))
	case launcher.ClosureInterlocked:
		checks = append(checks, checkBlocked(checkClosure, gateInterlocks,
			fmt.Sprintf("launcher interlocks rejected the closure proof at epoch %d", closureEpoch)))
	default:
		checks = append(checks, checkReady(checkClosure, gateInterlocks,
			fmt.Sprintf("closure proof accepted at epoch %d; pressure authorized and launch interlocks clear", closureEpoch)))
	}

	return snapshot.Revision, checks
}

// ValidateLaunchRequest preserves the request-level validation entry point and
// derives its answer from the same shared evaluation, so it can never drift
// from what preflight (or launch) would decide.
func (r *Runtime) ValidateLaunchRequest(request LaunchRequest) error {
	const freshClosureEpoch int64 = 2
	_, checks := r.evaluateLaunchChecks(request, probeRunID(request), freshClosureEpoch)
	if blocked := blockingChecks(checks); len(blocked) > 0 {
		return fmt.Errorf("%s", blocked[0].Detail)
	}
	return nil
}
