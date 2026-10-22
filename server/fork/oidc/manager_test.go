package oidc_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/runatlantis/atlantis/server/fork/oidc"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

const redirectURL = "https://atlantis.test/oauth/callback"

// fakeIssuer is a minimal OIDC provider: discovery, JWKS and a token
// endpoint that enforces PKCE.
type fakeIssuer struct {
	srv *httptest.Server
	key *rsa.PrivateKey

	mu    sync.Mutex
	codes map[string]issued
}

type issued struct {
	challenge string
	nonce     string
	aud       string
	expiry    time.Time
	iss       string         // overrides the issuer claim when set
	badKey    bool           // sign with a key the JWKS does not publish
	extra     map[string]any // additional claims
}

func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	Ok(t, err)
	f := &fakeIssuer{key: key, codes: map[string]issued{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                f.srv.URL,
			"authorization_endpoint":                f.srv.URL + "/authorize",
			"token_endpoint":                        f.srv.URL + "/token",
			"jwks_uri":                              f.srv.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
			{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"},
		}})
	})
	mux.HandleFunc("/token", f.token)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIssuer) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f.mu.Lock()
	c, ok := f.codes[r.Form.Get("code")]
	f.mu.Unlock()
	sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
	if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != c.challenge {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	}
	signKey := f.key
	if c.badKey {
		signKey, _ = rsa.GenerateKey(rand.Reader, 2048)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: signKey},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	claims := map[string]any{
		"iss":    f.srv.URL,
		"sub":    "user-1",
		"aud":    c.aud,
		"exp":    c.expiry.Unix(),
		"iat":    time.Now().Add(-time.Minute).Unix(),
		"nonce":  c.nonce,
		"email":  "alice@example.com",
		"name":   "Alice",
		"groups": []string{"infra", "dev"},
	}
	if c.iss != "" {
		claims["iss"] = c.iss
	}
	maps.Copy(claims, c.extra)
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	f.mu.Lock()
	delete(f.codes, r.Form.Get("code"))
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "at", "token_type": "Bearer", "id_token": raw,
	})
}

// issueCode plays the role of the authorize endpoint after user consent.
func (f *fakeIssuer) issueCode(authURL string, mutate func(*issued)) (code, state string) {
	u, _ := url.Parse(authURL)
	q := u.Query()
	i := issued{
		challenge: q.Get("code_challenge"),
		nonce:     q.Get("nonce"),
		aud:       "atlantis",
		expiry:    time.Now().Add(time.Hour),
	}
	if mutate != nil {
		mutate(&i)
	}
	code = "code-" + q.Get("state")
	f.mu.Lock()
	f.codes[code] = i
	f.mu.Unlock()
	return code, q.Get("state")
}

type env struct {
	t      *testing.T
	issuer *fakeIssuer
	mgr    *oidc.Manager
	h      http.Handler
}

func newEnv(t *testing.T, allowToken bool) *env {
	return newEnvWith(t, allowToken, "", nil)
}

// newEnvWith adds raw YAML to the preset and optionally overrides the clock.
func newEnvWith(t *testing.T, allowToken bool, presetExtra string, now func() time.Time) *env {
	t.Helper()
	f := newFakeIssuer(t)
	cfg, err := oidc.ParseConfig([]byte(`
presets:
  test:
    provider: keycloak
    issuer: ` + f.srv.URL + `
    client_id: atlantis
    client_secret: s3cret
    redirect_url: ` + redirectURL + `
` + presetExtra))
	Ok(t, err)
	mgr, err := oidc.New(t.Context(), cfg, "test", oidc.Options{
		Logger:        logging.NewNoopLogger(t),
		APISecret:     []byte("api-secret"),
		AllowAPIToken: allowToken,
		Now:           now,
	})
	Ok(t, err)
	e := &env{t: t, issuer: f, mgr: mgr}
	e.h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mgr.ServeHTTP(w, r, func(w http.ResponseWriter, r *http.Request) {
			who := "anonymous"
			if id, ok := oidc.IdentityFromContext(r.Context()); ok {
				who = id.Subject + "|" + id.Email + "|" + id.Name + "|" + id.Groups[0]
			}
			w.Header().Set("X-Who", who)
			w.Header().Set("X-Seen-Token", r.Header.Get("X-Atlantis-Token"))
			_, _ = w.Write([]byte("ok"))
		})
	})
	return e
}

