// Package oidc implements native OpenID Connect (Authorization Code + PKCE)
// login for the Atlantis web UI and API. Fork-only feature, see
// docs/adr/0005-fork-oidc.md.
package oidc

import (
	"fmt"
	"os"
	"strings"
	"time"

	"go.yaml.in/yaml/v4"
)

// Off is the flag value that disables the feature.
const Off = "off"

const (
	defaultSessionTTL = 8 * time.Hour
	defaultCookieName = "atlantis_fork_session"
)

// ClaimMap maps identity fields to the names of ID token claims.
type ClaimMap struct {
	Subject string `yaml:"subject"`
	Email   string `yaml:"email"`
	Name    string `yaml:"name"`
	Groups  string `yaml:"groups"`
}

// Preset describes one identity provider.
type Preset struct {
	// Provider selects built-in defaults: okta, entra, google, gitlab,
	// keycloak or authentik. Optional.
	Provider string `yaml:"provider"`
	Issuer   string `yaml:"issuer"`
	ClientID string `yaml:"client_id"`
	// ClientSecret is the inline secret. ClientSecretEnv names an
	// environment variable that takes precedence when set.
	ClientSecret    string   `yaml:"client_secret"`
	ClientSecretEnv string   `yaml:"client_secret_env"`
	RedirectURL     string   `yaml:"redirect_url"`
	Scopes          []string `yaml:"scopes"`
	ClaimMap        ClaimMap `yaml:"claim_map"`
	// AuthParams are extra query parameters for the authorization request.
	AuthParams map[string]string `yaml:"auth_params"`
}

// Config is the on-disk configuration file.
type Config struct {
	Session struct {
		TTL        time.Duration `yaml:"ttl"`
		CookieName string        `yaml:"cookie_name"`
	} `yaml:"session"`
	Presets map[string]Preset `yaml:"presets"`
}

var baseClaims = ClaimMap{Subject: "sub", Email: "email", Name: "name", Groups: "groups"}

// providerDefaults holds per-provider scope and claim overrides.
var providerDefaults = map[string]Preset{
	"okta":      {Scopes: []string{"openid", "profile", "email", "groups"}},
	"entra":     {ClaimMap: ClaimMap{Email: "preferred_username"}},
	"google":    {},
	"gitlab":    {ClaimMap: ClaimMap{Groups: "groups_direct"}},
	"keycloak":  {},
	"authentik": {},
}

// LoadConfig reads and validates a configuration file.
func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- operator supplied path
	if err != nil {
		return nil, fmt.Errorf("reading oidc config: %w", err)
	}
	return ParseConfig(raw)
}

// ParseConfig parses configuration bytes and applies provider defaults.
func ParseConfig(raw []byte) (*Config, error) {
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parsing oidc config: %w", err)
	}
	if c.Session.TTL <= 0 {
		c.Session.TTL = defaultSessionTTL
	}
	if c.Session.CookieName == "" {
		c.Session.CookieName = defaultCookieName
	}
	for name, p := range c.Presets {
		if name == "" || name == Off || strings.ContainsAny(name, "/?# ") {
			return nil, fmt.Errorf("invalid oidc preset name %q", name)
		}
		if p.Provider != "" {
			if _, ok := providerDefaults[p.Provider]; !ok {
				return nil, fmt.Errorf("preset %q: unknown provider %q", name, p.Provider)
			}
		}
		c.Presets[name] = p.withDefaults()
	}
	return &c, nil
}

func (p Preset) withDefaults() Preset {
	d := providerDefaults[p.Provider]
	if len(p.Scopes) == 0 {
		p.Scopes = d.Scopes
	}
	if len(p.Scopes) == 0 {
		p.Scopes = []string{"openid", "profile", "email"}
	}
	if !contains(p.Scopes, "openid") {
		p.Scopes = append([]string{"openid"}, p.Scopes...)
	}
	fill := func(dst *string, provider, base string) {
		if *dst == "" {
			*dst = provider
		}
		if *dst == "" {
			*dst = base
		}
	}
	fill(&p.ClaimMap.Subject, d.ClaimMap.Subject, baseClaims.Subject)
	fill(&p.ClaimMap.Email, d.ClaimMap.Email, baseClaims.Email)
	fill(&p.ClaimMap.Name, d.ClaimMap.Name, baseClaims.Name)
	fill(&p.ClaimMap.Groups, d.ClaimMap.Groups, baseClaims.Groups)
	return p
}

func (p Preset) validate(name string) error {
	switch {
	case p.Issuer == "":
		return fmt.Errorf("preset %q: issuer is required", name)
	case p.ClientID == "":
		return fmt.Errorf("preset %q: client_id is required", name)
	case p.RedirectURL == "":
		return fmt.Errorf("preset %q: redirect_url is required", name)
	}
	return nil
}

func (p Preset) secret() string {
	if p.ClientSecretEnv != "" {
		if v := os.Getenv(p.ClientSecretEnv); v != "" {
			return v
		}
	}
	return p.ClientSecret
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
