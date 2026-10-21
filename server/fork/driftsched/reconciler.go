// SPDX-License-Identifier: Apache-2.0

package driftsched

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/logging"
)

// Service runs the drift API handlers in-process (controllers.APIController.CallDrift).
type Service interface {
	CallDrift(path string, body []byte) (status int, resp []byte)
}

// Reconciler runs the configured entries on their cron schedules.
// Single replica only: with several replicas every one fires every tick (US-070).
type Reconciler struct {
	entries []Entry
	svc     Service
	log     logging.SimpleLogging
	crons   []*cron.Cron
	offsets map[string]int
}

// New builds a Reconciler; entries must already be validated.
func New(entries []Entry, svc Service, log logging.SimpleLogging) (*Reconciler, error) {
	r := &Reconciler{entries: entries, svc: svc, log: log, offsets: map[string]int{}}
	byTZ := map[string]*cron.Cron{}
	for i := range entries {
		e := &entries[i]
		c, ok := byTZ[e.TZ]
		if !ok {
			loc, err := time.LoadLocation(e.TZ)
			if err != nil {
				return nil, fmt.Errorf("schedule %q: %w", e.ID, err)
			}
			c = cron.New(cron.WithParser(parser), cron.WithLocation(loc),
				cron.WithChain(cron.SkipIfStillRunning(cronLogger{log})))
			byTZ[e.TZ] = c
			r.crons = append(r.crons, c)
		}
		if _, err := c.AddFunc(e.Cron, func() { r.Tick(e) }); err != nil {
			return nil, fmt.Errorf("schedule %q: %w", e.ID, err)
		}
	}
	return r, nil
}

func (r *Reconciler) Start() {
	for _, c := range r.crons {
		c.Start()
	}
}

// Stop halts new ticks and waits for running ones, up to ctx.
func (r *Reconciler) Stop(ctx context.Context) {
	for _, c := range r.crons {
		select {
		case <-c.Stop().Done():
		case <-ctx.Done():
			return
		}
	}
}

// Tick runs one entry once. Not safe to call concurrently for the same entry;
// SkipIfStillRunning guarantees that for cron-driven calls.
func (r *Reconciler) Tick(e *Entry) {
	repos := e.Repos
	if e.MaxPerRun > 0 && e.MaxPerRun < len(repos) {
		start := r.offsets[e.ID] % len(repos)
		r.offsets[e.ID] = start + e.MaxPerRun
		repos = make([]string, 0, e.MaxPerRun)
		for i := range e.MaxPerRun {
			repos = append(repos, e.Repos[(start+i)%len(e.Repos)])
		}
	}
	for _, repo := range repos {
		path, body, err := e.request(repo)
		if err != nil {
			r.log.Err("drift schedule %q repo %q: %s", e.ID, repo, err)
			continue
		}
		status, resp := r.svc.CallDrift(path, body)
		// 207 is detect with per-project errors, recorded in storage already.
		if status != http.StatusOK && status != http.StatusMultiStatus {
			r.log.Err("drift schedule %q repo %q action %q returned status %d: %.300s", e.ID, repo, e.Action, status, resp)
			continue
		}
		r.log.Info("drift schedule %q repo %q action %q finished with status %d", e.ID, repo, e.Action, status)
	}
}

func (e *Entry) request(repo string) (string, []byte, error) {
	if e.Action == ActionDetect {
		b, err := json.Marshal(models.DriftDetectionRequest{Repository: repo, Ref: e.Ref,
			BaseBranch: e.BaseBranch, Type: e.Type, Projects: e.Projects})
		return "/api/drift/detect", b, err
	}
	b, err := json.Marshal(models.RemediationRequest{Repository: repo, Ref: e.Ref,
		BaseBranch: e.BaseBranch, Type: e.Type, Projects: e.Projects, DriftOnly: true,
		Action: models.RemediationAction(e.Action)})
	return "/api/drift/remediate", b, err
}

// cronLogger adapts the Atlantis logger to cron.Logger (used for skipped overlapping runs).
type cronLogger struct{ l logging.SimpleLogging }

func (c cronLogger) Info(msg string, kv ...any) { c.l.Info("drift scheduler: %s %v", msg, kv) }

func (c cronLogger) Error(err error, msg string, kv ...any) {
	c.l.Err("drift scheduler: %s: %s %v", msg, err, kv)
}