func (e *env) do(method, target string, cookies []*http.Cookie, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	e.h.ServeHTTP(rr, req)
	return rr
}

// startLogin hits the login endpoint and returns the provider URL and the
// cookies the browser would keep.
func (e *env) startLogin(next string) (string, []*http.Cookie) {
	rr := e.do("GET", "/oauth/login/test?next="+url.QueryEscape(next), nil, nil)
	Equals(e.t, http.StatusFound, rr.Code)
	return rr.Header().Get("Location"), rr.Result().Cookies()
}

func (e *env) callback(code, state string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	return e.do("GET", "/oauth/callback?code="+code+"&state="+state, cookies, nil)
}

func cookieNamed(cs []*http.Cookie, name string) *http.Cookie {
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func (e *env) login(mutate func(*issued)) []*http.Cookie {
	authURL, cookies := e.startLogin("/lock?id=1")
	code, state := e.issuer.issueCode(authURL, mutate)
	rr := e.callback(code, state, cookies)
	Equals(e.t, http.StatusFound, rr.Code)
	Equals(e.t, "/lock?id=1", rr.Header().Get("Location"))
	return rr.Result().Cookies()
}

func TestLogin_Success(t *testing.T) {
	e := newEnv(t, false)
	authURL, cookies := e.startLogin("/lock?id=1")

	u, _ := url.Parse(authURL)
	q := u.Query()
	Equals(t, "code", q.Get("response_type"))
	Equals(t, "S256", q.Get("code_challenge_method"))
	Assert(t, q.Get("code_challenge") != "", "pkce challenge missing")
	Assert(t, q.Get("state") != "" && q.Get("nonce") != "", "state and nonce are mandatory")
	Equals(t, "atlantis", q.Get("client_id"))

	sc := cookieNamed(cookies, "atlantis_fork_session_state")
	Assert(t, sc != nil, "state cookie missing")

	code, state := e.issuer.issueCode(authURL, nil)
	rr := e.callback(code, state, cookies)
	Equals(t, http.StatusFound, rr.Code)
	Equals(t, "/lock?id=1", rr.Header().Get("Location"))

	sess := cookieNamed(rr.Result().Cookies(), "atlantis_fork_session")
	Assert(t, sess != nil, "session cookie missing")
	Assert(t, sess.HttpOnly, "cookie must be HttpOnly")
	Assert(t, sess.Secure, "cookie must be Secure")
	Equals(t, http.SameSiteLaxMode, sess.SameSite)
	Assert(t, sess.Expires.After(time.Now().Add(7*time.Hour)), "default timeout is 8h")

	rr = e.do("GET", "/", []*http.Cookie{sess}, nil)
	Equals(t, http.StatusOK, rr.Code)
	Equals(t, "user-1|alice@example.com|Alice|infra", rr.Header().Get("X-Who"))

	// Logout revokes the session server side: replaying the cookie fails.
	Equals(t, http.StatusMethodNotAllowed, e.do("GET", "/oauth/logout", []*http.Cookie{sess}, nil).Code)
	rr = e.do("POST", "/oauth/logout", []*http.Cookie{sess}, nil)
	Equals(t, http.StatusOK, rr.Code)
	rr = e.do("GET", "/", []*http.Cookie{sess}, nil)
	Equals(t, http.StatusFound, rr.Code)
}

func TestLogin_ExpiredToken(t *testing.T) {
	e := newEnv(t, false)
	authURL, cookies := e.startLogin("/")
	code, state := e.issuer.issueCode(authURL, func(i *issued) { i.expiry = time.Now().Add(-time.Hour) })
	rr := e.callback(code, state, cookies)
	Equals(t, http.StatusUnauthorized, rr.Code)
	Assert(t, cookieNamed(rr.Result().Cookies(), "atlantis_fork_session") == nil, "no session on failure")
}

func TestLogin_WrongAudience(t *testing.T) {
	e := newEnv(t, false)
	authURL, cookies := e.startLogin("/")
	code, state := e.issuer.issueCode(authURL, func(i *issued) { i.aud = "someone-else" })
	rr := e.callback(code, state, cookies)
	Equals(t, http.StatusUnauthorized, rr.Code)
}

func TestLogin_WrongNonce(t *testing.T) {
	e := newEnv(t, false)
	authURL, cookies := e.startLogin("/")
	code, state := e.issuer.issueCode(authURL, func(i *issued) { i.nonce = "forged" })
	rr := e.callback(code, state, cookies)
	Equals(t, http.StatusUnauthorized, rr.Code)
}

func TestLogin_ReplayedState(t *testing.T) {
	e := newEnv(t, false)
	authURL, cookies := e.startLogin("/")
	code, state := e.issuer.issueCode(authURL, nil)
	Equals(t, http.StatusFound, e.callback(code, state, cookies).Code)

	// Same state again, even with a fresh code, must be rejected.
	code, state = e.issuer.issueCode(authURL, nil)
	rr := e.callback(code, state, cookies)
	Equals(t, http.StatusBadRequest, rr.Code)
}

func TestLogin_StateNotBoundToBrowser(t *testing.T) {
	e := newEnv(t, false)
	authURL, _ := e.startLogin("/")
	code, state := e.issuer.issueCode(authURL, nil)
	rr := e.callback(code, state, nil)
	Equals(t, http.StatusBadRequest, rr.Code)
}

func TestLogin_UnknownPresetAndOpenRedirect(t *testing.T) {
	e := newEnv(t, false)
	Equals(t, http.StatusNotFound, e.do("GET", "/oauth/login/other", nil, nil).Code)

	for _, next := range []string{"//evil.example/x", "/\t/evil.com", "/\x00/evil.com", "/a\\b", "/\n/evil.com", "https://evil.com", "/oauth/logout"} {
		authURL, cookies := e.startLogin(next)
		code, state := e.issuer.issueCode(authURL, nil)
		rr := e.callback(code, state, cookies)
		Equals(t, "/", rr.Header().Get("Location"))
	}
}

func TestLogin_RejectsBadSignatureAndForeignIssuer(t *testing.T) {
	e := newEnv(t, false)
	for _, mutate := range []func(*issued){
		func(i *issued) { i.badKey = true },
		func(i *issued) { i.iss = "https://evil.example" },
	} {
		authURL, cookies := e.startLogin("/")
		code, state := e.issuer.issueCode(authURL, mutate)
		Equals(t, http.StatusUnauthorized, e.callback(code, state, cookies).Code)
	}
}

func TestLogin_RequiredClaims(t *testing.T) {
	e := newEnvWith(t, false, "    required_claims:\n      hd: example.com\n", nil)

	authURL, cookies := e.startLogin("/")
	code, state := e.issuer.issueCode(authURL, func(i *issued) { i.extra = map[string]any{"hd": "other.com"} })
	rr := e.callback(code, state, cookies)
	Equals(t, http.StatusUnauthorized, rr.Code)
	Assert(t, cookieNamed(rr.Result().Cookies(), "atlantis_fork_session") == nil, "no session on failure")

	authURL, cookies = e.startLogin("/")
	code, state = e.issuer.issueCode(authURL, nil) // claim missing
	Equals(t, http.StatusUnauthorized, e.callback(code, state, cookies).Code)

	authURL, cookies = e.startLogin("/")
	code, state = e.issuer.issueCode(authURL, func(i *issued) { i.extra = map[string]any{"hd": "example.com"} })
	Equals(t, http.StatusFound, e.callback(code, state, cookies).Code)
}

func TestLogin_PendingLimitFailsClosed(t *testing.T) {
	e := newEnv(t, false)
	sess := e.login(nil)
	for range 10000 {
		Equals(t, http.StatusFound, e.do("GET", "/oauth/login/test", nil, nil).Code)
	}
	Equals(t, http.StatusServiceUnavailable, e.do("GET", "/oauth/login/test", nil, nil).Code)
	// Existing sessions keep working.
	Equals(t, http.StatusOK, e.do("GET", "/", sess, nil).Code)
}

func TestSession_ExpiresAfterTTL(t *testing.T) {
	var mu sync.Mutex
	offset := time.Duration(0)
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return time.Now().Add(offset) }
	e := newEnvWith(t, false, "", now)
	sess := e.login(nil)
	Equals(t, http.StatusOK, e.do("GET", "/", sess, nil).Code)

	mu.Lock()
	offset = 9 * time.Hour // default ttl is 8h
	mu.Unlock()
	rr := e.do("GET", "/", sess, nil)
	Equals(t, http.StatusFound, rr.Code)
	Assert(t, strings.HasPrefix(rr.Header().Get("Location"), "/oauth/login/test"), "expired session goes back to login")
}

