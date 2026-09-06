// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package ui_test

import (
	"strings"
	"testing"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/ui"
	"github.com/stretchr/testify/assert"
)

func TestNewLogoView(t *testing.T) {
	v := ui.NewLogo(config.NewStyles())
	v.Reset()

	assert.Contains(t, v.Logo().GetText(false), "██")
	assert.Equal(t, 10, strings.Count(v.Logo().GetText(false), "[#ffa500::b]"))
	assert.Empty(t, v.Status().GetText(false))
}

func TestLogoStatus(t *testing.T) {
	uu := map[string]struct {
		logo, msg, e string
	}{
		"info": {
			"[#008000::b]",
			"blee",
			"[#ffffff::b]blee\n",
		},
		"warn": {
			"[#c71585::b]",
			"blee",
			"[#ffffff::b]blee\n",
		},
		"err": {
			"[#ff0000::b]",
			"blee",
			"[#ffffff::b]blee\n",
		},
	}

	v := ui.NewLogo(config.NewStyles())
	for n := range uu {
		k, u := n, uu[n]
		t.Run(k, func(t *testing.T) {
			switch k {
			case "info":
				v.Info(u.msg)
			case "warn":
				v.Warn(u.msg)
			case "err":
				v.Err(u.msg)
			}
			assert.Contains(t, v.Logo().GetText(false), "██")
			assert.Equal(t, 10, strings.Count(v.Logo().GetText(false), u.logo))
			assert.Equal(t, u.e, v.Status().GetText(false))
		})
	}
}
