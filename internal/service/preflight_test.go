package service

import (
	"context"
	"strings"
	"testing"

	"github.com/wyw14/solo-036/internal/model"
	"github.com/wyw14/solo-036/internal/route"
)

const (
	testStation = "station-a"
	testRoute   = "main-station-a-station-c"
)

func validLaunchRequest() LaunchRequest {
	return LaunchRequest{StationID: testStation, RouteID: testRoute, TargetPressure: 11.0}
}

func gateByStatus(report model.PreflightReport, gate string) model.PreflightCheck {
	for _, check := range report.Checks {
		if check.Gate == gate {
			return check
		}
	}
	return model.PreflightCheck{}
}

func TestPreflightHappyPath(t *testing.T) {
	runtime := NewRuntime()
	runtime.ensureControl()

	report := runtime.Preflight(validLaunchRequest())
	if !report.Ready {
		t.Fatalf("expected ready report, got fail: %+v", report.Checks)
	}
	if len(report.Checks) != 6 {
		t.Fatalf("expected 6 itemized checks, got %d", len(report.Checks))
	}
	wantCategories := map[model.PreflightCategory]bool{
		model.PreflightStation:   false,
		model.PreflightRoute:     false,
		model.PreflightPressure:  false,
		model.PreflightClosure:   false,
		model.PreflightOccupancy: false,
	}
	for _, check := range report.Checks {
		if check.Status != model.PreflightPass {
			t.Errorf("gate %s = %s, want pass (%s)", check.Gate, check.Status, check.Detail)
		}
		if check.Blocking {
			t.Errorf("gate %s must not be blocking on a ready report", check.Gate)
		}
		if _, ok := wantCategories[check.Category]; ok {
			wantCategories[check.Category] = true
		}
	}
	for category, seen := range wantCategories {
		if !seen {
			t.Errorf("report is missing category %s", category)
		}
	}
	if report.Revision == "" || report.CheckedAt.IsZero() {
		t.Errorf("report must pin a revision and timestamp: %+v", report)
	}
	if report.Revision != runtime.Snapshot().Revision {
		t.Errorf("preflight revision %s does not match control revision %s", report.Revision, runtime.Snapshot().Revision)
	}
}

func TestPreflightHasNoSideEffects(t *testing.T) {
	runtime := NewRuntime()
	runtime.ensureControl()

	before := runtime.Snapshot()
	_ = runtime.Preflight(validLaunchRequest())
	_ = runtime.Preflight(validLaunchRequest())
	after := runtime.Snapshot()

	if len(runtime.Operations()) != 0 {
		t.Errorf("preflight created runs: %d", len(runtime.Operations()))
	}
	if len(route.Snapshot(runtime.Routes)) != 0 {
		t.Errorf("preflight acquired route leases: %+v", route.Snapshot(runtime.Routes))
	}
	if len(runtime.Journal.Events()) != 0 {
		t.Errorf("preflight appended journal events: %d", len(runtime.Journal.Events()))
	}
	if len(runtime.Pressure.History()) != 0 {
		t.Errorf("preflight applied pressure: %v", runtime.Pressure.History())
	}
	if after.Revision != before.Revision {
		t.Errorf("preflight changed control revision %s -> %s", before.Revision, after.Revision)
	}
}

// TestPreflightMatchesLaunchRejection is the core guarantee: every gate the
// preflight reports as blocking must be a gate the real launch rejects, with
// the same message, and a ready preflight must actually launch.
func TestPreflightMatchesLaunchRejection(t *testing.T) {
	cases := []struct {
		name      string
		request   LaunchRequest
		occupy    bool
		gate      string
		wantError string
	}{
		{
			name:      "unknown station",
			request:   LaunchRequest{StationID: "station-x", RouteID: testRoute, TargetPressure: 11.0},
			gate:      "station.ready",
			wantError: "unknown launch station station-x",
		},
		{
			name:      "unknown route",
			request:   LaunchRequest{StationID: testStation, RouteID: "nope", TargetPressure: 11.0},
			gate:      "route.ready",
			wantError: "unknown route nope",
		},
		{
			name:      "path unavailable at pressure",
			request:   LaunchRequest{StationID: testStation, RouteID: testRoute, TargetPressure: 11.5},
			gate:      "route.path",
			wantError: "no enabled route from station-a to station-c at pressure 11.50",
		},
		{
			name:      "pressure must be positive",
			request:   LaunchRequest{StationID: testStation, RouteID: testRoute, TargetPressure: 0},
			gate:      "route.path",
			wantError: "requested pressure must be positive",
		},
		{
			name:      "route occupied by another run",
			request:   validLaunchRequest(),
			occupy:    true,
			gate:      "occupancy.route",
			wantError: "route owned by other-run",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runtime := NewRuntime()
			runtime.ensureControl()
			if tc.occupy {
				if _, err := runtime.Routes.Reserve(tc.request.RouteID, "other-run", 7); err != nil {
					t.Fatalf("seed occupancy: %v", err)
				}
			}

			report := runtime.Preflight(tc.request)
			if report.Ready {
				t.Fatalf("expected blocking preflight, got ready")
			}
			blocking := gateByStatus(report, tc.gate)
			if blocking.Status != model.PreflightFail || !blocking.Blocking {
				t.Fatalf("gate %s = %+v, want blocking fail", tc.gate, blocking)
			}
			if blocking.Detail != tc.wantError {
				t.Errorf("gate detail %q does not match launch error %q", blocking.Detail, tc.wantError)
			}
			// Earlier gates may pass; everything after the blocker is skipped.
			blockerIndex := -1
			for i, check := range report.Checks {
				if check.Gate == tc.gate {
					blockerIndex = i
				}
			}
			for i, check := range report.Checks {
				if i > blockerIndex && check.Status != model.PreflightSkip {
					t.Errorf("gate after blocker %s should be skip, got %s", check.Gate, check.Status)
				}
			}

			_, launchErr := runtime.Launch(context.Background(), tc.request)
			if launchErr == nil {
				t.Fatalf("real launch succeeded despite blocking preflight")
			}
			if launchErr.Error() != tc.wantError {
				t.Errorf("launch error %q does not match preflight gate %q", launchErr.Error(), tc.wantError)
			}
		})
	}
}

func TestReadyPreflightActuallyLaunches(t *testing.T) {
	runtime := NewRuntime()
	runtime.ensureControl()

	report := runtime.Preflight(validLaunchRequest())
	if !report.Ready {
		t.Fatalf("expected ready: %+v", report.Checks)
	}
	record, err := runtime.Launch(context.Background(), validLaunchRequest())
	if err != nil {
		t.Fatalf("ready preflight must launch, got %v", err)
	}
	if record.Run.State != model.StateLaunched {
		t.Errorf("run state = %s, want launched", record.Run.State)
	}
}

func TestPreflightSkipAfterBlocker(t *testing.T) {
	runtime := NewRuntime()
	runtime.ensureControl()

	// Bad station: every later gate must be skipped, never independently failed.
	report := runtime.Preflight(LaunchRequest{StationID: "missing", RouteID: testRoute, TargetPressure: 11.0})
	for _, check := range report.Checks {
		if check.Gate == "station.ready" {
			if check.Status != model.PreflightFail {
				t.Errorf("station gate = %s, want fail", check.Status)
			}
			continue
		}
		if check.Status != model.PreflightSkip {
			t.Errorf("gate %s = %s, want skip", check.Gate, check.Status)
		}
		if !strings.Contains(check.Detail, "earlier gate") {
			t.Errorf("skip detail should explain short-circuit, got %q", check.Detail)
		}
	}
}
