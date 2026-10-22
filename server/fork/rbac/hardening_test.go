package rbac_test

import (
	"encoding/base32"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/fork/oidc"
	"github.com/runatlantis/atlantis/server/fork/rbac"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

func post(e *rbac.Engine, target, body string, id *oidc.Identity) bool {
	r := httptest.NewRequest("POST", target, strings.NewReader(body))
	if id != nil {
		r = r.WithContext(oidc.WithIdentity(r.Context(), *id))
	}
	w := httptest.NewRecorder()
	called := false
	e.ServeHTTP(w, r, func(http.ResponseWriter, *http.Request) { called = true })
	return called
}

// The controllers act on the JSON body; a query parameter must never decide.
func TestRepoSmugglingViaQueryIsIgnored(t *testing.T) {
	e := engine(t, matrixFile())
	id := &oidc.Identity{Subject: "u", Groups: []string{"g-apply"}} // apply on org/a-* only
	for _, p := range []string{"/api/plan", "/api/apply", "/api/drift/detect", "/api/drift/remediate"} {
		Assert(t, !post(e, p+"?repository=org/a-1", `{"Repository":"org/b-1"}`, id), "%s: query must not override body", p)
		Assert(t, post(e, p, `{"Repository":"org/a-1"}`, id), "%s: body in scope is allowed", p)
		// The query is not an alternative source either.
		Assert(t, !post(e, p+"?repository=org/a-1", `{}`, id), "%s: query alone must not grant", p)
	}
	// Token-only clients are checked the same way.
	Assert(t, !post(e, "/api/apply?repository=org/a-1", `{"Repository":"org/b-1"}`, nil), "token smuggling")
}

// Repo names with more than two segments (GitLab subgroups, Azure DevOps) must
// not match a pattern through the hostname slot.
func TestRepoScopeDoesNotLeakAcrossSegments(t *testing.T) {
	e := engine(t, rbac.File{
		Rules:    []rbac.Rule{{Claim: "groups", Value: "g", Role: "apply", Repos: []string{"team-a/*"}}},
		APIToken: rbac.Grant{Role: "apply", Repos: []string{"proj/*"}},
		VCSUsers: []rbac.Grant{{Username: "al", Role: "apply", Repos: []string{"team-a/*"}}},
	})
	id := oidc.Identity{Subject: "u", Groups: []string{"g"}}
	Equals(t, rbac.Apply, e.RoleFor(id, "team-a/sub/x"))
	Equals(t, rbac.None, e.RoleFor(id, "prod/team-a/secret"))
	Equals(t, rbac.Apply, e.TokenRole("proj/x"))
	Equals(t, rbac.None, e.TokenRole("other-org/proj/x"))
	r, ok := e.VCSRole("al", "team-a/x")
	Assert(t, ok && r == rbac.Apply, "in scope")
	r, ok = e.VCSRole("al", "prod/team-a/secret")
	Assert(t, ok && r == rbac.None, "must be out of scope, got %s", r)
}

func TestStaticAssetsOnlyNeedAuthentication(t *testing.T) {
	e := engine(t, matrixFile())
	scoped := &oidc.Identity{Subject: "u", Groups: []string{"g-viewer"}}
	Assert(t, serve(e, "GET", "/static/css/x.css", "", false, scoped), "repo-scoped user loads static assets")
	Assert(t, !serve(e, "GET", "/static/css/x.css", "", false, nil), "token-only client is not a UI user")
}

func TestLoadFileIsStrict(t *testing.T) {
	write := func(y string) string {
		p := filepath.Join(t.TempDir(), "rbac.yaml")
		Ok(t, os.WriteFile(p, []byte(y), 0o600))
		return p
	}
	_, err := rbac.LoadFile(write("default_role: none\nrules:\n  - {claim: groups, value: x, role: admin, repo: [\"org/a\"]}\n"))
	Assert(t, err != nil, "typo repo: instead of repos: must fail")
	_, err = rbac.LoadFile(write("default_role: none\nlocal_users:\n  - {name: r, password_bcrypt: x, role: admin, break_glas: true}\n"))
	Assert(t, err != nil, "unknown key must fail")
	_, err = rbac.LoadFile(write("rules: []\n"))
	Assert(t, err != nil, "missing default_role must fail")
	_, err = rbac.LoadFile(write("default_role: none\nrules:\n  - {claim: groups, value: x, role: admin, repos: [\"org/a\"]}\n"))
	Ok(t, err)
	_, err = rbac.LoadFile(filepath.Join(t.TempDir(), "missing.yaml"))
	Assert(t, err != nil, "missing file must fail")
}

func TestBreakGlassLiveCodes(t *testing.T) {
	raw := []byte("12345678901234567890")
	seed := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	e, err := rbac.New(rbac.File{DefaultRole: "none", LocalUsers: []rbac.LocalUser{
		{Name: "root", PasswordBcrypt: hash(t, "pw"), Role: "admin", BreakGlass: true, TOTPSeedEnv: "S"},
	}}, func(string) string { return seed })
	Ok(t, err)
	e.Logger = logging.NewNoopLogger(t)

	cur := uint64(time.Now().Unix() / 30) // #nosec G115 -- post-1970 time
	req := func(pw, code string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.SetBasicAuth("root", pw)
		r.Header.Set("X-Atlantis-TOTP", code)
		return r
	}
	id, ok := e.Authenticate(req("pw", rbac.TOTPCodeForTest(raw, cur)))
	Assert(t, ok, "current code with the right password")
	Equals(t, rbac.Admin, e.RoleFor(id, ""))
	_, ok = e.Authenticate(req("pw", rbac.TOTPCodeForTest(raw, cur)))
	Assert(t, !ok, "replayed code")
	_, ok = e.Authenticate(req("bad", rbac.TOTPCodeForTest(raw, cur+1)))
	Assert(t, !ok, "wrong password with a fresh code")
	for range 3 {
		_, _ = e.Authenticate(req("pw", "000000"))
	}
	// Five failures so far (replay, bad password, three bad codes): locked.
	_, ok = e.Authenticate(req("pw", rbac.TOTPCodeForTest(raw, cur+1)))
	Assert(t, !ok, "locked out even with a valid code")
	// Unknown users are rejected (and take the dummy-hash path).
	r := httptest.NewRequest("GET", "/", nil)
	r.SetBasicAuth("nobody", "pw")
	_, ok = e.Authenticate(r)
	Assert(t, !ok, "unknown user")
}

// A real VCS allowlist stays authoritative; the RBAC layer only narrows.
func TestCommandCheckerWithRealTeamAllowlist(t *testing.T) {
	inner, err := command.NewTeamAllowlistChecker("devs:*")
	Ok(t, err)
	e := engine(t, rbac.File{VCSUsers: []rbac.Grant{{Username: "al", Role: "plan", Repos: []string{"org/*"}}}})
	c := &rbac.CommandChecker{Inner: inner, Engine: e}
	ctx := func(user string) models.TeamAllowlistCheckerContext {
		return models.TeamAllowlistCheckerContext{BaseRepo: models.Repo{FullName: "org/x"}, User: models.User{Username: user}}
	}
	Assert(t, c.HasRules(), "active")
	Assert(t, c.IsCommandAllowedForAnyTeam(ctx("al"), []string{"devs"}, "plan"), "role and team agree")
	Assert(t, !c.IsCommandAllowedForAnyTeam(ctx("al"), []string{"devs"}, "apply"), "role too low")
	Assert(t, !c.IsCommandAllowedForAnyTeam(ctx("al"), []string{"ops"}, "plan"), "VCS team still required")
	Assert(t, c.IsCommandAllowedForAnyTeam(ctx("stranger"), []string{"devs"}, "apply"), "no entry: VCS decides alone")
	Assert(t, !c.IsCommandAllowedForAnyTeam(ctx("stranger"), []string{"ops"}, "apply"), "no entry, wrong team")
	Equals(t, inner.AllTeams(), c.AllTeams())
	Assert(t, c.IsCommandAllowedForTeam(ctx("al"), "devs", "plan"), "team check delegates")
}
