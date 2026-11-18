// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"fmt"
	"strings"
	"testing"

	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/tcell/v2"
	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/require"
)

// The pages :impact is given in terminals of 132x36, 80x24 and 60x24, as
// pinned text, the way the reference pages of the kit and the pages of
// :timeline are pinned: a change to the kit or to the view that moves a
// character of them is seen and read before it is committed
// (CALM_UPDATE=1 writes them again). The application keeps two rows at the
// top and one at the bottom, and one more where it draws its line of
// crumbs, which is the 80x20 page: the size at which the neutral rows fold
// and a blank row still sets the answer apart.
func TestImpactCalmPagesArePinnedAtTheSizesAViewIsGiven(t *testing.T) {
	story := func(t *testing.T) *impactView {
		v := impactViewFixture(t)
		v.accept(impactStoryRead(t, impactStory()))
		return v
	}
	lower := func(t *testing.T) *impactView {
		// The cursor in the group a short page folds: the group opens, and
		// the page says which rows it left out for it.
		v := story(t)
		for range 7 {
			impactPress(t, v, tcell.KeyDown)
		}
		return v
	}
	queries := func(t *testing.T) *impactView {
		v := story(t)
		impactPress(t, v, impactQueriesKey)
		return v
	}
	denied := func(t *testing.T) *impactView {
		v := impactViewFixture(t)
		v.accept(impactStoryRead(t, &impactSource{status: observability.Status{State: observability.StateDenied, HTTPStatus: 403}}))
		return v
	}
	for _, c := range []struct {
		name          string
		view          func(t *testing.T) *impactView
		width, height int
		ascii         bool
	}{
		{"story", story, 132, 33, false},
		{"story", story, 80, 21, false},
		{"story", story, 80, 20, false},
		{"story", story, 60, 21, true},
		{"lower", lower, 80, 20, false},
		{"queries", queries, 132, 33, false},
		{"queries", queries, 80, 20, false},
		{"denied", denied, 132, 33, false},
		{"denied", denied, 80, 21, false},
		{"denied", denied, 60, 21, true},
	} {
		name := fmt.Sprintf("%s-%dx%d", c.name, c.width, c.height)
		if c.ascii {
			name += "-ascii"
		}
		t.Run(name, func(t *testing.T) {
			v := c.view(t)
			v.app.Config.K9s.UI.NoIcons = c.ascii
			v.render()
			frame := drawnText(t, v, c.width, c.height)
			calmPinned(t, "impact", name, frame)
			for _, line := range strings.Split(frame, "\n") {
				require.LessOrEqual(t, runewidth.StringWidth(line), c.width, line)
			}
			if c.ascii {
				for _, r := range frame {
					require.Less(t, r, rune(128), "%q is not ASCII", r)
				}
			}
			lines := strings.Split(strings.TrimRight(frame, "\n"), "\n")
			require.LessOrEqual(t, len(lines), c.height)
			// The same parts in the same places as on every calm page: the
			// title, then the answer with its caveat, and the next line as
			// the first of the keys.
			require.True(t, strings.HasPrefix(lines[0], "Impact  Deployment app"), lines[0])
			require.Regexp(t, `(?m)^  (Order in time, not proof of cause|Not read is not zero and not healthy)\.$|  +(Order in time, not proof of cause|Not read is not zero and not healthy)\.$`, frame)
			require.Regexp(t, `(?m)^  next  `, frame)
			lowered := strings.ToLower(frame)
			for _, banned := range []string{"because", "caused", "due to", "root cause", "blame", "healthy "} {
				require.NotContains(t, lowered, banned)
			}
		})
	}
}
