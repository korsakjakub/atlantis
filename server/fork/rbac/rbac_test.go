package rbac_test

import (
	"encoding/base32"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/fork/oidc"
	"github.com/runatlantis/atlantis/server/fork/rbac"
	. "github.com/runatlantis/atlantis/testing"
	"golang.org/x/crypto/bcrypt"
)

var noEnv = func(string) string { return "" }

func engine(t *testing.T, f rbac.File) *rbac.Engine {
	t.Helper()
	if f.DefaultRole == "" {
		f.DefaultRole = "none"
	}
	e, err := rbac.New(f, noEnv)
	Ok(t, err)
	return e
}

var roles = []string{"viewer", "plan", "apply", "approver", "admin"}

// matrixFile gives group "g-<role>" that role on org/a-* only, plus a global admin.
func matrixFile() rbac.File {
	f := rbac.File{APIToken: rbac.Grant{Role: "apply", Repos: []string{"org/a-*"}}}
	for _, r := range roles {
		f.Rules = append(f.Rules, rbac.Rule{Claim: "groups", Value: "g-" + r, Role: r, Repos: []string{"org/a-*"}})
	}
	f.Rules = append(f.Rules, rbac.Rule{Claim: "email", Value: "*@root.example", Role: "admin"})
	return f
}

// TestMatrix is the role x action x repo matrix through the real middleware.
func TestMatrix(t *testing.T) {
	e := engine(t, matrixFile())
	actions := []struct {
		name, method, path string
		min                rbac.Role
		repoInQuery        bool
	}{
		{"read locks", "GET", "/api/locks", rbac.Viewer, false},
		{"read drift status", "GET", "/api/drift/status", rbac.Viewer, true},
		{"read remediation", "GET", "/api/drift/remediate/abc", rbac.Viewer, true},
		{"ui page", "GET", "/", rbac.Viewer, false},
		{"plan", "POST", "/api/plan", rbac.Plan, true},
		{"detect drift", "POST", "/api/drift/detect", rbac.Plan, true},
		{"apply", "POST", "/api/apply", rbac.Apply, true},
		{"remediate", "POST", "/api/drift/remediate", rbac.Apply, true},
		{"unknown api", "GET", "/api/unknown", rbac.Admin, true},
		// Repo-less endpoints: only unscoped rules can grant them.
		{"delete lock", "DELETE", "/locks", rbac.Admin, false},
		{"global apply lock", "POST", "/apply/lock", rbac.Admin, false},
		{"pprof", "GET", "/debug/pprof/", rbac.Admin, false},
	}
	repos := []struct {
		name, repo string
		inScope    bool
	}{{"in-scope", "org/a-1", true}, {"out-of-scope", "org/b-1", false}}

	for _, role := range roles {
		for _, a := range actions {
			for _, rp := range repos {
				want := rp.inScope && mustRole(t, role) >= a.min
				// Repo-less endpoints can only be granted by an unscoped rule.
				if !a.repoInQuery {
					want = false
				}
				t.Run(role+"/"+a.name+"/"+rp.name, func(t *testing.T) {
					Equals(t, want, serve(e, a.method, a.path, rp.repo, a.repoInQuery, &oidc.Identity{Subject: "u", Groups: []string{"g-" + role}}))
				})
			}
		}
	}
	// A global admin (unscoped rule) may do everything, repo-less included.
	root := &oidc.Identity{Subject: "r", Email: "ann@root.example"}
	for _, a := range actions {
		Assert(t, serve(e, a.method, a.path, "org/b-1", a.repoInQuery, root), "global admin denied %s", a.name)
	}
}

func mustRole(t *testing.T, s string) rbac.Role {
	r, err := rbac.ParseRole(s)
	Ok(t, err)
	return r
}

