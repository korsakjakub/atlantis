// SPDX-License-Identifier: Apache-2.0

package drift_test

import (
	"testing"

	"github.com/runatlantis/atlantis/server/core/drift"
	"github.com/runatlantis/atlantis/server/core/drift/storagetest"
)

func TestInMemoryStorage_Conformance(t *testing.T) {
	storagetest.Run(t, func(*testing.T) drift.Storage { return drift.NewInMemoryStorage() })
}
