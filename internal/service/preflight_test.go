package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wyw14/solo-036/internal/permit"
)

var testLaunchRequest = LaunchRequest{StationID: "station-a", RouteID: "main-station-a-station-c", TargetPressure: 10.0}

func checkByName(checks []LaunchCheck, name string) (LaunchCheck, bool) {
	for _, check := range checks {
		if check.Name == name {
			return check, true
		}
	}
	return LaunchCheck{}, false
}

func TestPreflightLaunchReadyAndSideEffectFree(t *testing.T) {
	r := NewRuntime()
	revisionBefore := r.Snapshot().Revision

	report := r.PreflightLaunch(testLaunchRequest)
	if !report.Ready {
		t.Fatalf("expected ready preflight, got blocking %v", report.Blocking)
	}
	if len(report.Checks) != 5 {
		t.Fatalf("expected 5 checks, got %d", len(report.Checks))
	}
	for _, check := range report.Checks {
		if !check.Ready {
			t.Errorf("check %s should be ready: %s", check.Name, check.Detail)
		}
		if check.Gate == "" {
			t.Errorf("check %s is missing its launch-chain gate", check.Name)
		}
	}
	if report.Revision != revisionBefore {
		t.Errorf("state revision changed by preflight: %s -> %s", revisionBefore, report.Revision)
	}
	// No field state may change: no run, no route fence, no closure receipt,
	// no pressure authorization, no permit mark, no journal event, no incident.
	if got := len(r.Runs.Snapshot()); got != 0 {
		t.Errorf("runs created by preflight: %d", got)
	}
	if _, owned := r.Routes.Owner(testLaunchRequest.RouteID); owned {
		t.Error("route fenced by preflight")
	}
	if receipt := r.Launcher.Current(); receipt.RunID != "" {
		t.Errorf("closure receipt left behind: %+v", receipt)
	}
	if auth := r.Pressure.AuthorizationSnapshot(); auth.Enabled || auth.Stopped {
		t.Errorf("pressure authorization changed: %+v", auth)
	}
	if got := len(permit.Snapshot(r.Permits)); got != 0 {
		t.Errorf("permits opened by preflight: %d", got)
	}
	if got := len(r.Journal.Events()); got != 0 {
		t.Errorf("journal events appended by preflight: %d", got)
	}
	if got := len(r.Snapshot().Incidents); got != 0 {
		t.Errorf("incidents recorded by preflight: %d", got)
	}
}

func TestPreflightLaunchBlockedItems(t *testing.T) {
	r := NewRuntime()
	cases := []struct {
		name    string
		request LaunchRequest
		blocked []string
	}{
		{"unknown station", LaunchRequest{StationID: "station-x", RouteID: "main-station-a-station-c", TargetPressure: 10}, []string{"station", "route"}},
		{"non-launcher station", LaunchRequest{StationID: "station-b", RouteID: "main-station-a-station-c", TargetPressure: 10}, []string{"station", "route"}},
		{"unknown route", LaunchRequest{StationID: "station-a", RouteID: "main-x", TargetPressure: 10}, []string{"route", "resource"}},
		{"pressure above launcher limit", LaunchRequest{StationID: "station-a", RouteID: "main-station-a-station-c", TargetPressure: 12.6}, []string{"route", "pressure"}},
		{"pressure above segment limit", LaunchRequest{StationID: "station-a", RouteID: "main-station-a-station-c", TargetPressure: 12.0}, []string{"route"}},
		{"non-positive pressure", LaunchRequest{StationID: "station-a", RouteID: "main-station-a-station-c", TargetPressure: 0}, []string{"pressure"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := r.PreflightLaunch(tc.request)
			if report.Ready {
				t.Fatalf("expected blocked preflight, got ready")
			}
			if len(report.Blocking) != len(tc.blocked) {
				t.Fatalf("expected %d blocked checks, got %v", len(tc.blocked), report.Blocking)
			}
			got := map[string]bool{}
			for _, name := range report.Blocking {
				got[name] = true
			}
			for _, want := range tc.blocked {
				if !got[want] {
					t.Errorf("expected %s blocked, blocking=%v", want, report.Blocking)
				}
			}
		})
	}
	if got := len(r.Runs.Snapshot()); got != 0 {
		t.Errorf("blocked preflights created runs: %d", got)
	}
}