func TestMiddleware_Exemptions(t *testing.T) {
	e := newEnv(t, false)
	for _, p := range []string{"/events", "/healthz", "/readyz", "/status"} {
		rr := e.do("POST", p, nil, nil)
		Equals(t, http.StatusOK, rr.Code)
		Equals(t, "anonymous", rr.Header().Get("X-Who"))
	}
}

func TestMiddleware_UIRedirectsToLogin(t *testing.T) {
	e := newEnv(t, false)
	rr := e.do("GET", "/lock?id=2", nil, nil)
	Equals(t, http.StatusFound, rr.Code)
	Equals(t, "/oauth/login/test?next=%2Flock%3Fid%3D2", rr.Header().Get("Location"))
	Equals(t, http.StatusUnauthorized, e.do("DELETE", "/locks?id=2", nil, nil).Code)
}

func TestMiddleware_API(t *testing.T) {
	e := newEnv(t, false)

	tok := map[string]string{"X-Atlantis-Token": "api-secret"}
	// No session: 401, and the machine token alone is off by default.
	Equals(t, http.StatusUnauthorized, e.do("POST", "/api/plan", nil, nil).Code)
	Equals(t, http.StatusUnauthorized, e.do("POST", "/api/plan", nil, tok).Code)

	// A login alone grants no API access: apply/remediate still need the secret.
	sess := e.login(nil)
	for _, p := range []string{"/api/plan", "/api/apply", "/api/drift/remediate"} {
		Equals(t, http.StatusUnauthorized, e.do("POST", p, sess, nil).Code)
		Equals(t, http.StatusUnauthorized, e.do("POST", p, sess, map[string]string{"X-Atlantis-Token": "bogus"}).Code)
	}

	// Session plus the secret is allowed, and the client's token reaches the controller.
	rr := e.do("POST", "/api/apply", sess, tok)
	Equals(t, http.StatusOK, rr.Code)
	Equals(t, "api-secret", rr.Header().Get("X-Seen-Token"))
	Equals(t, "user-1|alice@example.com|Alice|infra", rr.Header().Get("X-Who"))
}

