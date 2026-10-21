package rbac

import (
	"crypto/hmac"
	"crypto/sha1" // #nosec G505 -- RFC 6238 default, required by authenticator apps
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/runatlantis/atlantis/server/fork/oidc"
	"golang.org/x/crypto/bcrypt"
)

const (
	totpHeader  = "X-Atlantis-TOTP"
	maxFailures = 5
	lockout     = time.Minute
)

func totpCode(seed []byte, step uint64) string {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], step)
	h := hmac.New(sha1.New, seed)
	h.Write(b[:])
	s := h.Sum(nil)
	o := s[len(s)-1] & 0xf
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(s[o:o+4])&0x7fffffff)%1000000)
}

// totpGuard rejects replayed codes and throttles guessing, per user.
type totpGuard struct {
	mu        sync.Mutex
	lastStep  map[string]uint64
	failures  map[string]int
	lockedTil map[string]time.Time
}

func newTOTPGuard() *totpGuard {
	return &totpGuard{lastStep: map[string]uint64{}, failures: map[string]int{}, lockedTil: map[string]time.Time{}}
}

func (g *totpGuard) locked(name string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return now.Before(g.lockedTil[name])
}

// check validates code with +-1 step skew; each step is accepted once.
func (g *totpGuard) check(name string, seed []byte, code string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	cur := uint64(now.Unix() / 30) // #nosec G115 -- post-1970 time
	for step := cur - 1; step <= cur+1; step++ {
		if subtle.ConstantTimeCompare([]byte(totpCode(seed, step)), []byte(code)) == 1 && step > g.lastStep[name] {
			g.lastStep[name], g.failures[name] = step, 0
			return true
		}
	}
	return false
}

func (g *totpGuard) fail(name string, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failures[name]++; g.failures[name] >= maxFailures {
		g.failures[name], g.lockedTil[name] = 0, now.Add(lockout)
	}
}

// Authenticate is the oidc.Options.Fallback: HTTP Basic credentials of a
// local break-glass account, plus a TOTP code when the account requires it.
func (e *Engine) Authenticate(r *http.Request) (oidc.Identity, bool) {
	name, pw, ok := r.BasicAuth()
	u := e.local[name]
	if !ok || u == nil {
		return oidc.Identity{}, false
	}
	now := time.Now()
	if e.totp.locked(name, now) {
		return oidc.Identity{}, false
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordBcrypt), []byte(pw)) != nil ||
		(len(u.seed) > 0 && !e.totp.check(name, u.seed, r.Header.Get(totpHeader), now)) {
		e.totp.fail(name, now)
		return oidc.Identity{}, false
	}
	return oidc.Identity{Subject: LocalIssuer + ":" + name, Name: name, Issuer: LocalIssuer, Preset: LocalIssuer}, true
}
