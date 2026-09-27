package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/wyw14/solo-036/internal/service"
)

const launchBody = `{"station_id":"station-a","route_id":"main-station-a-station-c","target_pressure":10}`

func postJSON(t *testing.T, handler http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestPreflightEndpoint(t *testing.T) {
	handler := NewRouter(service.NewRuntime())
	rec := postJSON(t, handler, "/api/operations/preflight", launchBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	var report service.LaunchPreflightReport
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !report.Ready || len(report.Checks) != 5 {
		t.Fatalf("expected ready report with 5 checks, got %+v", report)
	}
	// Preflight must not have created an operation.
	ops := httptest.NewRecorder()
	handler.ServeHTTP(ops, httptest.NewRequest(http.MethodGet, "/api/operations", nil))
	if got := ops.Body.String(); got != "[]\n" && got != "[]" {
		t.Errorf("preflight created operations: %s", got)
	}
}

func TestLaunchEndpointReturnsChecksAndBlocksOnOccupied(t *testing.T) {
	handler := NewRouter(service.NewRuntime())

	rec := postJSON(t, handler, "/api/operations", launchBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body)
	}
	var launched service.LaunchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &launched); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if launched.Operation.Run.ID == "" || len(launched.PreflightChecks) != 5 || len(launched.LaunchChecks) != 5 {
		t.Fatalf("launch response missing run or checks: %+v", launched)
	}

	// A second launch on the same route is blocked with the per-item report.
	rec = postJSON(t, handler, "/api/operations", launchBody)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body)
	}
	var blocked struct {
		Error    string                `json:"error"`
		Ready    bool                  `json:"ready"`
		Checks   []service.LaunchCheck `json:"checks"`
		Blocking []string              `json:"blocking"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &blocked); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if blocked.Ready || len(blocked.Blocking) != 1 || blocked.Blocking[0] != "resource" {
		t.Errorf("unexpected blocked report: %+v", blocked)
	}

	// Preflight on the same route now agrees with the launch-time verdict.
	rec = postJSON(t, handler, "/api/operations/preflight", launchBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	var report service.LaunchPreflightReport
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if report.Ready || len(report.Blocking) != 1 || report.Blocking[0] != "resource" {
		t.Errorf("preflight disagrees with launch verdict: %+v", report)
	}
}
