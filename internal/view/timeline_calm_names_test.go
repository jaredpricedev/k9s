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

// A Pod is written one way on a page: by the last word of its name, as its
// row, the answer and the next line write it. The list under the axis
// writes the whole name where its column holds it, and the short name
// where it does not; it never writes a third form with the middle cut out.
func TestTimelineCalmListWritesAPodByItsShortNameWhereTheWholeDoesNotFit(t *testing.T) {
	v := timelineIncidentView(t)
	// 132 columns hold the whole name: the one place the page has for it.
	frame := drawnText(t, v, 132, 33)
	require.Contains(t, frame, "\n> 11:50:00  pod/checkout-api-7d9f8c6b5d-x2x9q  Warning BackOff x14: ")
	// A list of one line names the kind too, in an 80x24 terminal that
	// also draws its line of crumbs: the whole name no longer fits beside
	// 28 cells of text, and what the short name leaves free is the text's.
	frame = drawnText(t, v, 80, 20)
	line, _ := timelineLineWith(t, frame, "> 11:50:00  ")
	assert.Equal(t, "> 11:50:00  warnings  pod x2x9q  Warning BackOff x14: Back-off restarting faile…", line)
	assert.Equal(t, 80, tviewCells(line), "the text takes what the name left free, to the last cell")
	frame = drawnText(t, v, 60, 21)
	line, _ = timelineLineWith(t, frame, "> 11:50:00  ")
	assert.Equal(t, "> 11:50:00  pod x2x9q  Warning BackOff x14: Back-off restar…", line)
	for _, size := range [][2]int{{132, 33}, {100, 30}, {80, 21}, {80, 20}, {60, 21}, {60, 20}, {44, 21}} {
		frame := drawnText(t, v, size[0], size[1])
		for _, cut := range []string{"pod/checkout-api-7…", "pod/checko…", "pod/chec…", "…x2x9q"} {
			assert.NotContains(t, frame, cut, "%dx%d: a Pod's name with its middle cut out\n%s", size[0], size[1], frame)
		}
		// The time cursor lists every kind in its column, each with its
		// object: the same rule holds there.
		v.cursorEdge(1)
		frame = drawnText(t, v, size[0], size[1])
		assert.NotRegexp(t, `pod/[a-z0-9-]*…`, frame, "%dx%d with the time cursor\n%s", size[0], size[1], frame)
		v.cursor = -1
		v.render()
	}
}

// The short name is the one every incident view has for the Pod: it never
// stands for two Pods, and a name that is not a Pod's is still cut in its
// middle, where its kind and its generated end tell it from its siblings.
func TestTimelineListObjectIsTheNameThePageUses(t *testing.T) {
	at := timelineAt
	pod := func(name string) timeline.Ref {
		return timeline.Ref{Resource: "v1/pods", Kind: "Pod", Namespace: "gitlab", Name: name}
	}
	unhealthy := func(ago time.Duration, name string) timeline.Entry {
		return timeline.Entry{At: at.Add(-ago), Source: timeline.SourceEvent, Failing: true, Summary: "Warning Unhealthy: Readiness probe failed", Ref: pod(name)}
	}
	// NewSnapshot orders the entries it is given: each story gets its own.
	entries := []timeline.Entry{
		unhealthy(9*time.Minute, "gitlab-webservice-default-6f9c7d5b84-hx4mt"),
		unhealthy(8*time.Minute, "gitlab-sidekiq-all-in-1-v2-5c7d9f-hx4mt"),
		{At: at.Add(-7 * time.Minute), Source: timeline.SourceRollout, Change: true, Summary: "rev 4: image v1 -> v2",
			Ref: timeline.Ref{Resource: "apps/v1/replicasets", Kind: "ReplicaSet", Namespace: "gitlab", Name: "gitlab-webservice-default-with-a-long-name-6f9c7d5b84"}},
	}
	scope := &timeline.Scope{Namespace: "gitlab"}
	st := timelineStoryOf(timeline.NewSnapshot(scope, at, entries, timelineReads(nil)), time.Hour, "", nil)
	o := calm.Options{}
	require.Len(t, st.shown, 3)
	first, second, rollout := &st.s.Entries[st.shown[0]], &st.s.Entries[st.shown[1]], &st.s.Entries[st.shown[2]]
	require.Equal(t, "gitlab-webservice-default-6f9c7d5b84-hx4mt", first.Ref.Name)
	require.Equal(t, "ReplicaSet", rollout.Ref.Kind)

	// Whole where it fits.
	assert.Equal(t, "pod/gitlab-webservice-default-6f9c7d5b84-hx4mt", st.listObject(first, 60, o))
	// Two Pods end in the same word: the short name is the whole name, as
	// calm.ShortName has it, so that it never stands for both. It is
	// written where it fits, and what does not fit is cut.
	assert.Equal(t, "pod gitlab-webservice-default-6f9c7d5b84-hx4mt", st.object(first))
	whole := "pod/gitlab-sidekiq-all-in-1-v2-5c7d9f-hx4mt"
	assert.Equal(t, whole, st.listObject(second, tviewCells(whole), o))
	cut := st.listObject(first, 30, o)
	assert.True(t, strings.HasPrefix(cut, "pod/gitlab-") && strings.HasSuffix(cut, "-hx4mt") && strings.Contains(cut, "…"), cut)
	assert.LessOrEqual(t, tviewCells(cut), 30)
	// Not a Pod: its kind and both ends of its name.
	cut = st.listObject(rollout, 30, o)
	assert.True(t, strings.HasPrefix(cut, "rs/gitlab-") && strings.HasSuffix(cut, "c7d5b84") && strings.Contains(cut, "…"), cut)
	assert.LessOrEqual(t, tviewCells(cut), 30)

	// One Pod alone is the last word of its name.
	alone := timelineStoryOf(timeline.NewSnapshot(scope, at, []timeline.Entry{unhealthy(9*time.Minute, "gitlab-webservice-default-6f9c7d5b84-hx4mt")}, timelineReads(nil)), time.Hour, "", nil)
	require.Len(t, alone.shown, 1)
	entry := alone.shown[0]
	assert.Equal(t, "pod hx4mt", alone.listObject(&alone.s.Entries[entry], 30, o))
	// 36 cells are the most the list gives an object, at any width: a name
	// of 46 is the short name there too.
	cols := alone.detailCols([]int{entry}, false, 132, o)
	assert.Equal(t, tviewCells("pod hx4mt"), cols.object)
	// The column is as wide as what is written in it: the text has the rest.
	assert.Equal(t, 132-calm.Indent-cols.clock-2-cols.object-2, cols.text)
}
