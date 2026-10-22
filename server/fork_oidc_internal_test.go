package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

func TestNewForkOIDC(t *testing.T) {
	logger := logging.NewNoopLogger(t)

	for _, v := range []string{"", "off"} {
		m, err := newForkOIDC(UserConfig{ForkOIDC: v}, logger)
		Ok(t, err)
		Assert(t, m == nil, "flag %q must leave OIDC disabled", v)
	}

	_, err := newForkOIDC(UserConfig{ForkOIDC: "okta"}, logger)
	Assert(t, err != nil, "preset without --fork-oidc-config must fail")

	_, err = newForkOIDC(UserConfig{ForkOIDC: "okta", ForkOIDCConfig: "/nonexistent/oidc.yaml"}, logger)
	Assert(t, err != nil, "unreadable config must fail")
}

func TestNewForkRBAC(t *testing.T) {
	logger := logging.NewNoopLogger(t)

	e, err := newForkRBAC(UserConfig{}, logger)
	Ok(t, err)
	Assert(t, e == nil, "flag off must leave RBAC disabled")

	_, err = newForkRBAC(UserConfig{ForkRBAC: true, ForkOIDC: "okta"}, logger)
	Assert(t, err != nil, "flag without --fork-rbac-config must fail")

	cfg := filepath.Join(t.TempDir(), "rbac.yaml")
	Ok(t, os.WriteFile(cfg, []byte("default_role: viewer\n"), 0o600))
	for _, oidc := range []string{"", "off"} {
		_, err = newForkRBAC(UserConfig{ForkRBAC: true, ForkRBACConfig: cfg, ForkOIDC: oidc}, logger)
		Assert(t, err != nil, "flag without --fork-oidc (%q) must fail", oidc)
	}

	_, err = newForkRBAC(UserConfig{ForkRBAC: true, ForkRBACConfig: "/nonexistent/rbac.yaml", ForkOIDC: "okta"}, logger)
	Assert(t, err != nil, "unreadable config must fail")

	e, err = newForkRBAC(UserConfig{ForkRBAC: true, ForkRBACConfig: cfg, ForkOIDC: "okta"}, logger)
	Ok(t, err)
	Assert(t, e != nil && e.Logger != nil, "valid config builds an engine with a logger")
}
