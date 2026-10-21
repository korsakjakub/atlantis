package rbac

import (
	"encoding/base32"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/runatlantis/atlantis/server/events"
	"github.com/runatlantis/atlantis/server/fork/oidc"
	"go.yaml.in/yaml/v4"
)

// LocalIssuer marks identities created by break-glass local login.
const LocalIssuer = "local"

// Rule grants Role to principals whose claim matches Value, on Repos.
type Rule struct {
	// Claim is one of groups, email, name, sub, issuer. Value may contain
	// glob wildcards (path.Match); for groups any element may match.
	Claim string `yaml:"claim"`
	Value string `yaml:"value"`
	Role  string `yaml:"role"`
	// Repos are repo-allowlist style globs on owner/repo ("org/*", "!org/x").
	// Empty means every repo.
	Repos []string `yaml:"repos"`
}

// Grant gives a fixed identity (VCS username or the API token) a role.
type Grant struct {
	Username string   `yaml:"username"` // vcs_users only
	Role     string   `yaml:"role"`
	Repos    []string `yaml:"repos"`
}

// LocalUser is a break-glass account (HTTP Basic + optional TOTP).
type LocalUser struct {
	Name           string `yaml:"name"`
	PasswordBcrypt string `yaml:"password_bcrypt"`
	Role           string `yaml:"role"`
	// BreakGlass users must present a TOTP code. The base32 seed is read
	// from the environment variable named by TOTPSeedEnv.
	BreakGlass  bool   `yaml:"break_glass"`
	TOTPSeedEnv string `yaml:"totp_seed_env"`
}

// File is the on-disk configuration.
type File struct {
	DefaultRole string      `yaml:"default_role"` // role for authenticated principals matching no rule; empty = none
	Rules       []Rule      `yaml:"rules"`
	APIToken    Grant       `yaml:"api_token"`
	VCSUsers    []Grant     `yaml:"vcs_users"`
	LocalUsers  []LocalUser `yaml:"local_users"`
}

type scoped struct {
	role  Role
	repos *events.RepoAllowlistChecker // nil when any
	any   bool                         // no repo scope: also applies to repo-less requests
}

type localUser struct {
	LocalUser
	role Role
	seed []byte
}

// Engine is the validated configuration.
type Engine struct {
	defaultRole Role
	rules       []scoped
	ruleSrc     []Rule
	apiToken    scoped
	vcsUsers    map[string]scoped
	local       map[string]*localUser
	totp        *totpGuard
}

func newScoped(role string, repos []string) (scoped, error) {
	r, err := ParseRole(role)
	if err != nil {
		return scoped{}, err
	}
	if len(repos) == 0 {
		return scoped{role: r, any: true}, nil
	}
	pats := make([]string, len(repos))
	for i, p := range repos {
		neg := strings.HasPrefix(p, "!")
		// The VCS hostname is unknown for API requests, so match "<any>/owner/repo".
		pats[i] = "*/" + strings.TrimPrefix(p, "!")
		if neg {
			pats[i] = "!" + pats[i]
		}
	}
	c, err := events.NewRepoAllowlistChecker(strings.Join(pats, ","))
	return scoped{role: r, repos: c}, err
}

func (s scoped) matches(repo string) bool {
	if s.any {
		return true
	}
	return repo != "" && s.repos.IsAllowlisted(repo, "")
}

// LoadFile reads and validates the config. Any problem fails startup.
func LoadFile(p string) (*Engine, error) {
	b, err := os.ReadFile(p) // #nosec G304 -- operator supplied path
	if err != nil {
		return nil, fmt.Errorf("reading rbac config: %w", err)
	}
	var f File
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parsing rbac config: %w", err)
	}
	return New(f, os.Getenv)
}

