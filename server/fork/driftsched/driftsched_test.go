// SPDX-License-Identifier: Apache-2.0

package driftsched_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/runatlantis/atlantis/server/core/drift"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/fork/driftredis"
	"github.com/runatlantis/atlantis/server/fork/driftsched"
	"github.com/runatlantis/atlantis/server/logging"
)

func TestValidate(t *testing.T) {
	ok := driftsched.Entry{ID: "a", Repos: []string{"o/r"}, Cron: "*/5 * * * *", Action: "detect"}
	cases := map[string]struct {
		mut        func(e *driftsched.Entry)
		allowApply bool
		wantErr    string
	}{
		"valid":        {mut: func(*driftsched.Entry) {}},
		"bad cron":     {mut: func(e *driftsched.Entry) { e.Cron = "61 * * * *" }, wantErr: "invalid cron"},
		"bad tz":       {mut: func(e *driftsched.Entry) { e.TZ = "Mars/Base" }, wantErr: "invalid tz"},
		"bad action":   {mut: func(e *driftsched.Entry) { e.Action = "nuke" }, wantErr: "action"},
		"wildcard":     {mut: func(e *driftsched.Entry) { e.Repos = []string{"o/*"} }, wantErr: "wildcards"},
		"no repos":     {mut: func(e *driftsched.Entry) { e.Repos = nil }, wantErr: "repos"},
		"no id":        {mut: func(e *driftsched.Entry) { e.ID = "" }, wantErr: "id is required"},
		"apply denied": {mut: func(e *driftsched.Entry) { e.Action = "apply" }, wantErr: "enable-drift-remediation"},
		"apply ok":     {mut: func(e *driftsched.Entry) { e.Action = "apply" }, allowApply: true},
		"negative max": {mut: func(e *driftsched.Entry) { e.MaxPerRun = -1 }, wantErr: "max_per_run"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := ok
			c.mut(&e)
			err := driftsched.Validate([]driftsched.Entry{e}, c.allowApply)
			if c.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
				t.Fatalf("want error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

func TestValidate_DuplicateID(t *testing.T) {
	e := driftsched.Entry{ID: "a", Repos: []string{"o/r"}, Cron: "@hourly", Action: "detect"}
	if err := driftsched.Validate([]driftsched.Entry{e, e}, false); err == nil {
		t.Fatal("expected duplicate id error")
	}
}

func TestLoadFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.yaml")
	write := func(s string) {
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("schedules:\n  - id: nightly\n    repos: [o/r]\n    cron: '0 2 * * *'\n    tz: Europe/Warsaw\n    action: detect\n")
	es, err := driftsched.LoadFile(p, false)
	if err != nil || len(es) != 1 || es[0].Ref != "main" || es[0].Type != "Github" {
		t.Fatalf("got %+v, %v", es, err)
	}
	write("schedules:\n  - id: x\n    repoz: [o/r]\n")
	if _, err := driftsched.LoadFile(p, false); err == nil {
		t.Fatal("unknown field must fail")
	}
}

func TestNextRuns_Timezone(t *testing.T) {
	e := driftsched.Entry{ID: "a", Repos: []string{"o/r"}, Cron: "0 2 * * *", TZ: "Europe/Warsaw", Action: "detect"}
	got, err := e.NextRuns(time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC), 2)
	if err != nil {
		t.Fatal(err)
	}
	// 02:00 CET (UTC+1) on 2026-01-10 and the next day.
	want := []time.Time{
		time.Date(2026, 1, 10, 1, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 11, 1, 0, 0, 0, time.UTC),
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Errorf("run %d: want %v got %v", i, want[i], got[i])
		}
	}
	// In summer Warsaw is UTC+2, so the same cron fires an hour earlier in UTC.
	summer, _ := e.NextRuns(time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC), 1)
	if !summer[0].Equal(time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("summer: got %v", summer[0])
	}
}

// fakeService stands in for the API handlers: detect stores one drifted project.
type fakeService struct {
	storage drift.Storage
	calls   []string
	status  int
}

func (f *fakeService) CallDrift(path string, body []byte) (int, []byte) {
	f.calls = append(f.calls, path+" "+string(body))
	if f.status != 0 {
		return f.status, []byte("boom")
	}
	if path == "/api/drift/detect" {
		var req models.DriftDetectionRequest
		if err := json.Unmarshal(body, &req); err != nil {
			return http.StatusBadRequest, nil
		}
		err := f.storage.Store(req.Repository, models.ProjectDrift{
			ProjectName: "p", Ref: req.Ref, LastChecked: time.Now(),
			Drift: models.DriftSummary{HasDrift: true, ToChange: 1},
		})
		if err != nil {
			return http.StatusInternalServerError, nil
		}
	}
	return http.StatusOK, nil
}

func newReconciler(t *testing.T, svc driftsched.Service, es []driftsched.Entry) *driftsched.Reconciler {
	t.Helper()
	if err := driftsched.Validate(es, true); err != nil {
		t.Fatal(err)
	}
	r, err := driftsched.New(es, svc, logging.NewNoopLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestTick_DetectStoresToRedis(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	storage := driftredis.NewStorage(client, driftredis.Options{})

	es := []driftsched.Entry{{ID: "n", Repos: []string{"o/r"}, Cron: "@hourly", Action: "detect"}}
	r := newReconciler(t, &fakeService{storage: storage}, es)
	r.Tick(&es[0])

	got, err := storage.Get("o/r", drift.GetOptions{})
	if err != nil || len(got) != 1 || !got[0].Drift.HasDrift || got[0].Ref != "main" {
		t.Fatalf("drift not stored: %+v %v", got, err)
	}
}

func TestTick_MaxPerRunRotates(t *testing.T) {
	svc := &fakeService{storage: drift.NewInMemoryStorage()}
	es := []driftsched.Entry{{ID: "n", Repos: []string{"o/a", "o/b", "o/c"}, Cron: "@hourly", Action: "detect", MaxPerRun: 2}}
	r := newReconciler(t, svc, es)
	r.Tick(&es[0])
	r.Tick(&es[0])
	var repos []string
	for _, c := range svc.calls {
		var req models.DriftDetectionRequest
		_ = json.Unmarshal([]byte(strings.TrimPrefix(c, "/api/drift/detect ")), &req)
		repos = append(repos, req.Repository)
	}
	if strings.Join(repos, ",") != "o/a,o/b,o/c,o/a" {
		t.Fatalf("got %v", repos)
	}
}

func TestTick_PlanUsesRemediate(t *testing.T) {
	svc := &fakeService{storage: drift.NewInMemoryStorage()}
	es := []driftsched.Entry{{ID: "n", Repos: []string{"o/a"}, Cron: "@hourly", Action: "plan"}}
	r := newReconciler(t, svc, es)
	r.Tick(&es[0])
	if len(svc.calls) != 1 || !strings.HasPrefix(svc.calls[0], "/api/drift/remediate ") || !strings.Contains(svc.calls[0], `"action":"plan"`) {
		t.Fatalf("got %v", svc.calls)
	}
}

func TestTick_ErrorStatusDoesNotStopOtherRepos(t *testing.T) {
	svc := &fakeService{storage: drift.NewInMemoryStorage(), status: http.StatusInternalServerError}
	es := []driftsched.Entry{{ID: "n", Repos: []string{"o/a", "o/b"}, Cron: "@hourly", Action: "detect"}}
	r := newReconciler(t, svc, es)
	r.Tick(&es[0])
	if len(svc.calls) != 2 {
		t.Fatalf("got %d calls", len(svc.calls))
	}
}
