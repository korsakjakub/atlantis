// SPDX-License-Identifier: Apache-2.0

package drift

import (
	"errors"
	"time"

	"github.com/runatlantis/atlantis/server/events/models"
)

// ErrStorageUnavailable is returned (wrapped) by Storage implementations whose
// backend cannot be reached. Callers must fail closed when they see it.
var ErrStorageUnavailable = errors.New("drift storage backend unavailable")

// HealthChecker is optionally implemented by Storage backends that can be
// unavailable at runtime. A nil error means the backend is reachable.
type HealthChecker interface {
	Healthy() error
}

// MatchesGet reports whether a drift record satisfies the Get filters.
// Exported so external Storage implementations share the exact semantics of
// InMemoryStorage.
func MatchesGet(d models.ProjectDrift, opts GetOptions, now time.Time) bool {
	return matchesGetOptions(d, opts, now)
}

// MatchesDelete reports whether a drift record satisfies the DeleteMatching
// filters, honoring GetOptions.Exact.
func MatchesDelete(d models.ProjectDrift, opts GetOptions, now time.Time) bool {
	return matchesDeleteOptions(d, opts, now)
}