func serve(e *rbac.Engine, method, path, repo string, inQuery bool, id *oidc.Identity) bool {
	target := path
	var body io.Reader
	if inQuery && method == "POST" {
		body = strings.NewReader(`{"Repository":"` + repo + `"}`) // controllers read the repo from the body
	} else if inQuery {
		target += "?repository=" + repo
	}
	r := httptest.NewRequest(method, target, body)
	if id != nil {
		r = r.WithContext(oidc.WithIdentity(r.Context(), *id))
	}
	w := httptest.NewRecorder()
	called := false
	e.ServeHTTP(w, r, func(http.ResponseWriter, *http.Request) { called = true })
	return called && w.Code == http.StatusOK
}

func TestMiddlewarePostBodyRepoAndTokenRole(t *testing.T) {
	e := engine(t, matrixFile())
	post := func(body string) (bool, string) {
		r := httptest.NewRequest("POST", "/api/apply", strings.NewReader(body)) // no identity: token-only client
		w := httptest.NewRecorder()
		var seen string
		e.ServeHTTP(w, r, func(_ http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			seen = string(b)
		})
		return w.Code == http.StatusOK, seen
	}
	ok, seen := post(`{"repository":"org/a-1"}`)
	Assert(t, ok, "token role apply on in-scope repo should pass")
	Equals(t, `{"repository":"org/a-1"}`, seen) // body restored for the controller
	ok, _ = post(`{"repository":"org/b-1"}`)
	Assert(t, !ok, "token must be scoped")
	ok, _ = post(`not json`)
	Assert(t, !ok, "repo-less request must not use a scoped token")
	// Token-only clients never reach UI pages.
	Assert(t, !serve(e, "GET", "/", "", false, nil), "token on UI")
}

func TestExemptPaths(t *testing.T) {
	e := engine(t, matrixFile())
	for _, p := range []string{"/events", "/healthz", "/readyz", "/status"} {
		Assert(t, serve(e, "POST", p, "", false, nil), "%s must stay open", p)
	}
}

func TestClaimMapping(t *testing.T) {
	e := engine(t, rbac.File{
		DefaultRole: "viewer",
		Rules: []rbac.Rule{
			{Claim: "groups", Value: "platform", Role: "plan"},
			{Claim: "groups", Value: "sre-*", Role: "approver", Repos: []string{"org/infra"}},
			{Claim: "email", Value: "*@corp.example", Role: "apply", Repos: []string{"org/*", "!org/secret"}},
			{Claim: "sub", Value: "svc-1", Role: "admin"},
		},
	})
	id := func(f func(*oidc.Identity)) oidc.Identity { i := oidc.Identity{Subject: "x"}; f(&i); return i }
	cases := []struct {
		name string
		id   oidc.Identity
		repo string
		want rbac.Role
	}{
		{"default", id(func(*oidc.Identity) {}), "org/x", rbac.Viewer},
		{"group", id(func(i *oidc.Identity) { i.Groups = []string{"a", "platform"} }), "org/x", rbac.Plan},
		{"group glob scoped", id(func(i *oidc.Identity) { i.Groups = []string{"sre-eu"} }), "org/infra", rbac.Approver},
		{"group glob other repo falls back", id(func(i *oidc.Identity) { i.Groups = []string{"sre-eu"} }), "org/x", rbac.Viewer},
		{"email glob + exclusion", id(func(i *oidc.Identity) { i.Email = "a@corp.example" }), "org/x", rbac.Apply},
		{"excluded repo", id(func(i *oidc.Identity) { i.Email = "a@corp.example" }), "org/secret", rbac.Viewer},
		{"highest wins", id(func(i *oidc.Identity) { i.Email = "a@corp.example"; i.Groups = []string{"platform"} }), "org/x", rbac.Apply},
		{"sub", id(func(i *oidc.Identity) { i.Subject = "svc-1" }), "", rbac.Admin},
		{"repo-less ignores scoped rules", id(func(i *oidc.Identity) { i.Groups = []string{"sre-eu"} }), "", rbac.Viewer},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { Equals(t, c.want, e.RoleFor(c.id, c.repo)) })
	}
	// No default_role: unmatched principals get nothing.
	none := engine(t, rbac.File{})
	Equals(t, rbac.None, none.RoleFor(oidc.Identity{Subject: "x"}, "org/x"))
}

