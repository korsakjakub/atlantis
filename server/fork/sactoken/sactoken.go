// Package sactoken implements scoped service-account tokens for /api/*.
// Fork-only feature, see docs/adr/0006-fork-scoped-service-tokens.md.
package sactoken

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/runatlantis/atlantis/server/logging"
	"go.yaml.in/yaml/v4"
)

const (
	tokenHeader = "X-Atlantis-Token" // nolint: gosec // header name, not a credential
	maxBody     = 10 << 20

	Read  = "read"
	Plan  = "plan"
	Apply = "apply"
)

// Token is one service account as written in the YAML file.
type Token struct {
	ID string `yaml:"id"`
	// SecretSHA256 is the hex SHA-256 of the secret. Secrets are random, so a
	// plain hash is enough; no plaintext is ever stored or logged.
	SecretSHA256 string `yaml:"secret_sha256"`
	// Actions: read, plan and/or apply. They do not imply each other.
	Actions []string `yaml:"actions"`
	// Repos are case-insensitive globs ("*" wildcard) on the repository full
	// name, e.g. "myorg/*". A request without a repository (/api/locks) needs "*".
	Repos []string `yaml:"repos"`

	hash  []byte
	repos []*regexp.Regexp
}

type fileConfig struct {
	Tokens []*Token `yaml:"tokens"`
}

// Authorizer authenticates and authorizes scoped tokens.
type Authorizer struct {
	tokens    []*Token
	apiSecret []byte
	logger    logging.SimpleLogging
}

// Load reads and validates the token file. Any error must stop the server.
func Load(path string, apiSecret []byte, logger logging.SimpleLogging) (*Authorizer, error) {
	if len(apiSecret) == 0 {
		return nil, fmt.Errorf("scoped tokens require --api-secret to be set")
	}
	raw, err := os.ReadFile(path) // nolint: gosec // operator-supplied path
	if err != nil {
		return nil, fmt.Errorf("reading scoped tokens file: %w", err)
	}
	var cfg fileConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parsing scoped tokens file: %w", err)
	}
	if len(cfg.Tokens) == 0 {
		return nil, fmt.Errorf("scoped tokens file defines no tokens")
	}
	seen := map[string]bool{}
	for _, t := range cfg.Tokens {
		if t == nil || t.ID == "" || seen[t.ID] {
			return nil, fmt.Errorf("token ids must be present and unique")
		}
		seen[t.ID] = true
		if t.hash, err = hex.DecodeString(t.SecretSHA256); err != nil || len(t.hash) != sha256.Size {
			return nil, fmt.Errorf("token %q: secret_sha256 must be a hex SHA-256", t.ID)
		}
		if len(t.Actions) == 0 || len(t.Repos) == 0 {
			return nil, fmt.Errorf("token %q: actions and repos are required", t.ID)
		}
		for _, a := range t.Actions {
			if !slices.Contains([]string{Read, Plan, Apply}, a) {
				return nil, fmt.Errorf("token %q: unknown action %q", t.ID, a)
			}
		}
		for _, p := range t.Repos {
			if p == "" || strings.Contains(p, "://") {
				return nil, fmt.Errorf("token %q: invalid repo pattern %q", t.ID, p)
			}
			re, err := regexp.Compile("(?i)^" + strings.ReplaceAll(regexp.QuoteMeta(p), `\*`, ".*") + "$")
			if err != nil {
				return nil, fmt.Errorf("token %q: invalid repo pattern %q: %w", t.ID, p, err)
			}
			t.repos = append(t.repos, re)
		}
	}
	return &Authorizer{tokens: cfg.Tokens, apiSecret: apiSecret, logger: logger}, nil
}

func (t *Token) allows(action, repo string) bool {
	if !slices.Contains(t.Actions, action) {
		return false
	}
	for _, re := range t.repos {
		if re.MatchString(repo) {
			return true
		}
	}
	return false
}

// Classify maps a request to the action it needs and, when the endpoint
// takes one, the repository. ok is false for endpoints no scoped token may
// call (unknown paths are admin-only by default). body is only consulted for
// POST endpoints.
func Classify(method, path string, query func(string) string, body []byte) (action, repo string, ok bool, err error) {
	switch {
	case method == http.MethodGet && path == "/api/locks":
		return Read, "", true, nil
	case method == http.MethodGet && (path == "/api/drift/status" || strings.HasPrefix(path, "/api/drift/remediate")):
		return Read, query("repository"), true, nil
	case method == http.MethodPost && (path == "/api/plan" || path == "/api/apply" ||
		path == "/api/drift/detect" || path == "/api/drift/remediate"):
		var req struct {
			Repository string `json:"repository"`
			Action     string `json:"action"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			return "", "", true, fmt.Errorf("parsing request body: %w", err)
		}
		action = Plan
		if path == "/api/apply" || (path == "/api/drift/remediate" && req.Action == "apply") {
			action = Apply
		}
		return action, req.Repository, true, nil
	}
	return "", "", false, nil
}

// ServeHTTP implements negroni.Handler. Requests whose X-Atlantis-Token is not
// a scoped token (including the global --api-secret) pass through untouched.
// An authorized scoped request gets the global secret substituted so the
// controllers' own check passes; a denied one is answered with 403.
func (a *Authorizer) ServeHTTP(rw http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		next(rw, r)
		return
	}
	tok := a.lookup(r.Header.Get(tokenHeader))
	if tok == nil {
		next(rw, r)
		return
	}

	var body []byte
	if r.Method == http.MethodPost {
		var err error
		if body, err = io.ReadAll(io.LimitReader(r.Body, maxBody+1)); err != nil || len(body) > maxBody {
			a.deny(rw, tok, "", "", "invalid_request", "request body unreadable or too large", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	action, repo, ok, err := Classify(r.Method, r.URL.Path, r.URL.Query().Get, body)
	switch {
	case !ok:
		a.deny(rw, tok, "", "", "endpoint_not_allowed", "endpoint is not available to scoped tokens", http.StatusForbidden)
	case err != nil:
		a.deny(rw, tok, action, "", "invalid_request", "request body is not valid JSON", http.StatusBadRequest)
	case !tok.allows(action, repo):
		a.deny(rw, tok, action, repo, "scope_denied", fmt.Sprintf("token is not scoped for %q on this repository", action), http.StatusForbidden)
	default:
		// Audit line carries only id, action, repo and result.
		a.logger.Info("sa-token audit: token=%q action=%q repo=%q result=allow", tok.ID, action, repo)
		r.Header.Set(tokenHeader, string(a.apiSecret))
		next(rw, r)
	}
}

func (a *Authorizer) deny(rw http.ResponseWriter, tok *Token, action, repo, code, msg string, status int) {
	a.logger.Info("sa-token audit: token=%q action=%q repo=%q result=deny reason=%q", tok.ID, action, repo, code)
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	b, _ := json.Marshal(map[string]any{"error": map[string]string{"code": code, "message": msg}})
	_, _ = rw.Write(append(b, '\n'))
}

// lookup compares the hash of the presented secret against every token so the
// time taken does not reveal which one matched.
func (a *Authorizer) lookup(secret string) *Token {
	if secret == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(secret))
	var found *Token
	for _, t := range a.tokens {
		if subtle.ConstantTimeCompare(sum[:], t.hash) == 1 {
			found = t
		}
	}
	return found
}
