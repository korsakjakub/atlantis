package server

import (
	forksa "github.com/runatlantis/atlantis/server/fork/sactoken"
	"github.com/runatlantis/atlantis/server/logging"
	"github.com/urfave/negroni/v3"
)

// newForkSATokens returns nil when --fork-sa-tokens is unset, which leaves
// Atlantis behaving as upstream.
func newForkSATokens(userConfig UserConfig, logger logging.SimpleLogging) (*forksa.Authorizer, error) {
	if userConfig.ForkSATokens == "" {
		return nil, nil
	}
	return forksa.Load(userConfig.ForkSATokens, []byte(userConfig.APISecret), logger)
}

// useForkSATokens must run before the OIDC middleware, which only knows the global secret.
func (s *Server) useForkSATokens(n *negroni.Negroni) {
	if s.ForkSATokens != nil {
		n.Use(s.ForkSATokens)
	}
}