func TestValidationFailsFast(t *testing.T) {
	dupUser := rbac.LocalUser{Name: "a", PasswordBcrypt: hashNoT(), Role: "viewer"}
	bad := map[string]rbac.File{
		"role":        {DefaultRole: "root"},
		"no default":  {},
		"dup vcs":     {VCSUsers: []rbac.Grant{{Username: "Al", Role: "plan"}, {Username: "al", Role: "plan"}}},
		"dup local":   {LocalUsers: []rbac.LocalUser{dupUser, dupUser}},
		"not bcrypt":  {LocalUsers: []rbac.LocalUser{{Name: "a", PasswordBcrypt: "plain", Role: "viewer"}}},
		"pw only":     {LocalUsers: []rbac.LocalUser{{Name: "a", PasswordBcrypt: hashNoT(), Role: "plan"}}},
		"claim":       {Rules: []rbac.Rule{{Claim: "foo", Value: "x", Role: "admin"}}},
		"pattern":     {Rules: []rbac.Rule{{Claim: "groups", Value: "[", Role: "admin"}}},
		"empty value": {Rules: []rbac.Rule{{Claim: "groups", Role: "admin"}}},
		"rule role":   {Rules: []rbac.Rule{{Claim: "groups", Value: "x", Role: "none"}}},
		"token role":  {APIToken: rbac.Grant{Role: "x"}},
		"vcs user":    {VCSUsers: []rbac.Grant{{Role: "plan"}}},
		"local no pw": {LocalUsers: []rbac.LocalUser{{Name: "a", Role: "admin"}}},
		"no seed env": {LocalUsers: []rbac.LocalUser{{Name: "a", PasswordBcrypt: hashNoT(), Role: "admin", BreakGlass: true}}},
		"bad seed":    {LocalUsers: []rbac.LocalUser{{Name: "a", PasswordBcrypt: hashNoT(), Role: "admin", BreakGlass: true, TOTPSeedEnv: "SEED"}}},
	}
	for name, f := range bad {
		if name != "no default" && f.DefaultRole == "" {
			f.DefaultRole = "none" // so each case fails for its own reason
		}
		_, err := rbac.New(f, noEnv)
		Assert(t, err != nil, "%s: expected a validation error", name)
	}
}

// fakeInner is a VCS allowlist stand-in: only team "devs" may run anything.
type fakeInner struct{ rules bool }

func (f fakeInner) HasRules() bool { return f.rules }
func (f fakeInner) IsCommandAllowedForTeam(_ models.TeamAllowlistCheckerContext, team, _ string) bool {
	return team == "devs"
}
func (f fakeInner) IsCommandAllowedForAnyTeam(c models.TeamAllowlistCheckerContext, teams []string, cmd string) bool {
	for _, t := range teams {
		if f.IsCommandAllowedForTeam(c, t, cmd) {
			return true
		}
	}
	return false
}
func (f fakeInner) AllTeams() []string { return []string{"devs"} }

