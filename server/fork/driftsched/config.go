// SPDX-License-Identifier: Apache-2.0

// Package driftsched is the fork's in-process drift detection scheduler.
package driftsched

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	"go.yaml.in/yaml/v4"
)

const (
	ActionDetect = "detect"
	ActionPlan   = "plan"
	ActionApply  = "apply"
)

// Entry is one schedule. Repos are concrete "owner/repo" names: allowlist
// patterns cannot be enumerated, and the API handler still enforces the
// server's repo allowlist on every call.
type Entry struct {
	ID         string   `yaml:"id"`
	Repos      []string `yaml:"repos"`
	Projects   []string `yaml:"projects"`
	Cron       string   `yaml:"cron"`
	TZ         string   `yaml:"tz"`
	Action     string   `yaml:"action"`
	MaxPerRun  int      `yaml:"max_per_run"` // max repos per tick, 0 = all; rotates between ticks
	Ref        string   `yaml:"ref"`         // default "main"
	BaseBranch string   `yaml:"base_branch"`
	Type       string   `yaml:"type"` // VCS type, default "Github"

	next int // max_per_run rotation offset, owned by Tick
}

type file struct {
	Schedules []Entry `yaml:"schedules"`
}

var parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// LoadFile reads, defaults and validates the schedule file. Every problem is
// reported with the entry index and id so a bad config fails fast at startup.
func LoadFile(path string, allowApply bool) ([]Entry, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- operator supplied flag
	if err != nil {
		return nil, fmt.Errorf("reading drift schedule file: %w", err)
	}
	var f file
	var node yaml.Node
	err = yaml.NewDecoder(bytes.NewReader(data)).Decode(&node)
	if err == nil {
		err = node.Load(&f, yaml.WithKnownFields()) // reject typo'd keys
	}
	if err != nil {
		return nil, fmt.Errorf("parsing drift schedule file %q: %w", path, err)
	}
	return f.Schedules, Validate(f.Schedules, allowApply)
}

// Validate applies defaults in place and checks every entry.
func Validate(entries []Entry, allowApply bool) error {
	ids := map[string]bool{}
	for i := range entries {
		e := &entries[i]
		if e.Ref == "" {
			e.Ref = "main"
		}
		if e.Type == "" {
			e.Type = "Github"
		}
		if e.TZ == "" {
			e.TZ = "UTC"
		}
		if err := e.validate(allowApply); err != nil {
			return fmt.Errorf("drift schedule %d (id %q): %w", i, e.ID, err)
		}
		if ids[e.ID] {
			return fmt.Errorf("drift schedule %d: duplicate id %q", i, e.ID)
		}
		ids[e.ID] = true
	}
	return nil
}

func (e *Entry) validate(allowApply bool) error {
	if e.ID == "" {
		return fmt.Errorf("id is required")
	}
	if len(e.Repos) == 0 {
		return fmt.Errorf("repos must not be empty")
	}
	for _, r := range e.Repos {
		if !strings.Contains(r, "/") || strings.ContainsAny(r, "*? ") {
			return fmt.Errorf("repo %q must be a concrete owner/repo name (wildcards are not supported)", r)
		}
	}
	switch e.Action {
	case ActionDetect, ActionPlan:
	case ActionApply:
		if !allowApply {
			return fmt.Errorf("action apply requires --enable-drift-remediation")
		}
	default:
		return fmt.Errorf("action %q must be one of detect, plan, apply", e.Action)
	}
	if e.MaxPerRun < 0 {
		return fmt.Errorf("max_per_run must not be negative")
	}
	if _, err := time.LoadLocation(e.TZ); err != nil {
		return fmt.Errorf("invalid tz %q: %w", e.TZ, err)
	}
	if _, err := parser.Parse(e.Cron); err != nil {
		return fmt.Errorf("invalid cron %q: %w", e.Cron, err)
	}
	return nil
}

// NextRuns returns the next n trigger times of e after from, in e's timezone.
func (e *Entry) NextRuns(from time.Time, n int) ([]time.Time, error) {
	loc, err := time.LoadLocation(e.TZ)
	if err != nil {
		return nil, err
	}
	s, err := parser.Parse(e.Cron)
	if err != nil {
		return nil, err
	}
	t := from.In(loc)
	out := make([]time.Time, 0, n)
	for range n {
		t = s.Next(t)
		out = append(out, t)
	}
	return out, nil
}