func TestMiddleware_APITokenAlternative(t *testing.T) {
	e := newEnv(t, true)
	rr := e.do("POST", "/api/plan", nil, map[string]string{"X-Atlantis-Token": "api-secret"})
	Equals(t, http.StatusOK, rr.Code)
	Equals(t, "anonymous", rr.Header().Get("X-Who"))
	Equals(t, http.StatusUnauthorized, e.do("POST", "/api/plan", nil, map[string]string{"X-Atlantis-Token": "wrong"}).Code)
}

func TestParseConfig(t *testing.T) {
	cfg, err := oidc.ParseConfig([]byte(`
session:
  ttl: 1h
presets:
  okta:
    provider: okta
  entra:
    provider: entra
    claim_map:
      groups: roles
`))
	Ok(t, err)
	Equals(t, time.Hour, cfg.Session.TTL)
	Equals(t, []string{"openid", "profile", "email", "groups"}, cfg.Presets["okta"].Scopes)
	Equals(t, "preferred_username", cfg.Presets["entra"].ClaimMap.Email)
	Equals(t, "roles", cfg.Presets["entra"].ClaimMap.Groups)
	Equals(t, "sub", cfg.Presets["entra"].ClaimMap.Subject)

	_, err = oidc.ParseConfig([]byte("presets:\n  x:\n    provider: nope\n"))
	Assert(t, err != nil, "unknown provider must fail")
	_, err = oidc.ParseConfig([]byte("presets:\n  off:\n    provider: okta\n"))
	Assert(t, err != nil, "reserved name must fail")
}