func TestPreflightClosureProbeRestoresState(t *testing.T) {
	r := NewRuntime()
	// A stopped pressure controller is re-authorized by ConfirmClosure in the
	// real chain, so the closure check mirrors that and stays ready; what
	// matters is that the probe leaves every touched state exactly as found.
	r.Pressure.Stop()
	report := r.PreflightLaunch(testLaunchRequest)
	if !report.Ready {
		t.Fatalf("expected ready preflight, got %v", report.Blocking)
	}
	if auth := r.Pressure.AuthorizationSnapshot(); !auth.Stopped || auth.Enabled {
		t.Errorf("pressure state not restored after probe: %+v", auth)
	}
	if receipt := r.Launcher.Current(); receipt.RunID != "" {
		t.Errorf("closure receipt left behind: %+v", receipt)
	}
	if got := len(permit.Snapshot(r.Permits)); got != 0 {
		t.Errorf("permits opened by probe: %d", got)
	}
}

func TestLaunchRechecksAgainstRealRun(t *testing.T) {
	r := NewRuntime()
	before := r.PreflightLaunch(testLaunchRequest)
	if !before.Ready {
		t.Fatalf("preflight not ready: %v", before.Blocking)
	}
	response, err := r.Launch(context.Background(), testLaunchRequest)
	if err != nil {
		t.Fatalf("launch failed: %v", err)
	}
	if response.Operation.Run.ID == "" {
		t.Fatal("missing run in launch response")
	}
	if len(response.PreflightChecks) != 5 || len(response.LaunchChecks) != 5 {
		t.Fatalf("expected 5 checks in both versions, got %d/%d",
			len(response.PreflightChecks), len(response.LaunchChecks))
	}
	// The two versions correspond item-for-item in name and gate.
	for i := range response.PreflightChecks {
		if response.PreflightChecks[i].Name != response.LaunchChecks[i].Name ||
			response.PreflightChecks[i].Gate != response.LaunchChecks[i].Gate {
			t.Errorf("check %d mismatch: %+v vs %+v", i, response.PreflightChecks[i], response.LaunchChecks[i])
		}
	}
	for _, check := range response.LaunchChecks {
		if !check.Ready {
			t.Errorf("launch check %s not ready: %s", check.Name, check.Detail)
		}
	}
	// The resource check now sees the real run holding the fence.
	resource, _ := checkByName(response.LaunchChecks, "resource")
	if !strings.Contains(resource.Detail, response.Operation.Run.ID) {
		t.Errorf("resource check does not reference the real run: %s", resource.Detail)
	}
	// A fresh preflight now reports the route as occupied by that run.
	after := r.PreflightLaunch(testLaunchRequest)
	if after.Ready {
		t.Fatal("expected preflight to block after the route was fenced")
	}
	occupied, _ := checkByName(after.Checks, "resource")
	if occupied.Ready || !strings.Contains(occupied.Detail, response.Operation.Run.ID) {
		t.Errorf("expected resource blocked by %s, got %+v", response.Operation.Run.ID, occupied)
	}
}

func TestLaunchBlockedByOccupiedRoute(t *testing.T) {
	r := NewRuntime()
	first, err := r.Launch(context.Background(), testLaunchRequest)
	if err != nil {
		t.Fatalf("first launch failed: %v", err)
	}
	runsBefore := len(r.Runs.Snapshot())
	_, err = r.Launch(context.Background(), testLaunchRequest)
	var blocked *ChecksBlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("expected ChecksBlockedError, got %v", err)
	}
	resource, ok := checkByName(blocked.Checks, "resource")
	if !ok || resource.Ready {
		t.Fatalf("expected resource check blocked, got %+v", blocked.Checks)
	}
	if !strings.Contains(resource.Detail, first.Operation.Run.ID) {
		t.Errorf("blocking detail does not name the owner %s: %s", first.Operation.Run.ID, resource.Detail)
	}
	if got := len(r.Runs.Snapshot()); got != runsBefore {
		t.Errorf("blocked launch created runs: %d -> %d", runsBefore, got)
	}
	if lease, owned := r.Routes.Owner(testLaunchRequest.RouteID); !owned || lease.RunID != first.Operation.Run.ID {
		t.Errorf("route fence changed by blocked launch: %+v", lease)
	}
}

func TestValidateLaunchRequestMatchesPreflight(t *testing.T) {
	r := NewRuntime()
	bad := LaunchRequest{StationID: "station-x", RouteID: "main-station-a-station-c", TargetPressure: 10}
	report := r.PreflightLaunch(bad)
	if report.Ready || len(report.Blocking) == 0 {
		t.Fatalf("expected blocked preflight, got %+v", report)
	}
	err := r.ValidateLaunchRequest(bad)
	if err == nil {
		t.Fatal("expected validation error")
	}
	first, _ := checkByName(report.Checks, report.Blocking[0])
	if err.Error() != first.Detail {
		t.Errorf("validation error %q does not match first blocked check %q", err.Error(), first.Detail)
	}
	if err := r.ValidateLaunchRequest(testLaunchRequest); err != nil {
		t.Errorf("expected valid request, got %v", err)
	}
}
