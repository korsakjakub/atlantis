// SPDX-License-Identifier: Apache-2.0

// Package storagetest provides a conformance suite for drift.Storage
// implementations.
package storagetest

import (
	"sort"
	"testing"
	"time"

	"github.com/runatlantis/atlantis/server/core/drift"
	"github.com/runatlantis/atlantis/server/events/models"
)

// Factory returns a fresh, empty Storage for each call.
type Factory func(t *testing.T) drift.Storage

func rec(project, path, ws, ref string) models.ProjectDrift {
	return models.ProjectDrift{
		ProjectName: project,
		Path:        path,
		Workspace:   ws,
		Ref:         ref,
		BaseBranch:  "main",
		LastChecked: time.Now(),
		PlanOutput:  "transient plan output",
	}
}

func names(ds []models.ProjectDrift) []string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.ProjectName+"@"+d.Ref)
	}
	sort.Strings(out)
	return out
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Run executes the table-driven conformance suite against the factory.
func Run(t *testing.T, newStorage Factory) {
	const repo = "github.com/o/r"
	const other = "github.com/o/other"

	seed := func(t *testing.T, s drift.Storage) {
		t.Helper()
		for _, d := range []models.ProjectDrift{
			rec("a", "dir/a", "default", "main"),
			rec("a", "dir/a", "default", "dev"),
			rec("b", "dir/b", "prod", "main"),
		} {
			if err := s.Store(repo, d); err != nil {
				t.Fatalf("seed store: %v", err)
			}
		}
		if err := s.Store(other, rec("c", "dir/c", "default", "main")); err != nil {
			t.Fatalf("seed store: %v", err)
		}
	}

	tests := []struct {
		name string
		run  func(t *testing.T, s drift.Storage) []string
		want []string
	}{
		{"get unknown repo is empty", func(t *testing.T, s drift.Storage) []string {
			ds, err := s.Get("nope", drift.GetOptions{})
			must(t, err)
			return names(ds)
		}, []string{}},
		{"get all in repo", func(t *testing.T, s drift.Storage) []string {
			seed(t, s)
			ds, err := s.Get(repo, drift.GetOptions{})
			must(t, err)
			return names(ds)
		}, []string{"a@dev", "a@main", "b@main"}},
		{"same project on different refs is kept separately", func(t *testing.T, s drift.Storage) []string {
			seed(t, s)
			ds, err := s.Get(repo, drift.GetOptions{ProjectName: "a"})
			must(t, err)
			return names(ds)
		}, []string{"a@dev", "a@main"}},
		{"store overwrites same identity", func(t *testing.T, s drift.Storage) []string {
			seed(t, s)
			d := rec("a", "dir/a", "default", "main")
			d.Drift.HasDrift = true
			must(t, s.Store(repo, d))
			ds, err := s.Get(repo, drift.GetOptions{ProjectName: "a", Ref: "main"})
			must(t, err)
			if len(ds) != 1 || !ds[0].Drift.HasDrift {
				t.Fatalf("expected one overwritten record, got %+v", ds)
			}
			return names(ds)
		}, []string{"a@main"}},
		{"filter by ref", func(t *testing.T, s drift.Storage) []string {
			seed(t, s)
			ds, err := s.Get(repo, drift.GetOptions{Ref: "main"})
			must(t, err)
			return names(ds)
		}, []string{"a@main", "b@main"}},
		{"filter by workspace and path", func(t *testing.T, s drift.Storage) []string {
			seed(t, s)
			ds, err := s.Get(repo, drift.GetOptions{Workspace: "prod", Path: "dir/b"})
			must(t, err)
			return names(ds)
		}, []string{"b@main"}},
		{"max age filters stale", func(t *testing.T, s drift.Storage) []string {
			old := rec("old", "p", "default", "main")
			old.LastChecked = time.Now().Add(-2 * time.Hour)
			must(t, s.Store(repo, old))
			must(t, s.Store(repo, rec("new", "p2", "default", "main")))
			ds, err := s.Get(repo, drift.GetOptions{MaxAge: time.Hour})
			must(t, err)
			return names(ds)
		}, []string{"new@main"}},
		{"plan output is never persisted", func(t *testing.T, s drift.Storage) []string {
			seed(t, s)
			ds, err := s.Get(repo, drift.GetOptions{})
			must(t, err)
			for _, d := range ds {
				if d.PlanOutput != "" {
					t.Fatalf("PlanOutput persisted for %s", d.ProjectName)
				}
			}
			return names(ds)
		}, []string{"a@dev", "a@main", "b@main"}},
		{"delete project", func(t *testing.T, s drift.Storage) []string {
			seed(t, s)
			must(t, s.Delete(repo, "a"))
			ds, err := s.Get(repo, drift.GetOptions{})
			must(t, err)
			return names(ds)
		}, []string{"b@main"}},
		{"delete repository leaves others", func(t *testing.T, s drift.Storage) []string {
			seed(t, s)
			must(t, s.Delete(repo, ""))
			ds, err := s.Get(repo, drift.GetOptions{})
			must(t, err)
			rest, err := s.Get(other, drift.GetOptions{})
			must(t, err)
			return append(names(ds), names(rest)...)
		}, []string{"c@main"}},
		{"delete unknown is a no-op", func(t *testing.T, s drift.Storage) []string {
			must(t, s.Delete("nope", ""))
			must(t, s.Delete("nope", "x"))
			return []string{}
		}, []string{}},
		{"delete matching by ref", func(t *testing.T, s drift.Storage) []string {
			seed(t, s)
			must(t, s.DeleteMatching(repo, drift.GetOptions{Ref: "dev"}))
			ds, err := s.Get(repo, drift.GetOptions{})
			must(t, err)
			return names(ds)
		}, []string{"a@main", "b@main"}},
		{"delete matching exact honors empty fields", func(t *testing.T, s drift.Storage) []string {
			must(t, s.Store(repo, rec("a", "dir/a", "default", "main")))
			must(t, s.Store(repo, rec("a", "dir/a", "default", "main")))
			noBase := rec("a", "dir/a", "default", "main")
			noBase.BaseBranch = ""
			must(t, s.Store(repo, noBase))
			must(t, s.DeleteMatching(repo, drift.GetOptions{
				ProjectName: "a", Path: "dir/a", Workspace: "default", Ref: "main", Exact: true,
			}))
			ds, err := s.Get(repo, drift.GetOptions{})
			must(t, err)
			for _, d := range ds {
				if d.BaseBranch == "" {
					t.Fatalf("exact delete should only match empty base branch")
				}
			}
			return names(ds)
		}, []string{"a@main"}},
		{"delete matching requires a filter", func(t *testing.T, s drift.Storage) []string {
			seed(t, s)
			if err := s.DeleteMatching(repo, drift.GetOptions{}); err == nil {
				t.Fatal("expected error for empty filters")
			}
			ds, err := s.Get(repo, drift.GetOptions{})
			must(t, err)
			return names(ds)
		}, []string{"a@dev", "a@main", "b@main"}},
		{"get all spans repositories", func(t *testing.T, s drift.Storage) []string {
			seed(t, s)
			all, err := s.GetAll()
			must(t, err)
			var out []string
			for r, ds := range all {
				for _, n := range names(ds) {
					out = append(out, r+"|"+n)
				}
			}
			sort.Strings(out)
			return out
		}, []string{other + "|c@main", repo + "|a@dev", repo + "|a@main", repo + "|b@main"}},
		{"get all omits fully deleted repositories", func(t *testing.T, s drift.Storage) []string {
			seed(t, s)
			must(t, s.Delete(other, ""))
			all, err := s.GetAll()
			must(t, err)
			if _, ok := all[other]; ok {
				t.Fatal("deleted repository still listed")
			}
			return []string{}
		}, []string{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.run(t, newStorage(t))
			sort.Strings(tc.want)
			if !eq(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
