package sactoken_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/runatlantis/atlantis/server/fork/sactoken"
	"github.com/runatlantis/atlantis/server/logging"
)

func hash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func load(t *testing.T, yaml string) (*sactoken.Authorizer, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.yaml")
	if err := os.WriteFile(p, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return sactoken.Load(p, []byte("global"), logging.NewNoopLogger(t))
}

func entry(id, secret, actions, repos string) string {
	return "- id: " + id + "\n  secret_sha256: " + hash(secret) + "\n  actions: " + actions + "\n  repos: " + repos + "\n"
}

func TestLoadValidation(t *testing.T) {
	for name, y := range map[string]string{
		"bad hash":     "tokens:\n- id: a\n  secret_sha256: zz\n  actions: [plan]\n  repos: ['*']\n",
		"bad action":   "tokens:\n" + entry("a", "x", "[admin]", "['*']"),
		"no repos":     "tokens:\n- id: a\n  secret_sha256: " + hash("x") + "\n  actions: [plan]\n",
		"duplicate id": "tokens:\n" + entry("a", "x", "[plan]", "['*']") + entry("a", "y", "[plan]", "['*']"),
		"no tokens":    "tokens: []\n",
	} {
		if _, err := load(t, y); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if _, err := sactoken.Load("x", nil, nil); err == nil {
		t.Error("expected error without api secret")
	}
}

func TestEnforcement(t *testing.T) {
	a, err := load(t, "tokens:\n"+
		entry(`"ci:plan-only"`, "plan", "[plan, read]", `["myorg/*"]`)+
		entry(`"ci:all"`, "all", "[plan, apply, read]", `["*"]`))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, token, method, path, body string
		want                            int
	}{
		{"plan ok", "plan", "POST", "/api/plan", `{"repository":"myorg/r"}`, 200},
		{"plan other repo", "plan", "POST", "/api/plan", `{"repository":"other/r"}`, 403},
		{"apply denied", "plan", "POST", "/api/apply", `{"repository":"myorg/r"}`, 403},
		{"remediate apply denied", "plan", "POST", "/api/drift/remediate", `{"repository":"myorg/r","action":"apply"}`, 403},
		{"remediate plan ok", "plan", "POST", "/api/drift/remediate", `{"repository":"myorg/r","action":"plan"}`, 200},
		{"read drift ok", "plan", "GET", "/api/drift/status?repository=MyOrg/r", "", 200},
		{"locks need global repos", "plan", "GET", "/api/locks", "", 403},
		{"locks ok", "all", "GET", "/api/locks", "", 200},
		{"apply ok", "all", "POST", "/api/apply", `{"repository":"x/y"}`, 200},
		{"unknown endpoint", "all", "GET", "/api/other", "", 403},
		{"bad json", "plan", "POST", "/api/plan", `{`, 400},
		{"global secret untouched", "global", "POST", "/api/apply", `{"repository":"x/y"}`, 200},
		{"unknown token passes through", "nope", "GET", "/api/locks", "", 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var called bool
			var gotToken string
			next := func(_ http.ResponseWriter, r *http.Request) {
				called = true
				gotToken = r.Header.Get("X-Atlantis-Token")
			}
			req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
			req.Header.Set("X-Atlantis-Token", c.token)
			rec := httptest.NewRecorder()
			a.ServeHTTP(rec, req, next)
			if called != (c.want == 200) {
				t.Fatalf("next called=%v, want status %d (%s)", called, c.want, rec.Body)
			}
			if !called && rec.Code != c.want {
				t.Fatalf("got %d want %d: %s", rec.Code, c.want, rec.Body)
			}
			if called && (c.token == "plan" || c.token == "all") && gotToken != "global" {
				t.Fatalf("scoped token not swapped for global secret: %q", gotToken)
			}
		})
	}
}
