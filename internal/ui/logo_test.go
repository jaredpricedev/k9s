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
	assert.Equal(t, 10, strings.Count(v.Logo().GetText(false), "[#79c7d4::b]"))
	assert.Empty(t, v.Status().GetText(false))
}

func TestLogoStatus(t *testing.T) {
	uu := map[string]struct {
		logo, msg, e string
	}{
		"info": {
			"[#91b8ec::b]",
			"blee",
			"[#e1e7e3::b]blee\n",
		},
		"warn": {
			"[#e7bd73::b]",
			"blee",
			"[#e1e7e3::b]blee\n",
		},
		"err": {
			"[#ef8278::b]",
			"blee",
			"[#e1e7e3::b]blee\n",
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
