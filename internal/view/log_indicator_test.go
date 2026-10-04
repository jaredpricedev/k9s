// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package view_test

import (
	"testing"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/view"
	"github.com/stretchr/testify/assert"
)

func TestLogIndicatorRefresh(t *testing.T) {
	defaults := config.NewStyles()
	uu := map[string]struct {
		li *view.LogIndicator
		e  string
	}{
		"all-containers": {
			view.NewLogIndicator(config.NewConfig(nil), defaults, true), "[::b]AllContainers:[#a5b0aa::-]Off[-::]     [::b]Autoscroll:[#79c7d4::b]On[-::]      [::b]ColumnLock:[#a5b0aa::-]Off[-::]     [::b]FullScreen:[#a5b0aa::-]Off[-::]     [::b]Timestamps:[#a5b0aa::-]Off[-::]     [::b]Wrap:[#a5b0aa::-]Off[-::]\n",
		},
		"plain": {
			view.NewLogIndicator(config.NewConfig(nil), defaults, false), "[::b]Autoscroll:[#79c7d4::b]On[-::]      [::b]ColumnLock:[#a5b0aa::-]Off[-::]     [::b]FullScreen:[#a5b0aa::-]Off[-::]     [::b]Timestamps:[#a5b0aa::-]Off[-::]     [::b]Wrap:[#a5b0aa::-]Off[-::]\n",
		},
	}

	for k := range uu {
		u := uu[k]
		t.Run(k, func(t *testing.T) {
			u.li.Refresh()
			assert.Equal(t, u.e, u.li.GetText(false))
		})
	}
}

func BenchmarkLogIndicatorRefresh(b *testing.B) {
	defaults := config.NewStyles()
	v := view.NewLogIndicator(config.NewConfig(nil), defaults, true)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		v.Refresh()
	}
}