// New validates f. TOTP is required for every break_glass local user.
func New(f File, getenv func(string) string) (*Engine, error) {
	e := &Engine{vcsUsers: map[string]scoped{}, local: map[string]*localUser{}, ruleSrc: f.Rules, totp: newTOTPGuard()}
	var err error
	if f.DefaultRole != "" {
		if e.defaultRole, err = ParseRole(f.DefaultRole); err != nil {
			return nil, fmt.Errorf("default_role: %w", err)
		}
	}
	for i, r := range f.Rules {
		switch r.Claim {
		case "groups", "email", "name", "sub", "issuer":
		default:
			return nil, fmt.Errorf("rules[%d]: unknown claim %q", i, r.Claim)
		}
		if _, err := path.Match(r.Value, ""); err != nil || r.Value == "" {
			return nil, fmt.Errorf("rules[%d]: invalid value %q", i, r.Value)
		}
		s, err := newScoped(r.Role, r.Repos)
		if err != nil {
			return nil, fmt.Errorf("rules[%d]: %w", i, err)
		}
		e.rules = append(e.rules, s)
	}
	if f.APIToken.Role != "" {
		if e.apiToken, err = newScoped(f.APIToken.Role, f.APIToken.Repos); err != nil {
			return nil, fmt.Errorf("api_token: %w", err)
		}
	}
	for i, g := range f.VCSUsers {
		if g.Username == "" {
			return nil, fmt.Errorf("vcs_users[%d]: username required", i)
		}
		s, err := newScoped(g.Role, g.Repos)
		if err != nil {
			return nil, fmt.Errorf("vcs_users[%d]: %w", i, err)
		}
		e.vcsUsers[strings.ToLower(g.Username)] = s
	}
	for i, u := range f.LocalUsers {
		role, err := ParseRole(u.Role)
		if err != nil || u.Name == "" || u.PasswordBcrypt == "" {
			return nil, fmt.Errorf("local_users[%d]: name, password_bcrypt and a valid role are required (%v)", i, err)
		}
		lu := &localUser{LocalUser: u, role: role}
		if u.BreakGlass {
			if u.TOTPSeedEnv == "" {
				return nil, fmt.Errorf("local_users[%d]: break_glass user needs totp_seed_env", i)
			}
			lu.seed, err = base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(getenv(u.TOTPSeedEnv)))
			if err != nil || len(lu.seed) < 10 {
				return nil, fmt.Errorf("local_users[%d]: env %s must hold a base32 seed of at least 80 bits", i, u.TOTPSeedEnv)
			}
		}
		e.local[u.Name] = lu
	}
	return e, nil
}

// HasVCSUsers reports whether PR commands need an RBAC check at all.
func (e *Engine) HasVCSUsers() bool { return len(e.vcsUsers) > 0 }

func claimMatches(r Rule, id oidc.Identity) bool {
	ok := func(s string) bool { m, _ := path.Match(r.Value, s); return s != "" && m }
	switch r.Claim {
	case "groups":
		for _, g := range id.Groups {
			if ok(g) {
				return true
			}
		}
		return false
	case "email":
		return ok(id.Email)
	case "name":
		return ok(id.Name)
	case "sub":
		return ok(id.Subject)
	default:
		return ok(id.Issuer)
	}
}

// RoleFor resolves the role of an OIDC identity on repo ("" = repo-less
// request: only rules without a repo scope count). Roles from all matching
// rules are combined by taking the highest; no match yields default_role.
func (e *Engine) RoleFor(id oidc.Identity, repo string) Role {
	if id.Issuer == LocalIssuer {
		if u := e.local[strings.TrimPrefix(id.Subject, LocalIssuer+":")]; u != nil {
			return u.role
		}
		return None
	}
	best, matched := None, false
	for i, r := range e.ruleSrc {
		if claimMatches(r, id) && e.rules[i].matches(repo) {
			matched = true
			best = max(best, e.rules[i].role)
		}
	}
	if !matched {
		return e.defaultRole
	}
	return best
}

// TokenRole is the role of a holder of the X-Atlantis-Token API secret.
func (e *Engine) TokenRole(repo string) Role {
	if e.apiToken.role != None && e.apiToken.matches(repo) {
		return e.apiToken.role
	}
	return None
}

// VCSRole returns the role configured for a VCS username on repo. ok is
// false when the user has no entry; the VCS allowlist then decides alone.
func (e *Engine) VCSRole(username, repo string) (role Role, ok bool) {
	s, ok := e.vcsUsers[strings.ToLower(username)]
	if !ok {
		return None, false
	}
	if !s.matches(repo) {
		return None, true
	}
	return s.role, true
}
