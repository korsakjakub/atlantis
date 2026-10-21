package server

import (
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
