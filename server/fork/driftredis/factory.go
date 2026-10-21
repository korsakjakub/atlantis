// SPDX-License-Identifier: Apache-2.0

package driftredis

import (
	"fmt"
	"strings"

	"github.com/runatlantis/atlantis/server/core/drift"
)

// SplitAddresses parses a comma separated address list, dropping blanks.
func SplitAddresses(s string) []string {
	var out []string
	for _, a := range strings.Split(s, ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// Build returns the drift.Storage for the ATLANTIS_FORK_DRIFT_STORAGE value:
// "" or "memory" yields the upstream in-memory storage, "redis" a RedisStorage.
func Build(backend string, cfg Config, opts Options) (drift.Storage, error) {
	switch backend {
	case "", "memory":
		return drift.NewInMemoryStorage(), nil
	case "redis":
		client, err := NewClient(cfg)
		if err != nil {
			return nil, err
		}
		return NewStorage(client, opts), nil
	default:
		return nil, fmt.Errorf("unknown drift storage backend %q", backend)
	}
}
