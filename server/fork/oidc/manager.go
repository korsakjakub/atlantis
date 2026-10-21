package oidc

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"github.com/runatlantis/atlantis/server/logging"
	"golang.org/x/oauth2"
)

const (
	loginPrefix    = "/oauth/login/"
	callbackPath   = "/oauth/callback"
	logoutPath     = "/oauth/logout"
	tokenHeader    = "X-Atlantis-Token" // #nosec G101 -- header name, not a credential
	pendingTTL     = 10 * time.Minute
	stateCookieExt = "_state"
)

// Options are the runtime dependencies of a Manager.
type Options struct {
	Logger logging.SimpleLogging
	// APISecret is the configured --api-secret. When set, session
	// authenticated /api/ requests are forwarded to the API controllers with
	// it, so the controllers' own token check keeps working.
	APISecret []byte
	// AllowAPIToken lets /api/ requests that carry a valid X-Atlantis-Token
	// through without a session (machine clients). Default false.
	AllowAPIToken bool
	// HTTPClient is used for discovery and token exchange. Optional.
	HTTPClient *http.Client
	// Now overrides the clock. Optional.
	Now func() time.Time
}

// Manager authenticates web UI and API requests with one OIDC preset.
type Manager struct {
	cfg      *Config
	name     string
	preset   Preset
	verifier *gooidc.IDTokenVerifier
	oauth    *oauth2.Config
	store    *store
	opts     Options
	now      func() time.Time
	// cookies are marked Secure unless the redirect URL is plain http,
	// which only happens in local development.
	secureCookie bool
}

// New performs OIDC discovery for the named preset and returns a Manager.
func New(ctx context.Context, cfg *Config, preset string, opts Options) (*Manager, error) {
	p, ok := cfg.Presets[preset]
	if !ok {
		return nil, fmt.Errorf("oidc preset %q not found in config", preset)
	}
	if err := p.validate(preset); err != nil {
		return nil, err
	}
	ru, err := url.Parse(p.RedirectURL)
	if err != nil || ru.Host == "" {
		return nil, fmt.Errorf("preset %q: invalid redirect_url %q", preset, p.RedirectURL)
	}
	if opts.HTTPClient != nil {
		ctx = gooidc.ClientContext(ctx, opts.HTTPClient)
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	provider, err := gooidc.NewProvider(ctx, p.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery for preset %q: %w", preset, err)
	}
	return &Manager{
		cfg:      cfg,
		name:     preset,
		preset:   p,
		verifier: provider.Verifier(&gooidc.Config{ClientID: p.ClientID, Now: now}),
		oauth: &oauth2.Config{
			ClientID:     p.ClientID,
			ClientSecret: p.secret(),
			Endpoint:     provider.Endpoint(),
			RedirectURL:  p.RedirectURL,
			Scopes:       p.Scopes,
		},
		store:        newStore(now),
		opts:         opts,
		now:          now,
		secureCookie: ru.Scheme == "https",
	}, nil
}

// ServeHTTP implements negroni.Handler. It serves the /oauth/ endpoints and
// protects everything else except VCS webhooks and health endpoints.
func (m *Manager) ServeHTTP(rw http.ResponseWriter, r *http.Request, next http.HandlerFunc) {
	path := r.URL.Path
	switch {
	case strings.HasPrefix(path, loginPrefix):
		m.login(rw, r)
		return
	case path == callbackPath:
		m.callback(rw, r)
		return
	case path == logoutPath:
		m.logout(rw, r)
		return
	case exempt(path):
		// Webhooks and probes are never behind the login.
		next(rw, r)
		return
	}

	id, hasSession := m.sessionIdentity(r)
	if hasSession {
		r = r.WithContext(WithIdentity(r.Context(), id))
	}

	if strings.HasPrefix(path, "/api/") {
		if m.opts.AllowAPIToken && len(m.opts.APISecret) > 0 &&
			subtle.ConstantTimeCompare([]byte(r.Header.Get(tokenHeader)), m.opts.APISecret) == 1 {
			next(rw, r)
			return
		}
		if !hasSession {
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusUnauthorized)
			_, _ = rw.Write([]byte(`{"error":{"code":"unauthorized","message":"OIDC login required"}}` + "\n"))
			return
		}
		// Never trust a client supplied token on a session request.
		r.Header.Del(tokenHeader)
		if len(m.opts.APISecret) > 0 {
			r.Header.Set(tokenHeader, string(m.opts.APISecret))
		}
		next(rw, r)
		return
	}

	if hasSession {
		next(rw, r)
		return
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		http.Redirect(rw, r, loginPrefix+url.PathEscape(m.name)+"?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusFound)
		return
	}
	http.Error(rw, "Unauthorized", http.StatusUnauthorized)
}

func exempt(path string) bool {
	switch path {
	case "/events", "/healthz", "/readyz", "/status":
		return true
	}
	return false
}

func (m *Manager) sessionIdentity(r *http.Request) (Identity, bool) {
	c, err := r.Cookie(m.cfg.Session.CookieName)
	if err != nil || c.Value == "" {
		return Identity{}, false
	}
	return m.store.lookup(c.Value)
}

func (m *Manager) login(rw http.ResponseWriter, r *http.Request) {
	name, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, loginPrefix))
	if r.Method != http.MethodGet || err != nil || name != m.name {
		http.NotFound(rw, r)
		return
	}
	state, err1 := randomToken()
	nonce, err2 := randomToken()
	if err := errors.Join(err1, err2); err != nil {
		m.fail(rw, http.StatusInternalServerError, "generating login state", err)
		return
	}
	verifier := oauth2.GenerateVerifier()
	m.store.putPending(state, pending{
		nonce:    nonce,
		verifier: verifier,
		next:     safeNext(r.URL.Query().Get("next")),
		expires:  m.now().Add(pendingTTL),
	})
	// Binding the state to the browser prevents login CSRF.
	http.SetCookie(rw, m.cookie(m.cfg.Session.CookieName+stateCookieExt, state, "/oauth/", m.now().Add(pendingTTL)))

	opts := []oauth2.AuthCodeOption{
		oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("nonce", nonce),
	}
	for k, v := range m.preset.AuthParams {
		opts = append(opts, oauth2.SetAuthURLParam(k, v))
	}
	http.Redirect(rw, r, m.oauth.AuthCodeURL(state, opts...), http.StatusFound)
}

