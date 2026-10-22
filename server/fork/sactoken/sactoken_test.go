package sactoken_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/runatlantis/atlantis/server/fork/sactoken"
	"github.com/runatlantis/atlantis/server/logging"
)

const globalSecret = "global-secret-value"

func hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// capLog records Info lines; every other method is the no-op logger's.
type capLog struct {
	logging.SimpleLogging
	lines []string
}

func (c *capLog) Info(format string, a ...any) { c.lines = append(c.lines, fmt.Sprintf(format, a...)) }

func load(t *testing.T, yaml string) (*sactoken.Authorizer, *capLog, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.yaml")
	if err := os.WriteFile(p, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	l := &capLog{SimpleLogging: logging.NewNoopLogger(t)}
	a, err := sactoken.Load(p, []byte(globalSecret), l)
	return a, l, err
}

func entry(id, secret, actions, repos string) string {
	return "- id: " + id + "\n  secret_sha256: " + hash(secret) + "\n  actions: " + actions + "\n  repos: " + repos + "\n"
}

func TestLoadValidation(t *testing.T) {
	for name, y := range map[string]string{
		"bad hash":          "tokens:\n- id: a\n  secret_sha256: zz\n  actions: [plan]\n  repos: ['*']\n",
		"bad action":        "tokens:\n" + entry("a", "x", "[admin]", "['*']"),
		"no repos":          "tokens:\n- id: a\n  secret_sha256: " + hash("x") + "\n  actions: [plan]\n",
		"duplicate id":      "tokens:\n" + entry("a", "x", "[plan]", "['*']") + entry("a", "y", "[plan]", "['*']"),
		"duplicate secret":  "tokens:\n" + entry("a", "x", "[plan]", "['*']") + entry("b", "x", "[plan]", "['*']"),
		"equals api secret": "tokens:\n" + entry("a", globalSecret, "[plan]", "['*']"),
		"unknown field":     "tokens:\n" + entry("a", "x", "[plan]", "['*']") + "  scopes: [apply]\n",
		"wildcard owner":    "tokens:\n" + entry("a", "x", "[plan]", "['*/r']"),
		"partial owner":     "tokens:\n" + entry("a", "x", "[plan]", "['my*/r']"),
		"no slash":          "tokens:\n" + entry("a", "x", "[plan]", "['myorg']"),
		"url":               "tokens:\n" + entry("a", "x", "[plan]", "['https://x/y']"),
		"no tokens":         "tokens: []\n",
	} {
		if _, _, err := load(t, y); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if _, err := sactoken.Load("x", nil, nil); err == nil {
		t.Error("expected error without api secret")
	}
}

type tc struct {
	name, token, method, target, body string
	want                              int
}

func TestEnforcement(t *testing.T) {
	secrets := map[string]string{
		"plan": "secret-plan-xyz", "read": "secret-read-xyz", "apply": "secret-apply-xyz",
		"all": "secret-all-xyz", "nested": "secret-nested-xyz",
	}
	a, _, err := load(t, "tokens:\n"+
		entry(`"ci:plan-only"`, secrets["plan"], "[plan, read]", `["myorg/*"]`)+
		entry(`"ci:read-only"`, secrets["read"], "[read]", `["myorg/*"]`)+
		entry(`"ci:apply-only"`, secrets["apply"], "[apply]", `["myorg/*"]`)+
		entry(`"ci:nested"`, secrets["nested"], "[plan]", `["group/**"]`)+
		entry(`"ci:all"`, secrets["all"], "[plan, apply, read]", `["*"]`))
	if err != nil {
		t.Fatal(err)
	}
	cases := []tc{
		// plan-only
		{"plan ok", "plan", "POST", "/api/plan", `{"repository":"myorg/r"}`, 200},
		{"plan other repo", "plan", "POST", "/api/plan", `{"repository":"other/r"}`, 403},
		{"plan cross-org prefix", "plan", "POST", "/api/plan", `{"repository":"myorg-evil/r"}`, 403},
		{"plan cross-org suffix", "plan", "POST", "/api/plan", `{"repository":"myorgx/r"}`, 403},
		{"plan star stays in one segment", "plan", "POST", "/api/plan", `{"repository":"myorg/a/b"}`, 403},
		{"plan query does not override body", "plan", "POST", "/api/plan?repository=myorg/r", `{"repository":"other/r"}`, 403},
		{"plan duplicate key, last wins", "plan", "POST", "/api/plan", `{"repository":"myorg/r","Repository":"other/r"}`, 403},
		{"apply denied", "plan", "POST", "/api/apply", `{"repository":"myorg/r"}`, 403},
		{"detect ok", "plan", "POST", "/api/drift/detect", `{"repository":"myorg/r"}`, 200},
		{"detect other repo", "plan", "POST", "/api/drift/detect", `{"repository":"other/r"}`, 403},
		{"remediate apply denied", "plan", "POST", "/api/drift/remediate", `{"repository":"myorg/r","action":"apply"}`, 403},
		{"remediate plan ok", "plan", "POST", "/api/drift/remediate", `{"repository":"myorg/r","action":"plan"}`, 200},
		{"remediate without action is plan", "plan", "POST", "/api/drift/remediate", `{"repository":"myorg/r"}`, 200},
		{"drift status ok, case-insensitive", "plan", "GET", "/api/drift/status?repository=MyOrg/r", "", 200},
		{"remediate list ok", "plan", "GET", "/api/drift/remediate?repository=myorg/r", "", 200},
		{"remediate result other repo", "plan", "GET", "/api/drift/remediate/abc?repository=other/r", "", 403},
		{"remediate result ok", "plan", "GET", "/api/drift/remediate/abc?repository=myorg/r", "", 200},
		{"remediate nested path denied", "plan", "GET", "/api/drift/remediate/abc/x?repository=myorg/r", "", 403},
		{"remediate lookalike path denied", "plan", "GET", "/api/drift/remediateX?repository=myorg/r", "", 403},
		{"locks need lone star", "plan", "GET", "/api/locks", "", 403},
		{"unknown endpoint", "all", "GET", "/api/other", "", 403},
		{"wrong method", "all", "DELETE", "/api/plan", "", 403},
		{"bad json", "plan", "POST", "/api/plan", `{`, 400},
		// read-only
		{"read: plan denied", "read", "POST", "/api/plan", `{"repository":"myorg/r"}`, 403},
		{"read: apply denied", "read", "POST", "/api/apply", `{"repository":"myorg/r"}`, 403},
		{"read: detect denied", "read", "POST", "/api/drift/detect", `{"repository":"myorg/r"}`, 403},
		{"read: remediate denied", "read", "POST", "/api/drift/remediate", `{"repository":"myorg/r"}`, 403},
		{"read: drift status ok", "read", "GET", "/api/drift/status?repository=myorg/r", "", 200},
		// apply-only
		{"apply: plan denied", "apply", "POST", "/api/plan", `{"repository":"myorg/r"}`, 403},
		{"apply: apply ok", "apply", "POST", "/api/apply", `{"repository":"myorg/r"}`, 200},
		{"apply: remediate apply ok", "apply", "POST", "/api/drift/remediate", `{"repository":"myorg/r","action":"apply"}`, 200},
		// ** spans segments
		{"nested ok", "nested", "POST", "/api/plan", `{"repository":"group/sub/r"}`, 200},
		{"nested other group", "nested", "POST", "/api/plan", `{"repository":"groupx/sub/r"}`, 403},
		// lone star
		{"locks ok", "all", "GET", "/api/locks", "", 200},
		{"all: apply ok", "all", "POST", "/api/apply", `{"repository":"x/y/z"}`, 200},
		// pass-through
		{"global secret untouched", globalSecret, "POST", "/api/apply", `{"repository":"x/y"}`, 200},
		{"unknown token passes through", "nope", "GET", "/api/locks", "", 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			secret, scoped := secrets[c.token]
			if !scoped {
				secret = c.token
			}
			var called bool
			var gotToken, gotBody string
			next := func(_ http.ResponseWriter, r *http.Request) {
				called = true
				gotToken = r.Header.Get("X-Atlantis-Token")
				b, _ := io.ReadAll(r.Body)
				gotBody = string(b)
			}
			req := httptest.NewRequest(c.method, c.target, strings.NewReader(c.body))
			req.Header.Set("X-Atlantis-Token", secret)
			rec := httptest.NewRecorder()
			a.ServeHTTP(rec, req, next)
			if called != (c.want == 200) {
				t.Fatalf("next called=%v, want status %d (got %d %s)", called, c.want, rec.Code, rec.Body)
			}
			if !called && rec.Code != c.want {
				t.Fatalf("got %d want %d: %s", rec.Code, c.want, rec.Body)
			}
			if called && gotBody != c.body {
				t.Fatalf("next got body %q, want %q", gotBody, c.body)
			}
			if called && scoped && gotToken != globalSecret {
				t.Fatalf("scoped token not swapped for global secret: %q", gotToken)
			}
			if called && !scoped && gotToken != secret {
				t.Fatalf("pass-through token changed: %q", gotToken)
			}
		})
	}
}

func TestAuditLog(t *testing.T) {
	const secret, body = "secret-audit-xyz", `{"repository":"myorg/r","note":"BODYMARKER"}`
	a, l, err := load(t, "tokens:\n"+entry(`"ci:plan-only"`, secret, "[plan]", `["myorg/*"]`))
	if err != nil {
		t.Fatal(err)
	}
	next := func(http.ResponseWriter, *http.Request) {}
	do := func(path string) {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("X-Atlantis-Token", secret)
		a.ServeHTTP(httptest.NewRecorder(), req, next)
	}
	do("/api/plan")
	do("/api/apply")
	do("/api/admin-thing")
	if len(l.lines) != 3 {
		t.Fatalf("want 3 audit lines, got %q", l.lines)
	}
	for i, want := range [][]string{
		{`token="ci:plan-only"`, `action="plan"`, `repo="myorg/r"`, `result=allow`, `path="/api/plan"`},
		{`token="ci:plan-only"`, `action="apply"`, `repo="myorg/r"`, `result=deny`, `reason="scope_denied"`},
		{`token="ci:plan-only"`, `method="POST"`, `path="/api/admin-thing"`, `result=deny`, `reason="endpoint_not_allowed"`},
	} {
		for _, w := range want {
			if !strings.Contains(l.lines[i], w) {
				t.Errorf("line %d %q missing %q", i, l.lines[i], w)
			}
		}
	}
	for _, line := range l.lines {
		for _, bad := range []string{secret, hash(secret), "BODYMARKER", globalSecret} {
			if strings.Contains(line, bad) {
				t.Errorf("audit line leaks %q: %s", bad, line)
			}
		}
	}
}