func TestCommandCheckerMatrix(t *testing.T) {
	var vcs []rbac.Grant
	for _, r := range roles {
		vcs = append(vcs, rbac.Grant{Username: "u-" + r, Role: r, Repos: []string{"org/a-*"}})
	}
	e := engine(t, rbac.File{VCSUsers: vcs})
	cmds := []struct {
		name string
		min  rbac.Role
	}{
		{command.Plan.String(), rbac.Plan}, {command.Unlock.String(), rbac.Plan},
		{command.Apply.String(), rbac.Apply}, {command.Import.String(), rbac.Apply}, {command.State.String(), rbac.Apply},
		{command.ApprovePolicies.String(), rbac.Approver},
	}
	for _, innerRules := range []bool{true, false} {
		c := &rbac.CommandChecker{Inner: fakeInner{innerRules}, Engine: e}
		Assert(t, c.HasRules(), "vcs_users make the checker active")
		for _, role := range roles {
			for _, cmd := range cmds {
				for _, repo := range []string{"org/a-1", "org/b-1"} {
					ctx := models.TeamAllowlistCheckerContext{BaseRepo: models.Repo{FullName: repo}, User: models.User{Username: "U-" + role}}
					want := repo == "org/a-1" && mustRole(t, role) >= cmd.min
					if innerRules { // the VCS allowlist still has to agree
						Equals(t, false, c.IsCommandAllowedForAnyTeam(ctx, []string{"ops"}, cmd.name))
						Equals(t, want, c.IsCommandAllowedForAnyTeam(ctx, []string{"devs"}, cmd.name))
					} else {
						Equals(t, want, c.IsCommandAllowedForAnyTeam(ctx, nil, cmd.name))
					}
				}
			}
		}
		// A VCS user without a vcs_users entry is decided by the VCS checker alone.
		ctx := models.TeamAllowlistCheckerContext{BaseRepo: models.Repo{FullName: "org/b-1"}, User: models.User{Username: "stranger"}}
		Equals(t, !innerRules, c.IsCommandAllowedForAnyTeam(ctx, []string{"ops"}, "apply"))
		Assert(t, c.IsCommandAllowedForAnyTeam(ctx, []string{"devs"}, "apply"), "stranger in an allowed team")
	}
	// Without vcs_users and without inner rules nothing changes.
	idle := &rbac.CommandChecker{Engine: engine(t, rbac.File{})}
	Assert(t, !idle.HasRules(), "must stay inert")
}

func hashNoT() string {
	b, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	return string(b)
}

func hash(t *testing.T, pw string) string {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	Ok(t, err)
	return string(b)
}

// TestBreakGlass drives Authenticate. The stale-code case uses the RFC 6238
// SHA1 test vector; live codes are covered in TestBreakGlassLiveCodes.
func TestBreakGlass(t *testing.T) {
	seed := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	env := func(string) string { return seed }
	e, err := rbac.New(rbac.File{DefaultRole: "none", LocalUsers: []rbac.LocalUser{
		{Name: "root", PasswordBcrypt: hash(t, "pw"), Role: "admin", BreakGlass: true, TOTPSeedEnv: "S"},
		{Name: "ci", PasswordBcrypt: hash(t, "pw"), Role: "viewer"},
	}}, env)
	Ok(t, err)

	req := func(user, pw, code string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.SetBasicAuth(user, pw)
		if code != "" {
			r.Header.Set("X-Atlantis-TOTP", code)
		}
		return r
	}
	// Non break-glass local user: password only.
	id, ok := e.Authenticate(req("ci", "pw", ""))
	Assert(t, ok, "password only account")
	Equals(t, rbac.Viewer, e.RoleFor(id, ""))
	_, ok = e.Authenticate(req("ci", "bad", ""))
	Assert(t, !ok, "wrong password")
	_, ok = e.Authenticate(req("nobody", "pw", ""))
	Assert(t, !ok, "unknown user")

	// Break-glass: a missing/wrong code is rejected even with the right password.
	_, ok = e.Authenticate(req("root", "pw", ""))
	Assert(t, !ok, "missing TOTP")
	_, ok = e.Authenticate(req("root", "pw", "000000"))
	Assert(t, !ok, "wrong TOTP")
	// RFC 6238 vector (T=59s -> 94287082 -> 6 digits 287082) is far in the past, so
	// the live clock rejects it: stale codes never work.
	_, ok = e.Authenticate(req("root", "pw", "287082"))
	Assert(t, !ok, "stale TOTP")
}

func TestTOTPAcceptsCurrentCodeOnceAndLocksOut(t *testing.T) {
	g := rbac.NewTOTPGuardForTest()
	seed := []byte("12345678901234567890")
	now := time.Unix(59, 0)
	// RFC 6238 vector: step 1 is 59s / 30s.
	const step = 1
	Equals(t, "287082", rbac.TOTPCodeForTest(seed, step))
	Assert(t, g.Check("u", seed, "287082", now), "valid code")
	Assert(t, !g.Check("u", seed, "287082", now), "replay must fail")
	for range 5 {
		g.Fail("u", now)
	}
	Assert(t, g.Locked("u", now), "locked after repeated failures")
	Assert(t, !g.Locked("u", now.Add(2*time.Minute)), "lock expires")
}
