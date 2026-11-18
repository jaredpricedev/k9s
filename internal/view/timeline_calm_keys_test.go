// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/calm"
	"github.com/derailed/k9s/internal/timeline"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// timelineLongName is a rollout and one failing condition of a Deployment
// whose name is as long as the names of a GitLab chart: what Enter opens
// is then wider than half of a 60-column line.
func timelineLongName(scope *timeline.Scope) *timeline.Snapshot {
	at := timelineAt
	deployment := timeline.Ref{Resource: "apps/v1/deployments", Kind: "Deployment", Namespace: "apps", Name: "gitlab-webservice-default"}
	entries := []timeline.Entry{
		{At: at.Add(-12 * time.Minute), Source: timeline.SourceRollout, Change: true, Summary: "rev 2: image v1 -> v2",
			Ref: timeline.Ref{Resource: "apps/v1/replicasets", Kind: "ReplicaSet", Namespace: "apps", Name: "gitlab-webservice-default-7d9f8c6b5d"}},
		{At: at.Add(-8 * time.Minute), Source: timeline.SourceCondition, Failing: true, Ref: deployment, Summary: "Progressing=False ProgressDeadlineExceeded"},
	}
	return timeline.NewSnapshot(scope, at, entries, timelineReads(nil))
}

// The key that shows every entry is named on the first key line at every
// size, also beside a name that would fill the line: a short screen gives
// the second key line up, so a key that is only there is not named at all.
// What Enter opens is what gives way, as it does in :impact; the list
// under the axis names the entry.
func TestTimelineCalmNamesTheKeyForAllDetailBesideALongName(t *testing.T) {
	v := timelineCalmFixture(t, timelineLongName)
	for _, c := range []struct {
		width, height int
		next          string
	}{
		{132, 33, "  next  Enter open deploy/gitlab-webservice-default    a full list"},
		{80, 21, "  next  Enter open deploy/gitlab-webservice-default    a full list"},
		{80, 20, "  next  Enter open deploy/gitlab-webservice-default    a full list"},
		// The name is 32 cells of the 52 the line has: with it there is no
		// room for the second action.
		{60, 21, "  next  Enter open    a full list"},
		{60, 20, "  next  Enter open    a full list"},
		{44, 21, "  next  Enter open    a full list"},
		{40, 14, "  next  Enter open    a full list"},
	} {
		frame := drawnText(t, v, c.width, c.height)
		line, _ := timelineLineWith(t, frame, "  next  ")
		assert.Equal(t, c.next, line, "%dx%d\n%s", c.width, c.height, frame)
		assert.Contains(t, frame, "gitlab-webservice-default", "%dx%d: the page still names the object", c.width, c.height)
	}
	// Enter still opens what the full words named.
	drawnText(t, v, 60, 21)
	entry := v.focusedEntry()
	require.NotNil(t, entry)
	require.Equal(t, "gitlab-webservice-default", entry.Ref.Name)
}

// A short name leaves the line as it was: the words of Enter are only
// shortened where that names one more key.
func TestTimelineCalmKeepsWhatEnterOpensWhereItFits(t *testing.T) {
	v := timelineIncidentView(t)
	for _, size := range [][2]int{{132, 33}, {80, 21}, {60, 21}} {
		frame := drawnText(t, v, size[0], size[1])
		line, _ := timelineLineWith(t, frame, "  next  ")
		require.True(t, strings.HasPrefix(line, "  next  Enter open x2x9q  "), "%d columns: %q", size[0], line)
		require.Contains(t, line, "a full list", "%d columns", size[0])
	}
}

// What the first key line has no room for is named on the second, in the
// place and the words every calm page has for it: all detail after the
// window, the hand-offs after the keys only this view has. Nothing is
// named twice.
func TestTimelineCalmSecondKeyLineNamesWhatTheFirstHasNoRoomFor(t *testing.T) {
	v := timelineIncidentView(t)
	drawnText(t, v, 132, 33)
	st := v.story
	require.NotNil(t, st)

	all := timelineCalmKeys(st, false, nil)
	assert.Equal(t, []string{"↑↓ row", "←→ time", "1-4 window, 0 all", "s sources", "n next entry", "x mark", "/ filter", "r re-read", "Esc back"}, all)

	next := []calm.Action{{Key: "Enter", Label: "open x2x9q"}, {Key: calm.DetailKey, Label: timelineDetailWords},
		{Key: timelineGraphKeyName, Label: "graph it"}, {Key: calmGrafanaKeyName, Label: calmNextGrafana}}
	o := calmOptions(v.app)
	// 132 columns name all four on the first line.
	require.Equal(t, 4, calm.NextShown(next, 132, o))
	assert.Equal(t, all, v.calmKeys(st, next, 132, o))
	// 60 columns have no room for Grafana, 40 for the graph either.
	require.Equal(t, 3, calm.NextShown(next, 60, o))
	assert.Equal(t, []string{"↑↓ row", "←→ time", "1-4 window, 0 all", "s sources", "n next entry", "x mark", "/ filter", "D Grafana", "r re-read", "Esc back"},
		v.calmKeys(st, next, 60, o))
	require.Equal(t, 2, calm.NextShown(next, 40, o))
	assert.Equal(t, []string{"↑↓ row", "←→ time", "1-4 window, 0 all", "s sources", "n next entry", "x mark", "/ filter", "m graph", "D Grafana", "r re-read", "Esc back"},
		v.calmKeys(st, next, 40, o))
	// A line with room for Enter alone leaves all detail to the second
	// line, where the kit keeps it longest.
	wide := []calm.Action{{Key: "Enter", Label: "open"}, {Key: calm.DetailKey, Label: timelineDetailWords}}
	require.Equal(t, 1, calm.NextShown(wide, 24, o))
	keys := v.calmKeys(st, wide, 24, o)
	assert.Equal(t, []string{"↑↓ row", "←→ time", "1-4 window, 0 all", "a full list", "s sources"}, keys[:5])
	assert.Contains(t, calm.RestLine(keys, 40, calm.Options{}), "a full list")
}