func (m *Manager) callback(rw http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	sc, err := r.Cookie(m.cfg.Session.CookieName + stateCookieExt)
	// The state is consumed whatever happens next, so it cannot be replayed.
	p, found := m.store.takePending(state)
	http.SetCookie(rw, m.expire(m.cfg.Session.CookieName+stateCookieExt, "/oauth/"))
	if state == "" || err != nil || subtle.ConstantTimeCompare([]byte(sc.Value), []byte(state)) != 1 || !found {
		m.fail(rw, http.StatusBadRequest, "invalid or replayed login state", nil)
		return
	}
	if e := q.Get("error"); e != "" {
		m.fail(rw, http.StatusUnauthorized, "identity provider returned an error", fmt.Errorf("%s: %s", e, q.Get("error_description")))
		return
	}
	code := q.Get("code")
	if code == "" {
		m.fail(rw, http.StatusBadRequest, "missing authorization code", nil)
		return
	}

	ctx := r.Context()
	if m.opts.HTTPClient != nil {
		ctx = context.WithValue(ctx, oauth2.HTTPClient, m.opts.HTTPClient)
		ctx = gooidc.ClientContext(ctx, m.opts.HTTPClient)
	}
	tok, err := m.oauth.Exchange(ctx, code, oauth2.VerifierOption(p.verifier))
	if err != nil {
		m.fail(rw, http.StatusUnauthorized, "token exchange failed", err)
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		m.fail(rw, http.StatusUnauthorized, "token response has no id_token", nil)
		return
	}
	// Verifies signature, issuer, audience and expiry.
	idt, err := m.verifier.Verify(ctx, raw)
	if err != nil {
		m.fail(rw, http.StatusUnauthorized, "id token verification failed", err)
		return
	}
	if subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(p.nonce)) != 1 {
		m.fail(rw, http.StatusUnauthorized, "id token nonce mismatch", nil)
		return
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		m.fail(rw, http.StatusUnauthorized, "decoding id token claims", err)
		return
	}
	id := m.identity(idt, claims)
	if id.Subject == "" {
		m.fail(rw, http.StatusUnauthorized, "id token has no subject", nil)
		return
	}
	sid, exp, err := m.store.createSession(id, m.cfg.Session.TTL)
	if err != nil {
		m.fail(rw, http.StatusInternalServerError, "creating session", err)
		return
	}
	http.SetCookie(rw, m.cookie(m.cfg.Session.CookieName, sid, "/", exp))
	if m.opts.Logger != nil {
		m.opts.Logger.Info("oidc login succeeded for subject %q preset %q", id.Subject, m.name)
	}
	http.Redirect(rw, r, p.next, http.StatusFound)
}

func (m *Manager) logout(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(rw, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if c, err := r.Cookie(m.cfg.Session.CookieName); err == nil {
		m.store.revoke(c.Value)
	}
	http.SetCookie(rw, m.expire(m.cfg.Session.CookieName, "/"))
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(rw, `<!doctype html><title>Logged out</title><p>Logged out. <a href="%s">Log in again</a></p>`,
		html.EscapeString(loginPrefix+url.PathEscape(m.name)))
}

func (m *Manager) identity(idt *gooidc.IDToken, claims map[string]any) Identity {
	cm := m.preset.ClaimMap
	id := Identity{
		Subject: stringClaim(claims, cm.Subject),
		Email:   stringClaim(claims, cm.Email),
		Name:    stringClaim(claims, cm.Name),
		Groups:  stringsClaim(claims, cm.Groups),
		Issuer:  idt.Issuer,
		Preset:  m.name,
	}
	if id.Subject == "" {
		id.Subject = idt.Subject
	}
	return id
}

func (m *Manager) cookie(name, value, path string, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     path,
		Expires:  expires,
		HttpOnly: true,
		Secure:   m.secureCookie,
		SameSite: http.SameSiteLaxMode,
	}
}

func (m *Manager) expire(name, path string) *http.Cookie {
	c := m.cookie(name, "", path, time.Unix(0, 0))
	c.MaxAge = -1
	return c
}

// fail logs the detail and returns a generic message to the client.
func (m *Manager) fail(rw http.ResponseWriter, code int, msg string, err error) {
	if m.opts.Logger != nil {
		if err != nil {
			m.opts.Logger.Warn("oidc: %s: %s", msg, err)
		} else {
			m.opts.Logger.Warn("oidc: %s", msg)
		}
	}
	http.Error(rw, msg, code)
}

// safeNext only allows same-origin relative redirect targets.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.HasPrefix(next, "/\\") ||
		strings.HasPrefix(next, loginPrefix) || strings.HasPrefix(next, "/oauth/") {
		return "/"
	}
	return next
}

func stringClaim(claims map[string]any, key string) string {
	if key == "" {
		return ""
	}
	s, _ := claims[key].(string)
	return s
}

func stringsClaim(claims map[string]any, key string) []string {
	if key == "" {
		return nil
	}
	switch v := claims[key].(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
