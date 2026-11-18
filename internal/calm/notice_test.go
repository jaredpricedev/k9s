// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package calm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNoticeLine(t *testing.T) {
	failed := "Re-read failed, the earlier read is shown: the read timed out"
	assert.Equal(t, "  "+failed, plain(NoticeLine(failed, "", 132, plainOpts)), "indented like the page")
	cut := plain(NoticeLine(failed, "", 40, plainOpts))
	assert.Equal(t, "  Re-read failed, the earlier read is s…", cut, "cut to the width, and marked")
	assert.Equal(t, 40, cells(cut))
	assert.Equal(t, "  Limit 512Mi -> 256Mi | reopen", plain(NoticeLine("Limit 512Mi → 256Mi · reopen", "", 60, asciiOpts)), "plain characters with noIcons")
	escaped := draw(t, []string{NoticeLine("a [red]tag[-] is text", "", 60, plainOpts)}, 60)
	var text []rune
	for _, c := range escaped[0][:23] {
		text = append(text, c.r)
	}
	assert.Equal(t, "  a [red]tag[-] is text", string(text), "text is escaped, never read as a tag")

	// The colour is the one the view names, muted without one, and never
	// bold or dim: a notice is neither the answer nor a footnote.
	warned := draw(t, []string{NoticeLine(failed, testPalette.Warn, 80, colorOpts), NoticeLine(failed, "", 80, colorOpts)}, 80)
	require.Equal(t, 'R', warned[0][2].r)
	assert.Equal(t, testPalette.Warn, warned[0][2].fg)
	assert.Equal(t, testPalette.Muted, warned[1][2].fg)
	assert.False(t, warned[0][2].bold || warned[0][2].dim)
	assert.Empty(t, warned[0][0].fg, "the margin carries no colour")
}
