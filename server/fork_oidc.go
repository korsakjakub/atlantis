package server

import (
	"context"
	"errors"
	"fmt"

	forkoidc "github.com/runatlantis/atlantis/server/fork/oidc"
	"github.com/runatlantis/atlantis/server/fork/rbac"
	"github.com/runatlantis/atlantis/server/logging"
	"github.com/urfave/negroni/v3"
)

// newForkOIDC builds the OIDC manager from the fork flags. It returns nil
// when --fork-oidc is off, which leaves Atlantis behaving as upstream.
func newForkOIDC(userConfig UserConfig, logger logging.SimpleLogging) (*forkoidc.Manager, error) {
	if userConfig.ForkOIDC == "" || userConfig.ForkOIDC == forkoidc.Off {
		return nil, nil
	}
	if userConfig.ForkOIDCConfig == "" {
		return nil, errors.New("--fork-oidc requires --fork-oidc-config")
	}
	cfg, err := forkoidc.LoadConfig(userConfig.ForkOIDCConfig)
	if err != nil {
		return nil, err
	}
	opts := forkoidc.Options{
		Logger:        logger,
		APISecret:     []byte(userConfig.APISecret),
		AllowAPIToken: userConfig.ForkOIDCAPIToken,
	}
	m, err := forkoidc.New(context.Background(), cfg, userConfig.ForkOIDC, opts)
	if err != nil {
		return nil, fmt.Errorf("initializing oidc: %w", err)
	}
	return m, nil
}

// useForkOIDC adds the OIDC middleware to the chain when enabled.
func (s *Server) useForkOIDC(n *negroni.Negroni) {
	if s.ForkOIDC != nil {
		n.Use(s.ForkOIDC)
		if s.ForkRBAC != nil {
			n.Use(s.ForkRBAC) // after OIDC: it needs the authenticated identity
		}
	}
}

// newForkRBAC loads the RBAC config. It returns nil when --fork-rbac is off.
func newForkRBAC(userConfig UserConfig) (*rbac.Engine, error) {
	if !userConfig.ForkRBAC {
		return nil, nil
	}
	if userConfig.ForkRBACConfig == "" {
		return nil, errors.New("--fork-rbac requires --fork-rbac-config")
	}
	if userConfig.ForkOIDC == "" || userConfig.ForkOIDC == forkoidc.Off {
		return nil, errors.New("--fork-rbac requires --fork-oidc")
	}
	return rbac.LoadFile(userConfig.ForkRBACConfig)
}
