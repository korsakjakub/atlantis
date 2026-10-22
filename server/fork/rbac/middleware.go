package rbac

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/runatlantis/atlantis/server/fork/oidc"
)

const maxPeekBody = 1 << 20

// ActionFor maps a request to the minimum action it needs. Anything not
// listed needs admin, so new endpoints are closed by default.
func ActionFor(method, p string) Action {
	switch {
	case strings.HasPrefix(p, "/api/"):
		switch method + " " + p {
		case "GET /api/locks", "GET /api/drift/status", "GET /api/drift/remediate":
			return ActionRead
		case "POST /api/plan", "POST /api/drift/detect":
			return ActionPlan
		case "POST /api/apply", "POST /api/drift/remediate":
			return ActionApply
		}
		if method == http.MethodGet && strings.HasPrefix(p, "/api/drift/remediate/") {
			return ActionRead
		}
		return ActionAdmin
	case strings.HasPrefix(p, "/debug/"), strings.HasPrefix(p, "/github-app/"):
		return ActionAdmin
	case method == http.MethodGet || method == http.MethodHead:
		return ActionRead // UI pages
	}
	// DELETE /locks, POST /apply/lock, DELETE /apply/unlock and the rest.
	return ActionAdmin
}

func exempt(p string) bool {
	switch p {
	case "/events", "/healthz", "/readyz", "/status":
		return true
	}
	return false
}

// repoOf extracts the repository (owner/repo) a request targets, or "".
// POST bodies are peeked and restored; the controllers still parse them.
// The source must match what the controllers act on: the JSON body for
// POST /api/*, the query for GET. The other one is ignored, never trusted.
func repoOf(r *http.Request, w http.ResponseWriter) (string, bool) {
	if r.Method == http.MethodGet {
		return r.URL.Query().Get("repository"), true
	}
	if r.Method != http.MethodPost || r.Body == nil || !strings.HasPrefix(r.URL.Path, "/api/") {
		return "", true
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPeekBody))
	if err != nil {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return "", false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var req struct{ Repository string }
	_ = json.Unmarshal(body, &req) // malformed JSON: no repo, controller rejects it
	return req.Repository, true
}

// Authorize is the decision point shared by UI, API and (later) approvals.
// id is nil for token-only API clients.
func (e *Engine) Authorize(id *oidc.Identity, a Action, repo string) bool {
	if id != nil {
		return e.RoleFor(*id, repo).Allows(a)
	}
	return e.TokenRole(repo).Allows(a)
}

// ServeHTTP implements negroni.Handler and must run after the OIDC
// middleware, which has already authenticated the request.
func (e *Engine) ServeHTTP(rw http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
	if exempt(r.URL.Path) || strings.HasPrefix(r.URL.Path, "/oauth/") {
		next(rw, r)
		return
	}
	var id *oidc.Identity
	if i, ok := oidc.IdentityFromContext(r.Context()); ok {
		id = &i
	} else if !strings.HasPrefix(r.URL.Path, "/api/") {
		deny(rw, r) // unauthenticated UI request: fail closed
		return
	}
	// Static assets carry no data: any authenticated identity may load them,
	// so repo-scoped users can render the UI.
	if id != nil && strings.HasPrefix(r.URL.Path, "/static/") {
		next(rw, r)
		return
	}
	repo, ok := repoOf(r, rw)
	if !ok {
		return
	}
	if !e.Authorize(id, ActionFor(r.Method, r.URL.Path), repo) {
		if e.Logger != nil {
			e.Logger.Debug("rbac denied %s %s repo %q", r.Method, r.URL.Path, repo)
		}
		deny(rw, r)
		return
	}
	next(rw, r)
}

func deny(rw http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusForbidden)
		_, _ = rw.Write([]byte(`{"error":{"code":"forbidden","message":"insufficient role for this action"}}` + "\n"))
		return
	}
	http.Error(rw, "Forbidden", http.StatusForbidden)
}
