package model

import "time"

// PreflightStatus is the per-check verdict of a read-only launch rehearsal.
type PreflightStatus string

const (
	// PreflightPass means the gate would accept the launch right now.
	PreflightPass PreflightStatus = "pass"
	// PreflightFail means the gate would reject the launch; the real launch
	// is guaranteed to stop at the matching gate until state changes.
	PreflightFail PreflightStatus = "fail"
	// PreflightSkip means the gate was not evaluated because an earlier gate
	// already blocks (the real launch short-circuits in the same order).
	PreflightSkip PreflightStatus = "skip"
)

// PreflightCategory groups checks by the field resource they evaluate.
type PreflightCategory string

const (
	PreflightStation   PreflightCategory = "station"
	PreflightRoute     PreflightCategory = "route"
	PreflightPressure  PreflightCategory = "pressure"
	PreflightClosure   PreflightCategory = "closure"
	PreflightOccupancy PreflightCategory = "occupancy"
)

// PreflightCheck is one item of the itemized launch rehearsal. Gate names the
// exact decision reused by the real launch, so a verdict can be matched to the
// re-check performed when the launch actually runs.
type PreflightCheck struct {
	Gate     string            `json:"gate"`
	Category PreflightCategory `json:"category"`
	Status   PreflightStatus   `json:"status"`
	Detail   string            `json:"detail"`
	// Blocking is true exactly when this gate rejects the real launch.
	Blocking bool `json:"blocking"`
}

// PreflightReport is the side-effect-free result of rehearsing a launch.
type PreflightReport struct {
	StationID      string           `json:"station_id"`
	RouteID        string           `json:"route_id"`
	TargetPressure float64          `json:"target_pressure"`
	Ready          bool             `json:"ready"`
	Revision       string           `json:"revision"`
	CheckedAt      time.Time        `json:"checked_at"`
	Checks         []PreflightCheck `json:"checks"`
}
