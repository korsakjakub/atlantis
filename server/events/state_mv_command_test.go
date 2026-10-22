// SPDX-License-Identifier: Apache-2.0

package events_test

import (
	"strings"
	"testing"

	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

func TestCommentParser_StateMv(t *testing.T) {
	r := commentParser.Parse(`atlantis state mv -d dir 'a["x"]' b -- -lock=false`, models.Github)
	Equals(t, "", r.CommentResponse)
	Equals(t, command.State, r.Command.Name)
	Equals(t, "mv", r.Command.SubName)
	Equals(t, "dir", r.Command.RepoRelDir)
	Equals(t, `-lock=false a["x"] b`, strings.Join(r.Command.Flags, " "))

	for _, c := range []string{"atlantis state mv a", "atlantis state mv a b c", "atlantis state mv"} {
		r := commentParser.Parse(c, models.Github)
		Assert(t, strings.Contains(r.CommentResponse, "unknown argument(s)"), "expected an argument error for %q, got %q", c, r.CommentResponse)
	}
}
