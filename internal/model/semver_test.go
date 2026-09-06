// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package model_test

import (
	"testing"

	"github.com/derailed/k9s/internal/model"
	"github.com/stretchr/testify/assert"
)

func TestNewSemVer(t *testing.T) {
	uu := map[string]struct {
		version             string
		major, minor, patch int
	}{
		"plain": {
			version: "0.11.1",
			major:   0,
			minor:   11,
			patch:   1,
		},
		"normalized": {
			version: "v10.11.12",
			major:   10,
			minor:   11,
			patch:   12,
		},
	}

	for k := range uu {
		u := uu[k]
		t.Run(k, func(t *testing.T) {
			v := model.NewSemVer(u.version)
			assert.Equal(t, u.major, v.Major)
			assert.Equal(t, u.minor, v.Minor)
			assert.Equal(t, u.patch, v.Patch)
		})
	}
}

func TestSemVerIsCurrent(t *testing.T) {
	uu := map[string]struct {
		current, latest string
		e               bool
	}{
		"same": {
			current: "0.11.1",
			latest:  "0.11.1",
			e:       true,
		},
		"older": {
			current: "v10.11.12",
			latest:  "v10.11.13",
		},
		"newer": {
			current: "10.11.13",
			latest:  "10.11.12",
			e:       true,
		},
	}

	for k := range uu {
		u := uu[k]
		t.Run(k, func(t *testing.T) {
			v1, v2 := model.NewSemVer(u.current), model.NewSemVer(u.latest)
			assert.Equal(t, u.e, v1.IsCurrent(v2))
		})
	}
}

func TestSemVerPrereleaseAndMajorOrdering(t *testing.T) {
	for _, tc := range []struct {
		current, latest string
		currentEnough   bool
	}{
		{"v0.1.0-dev", "v0.1.0", false},
		{"v0.1.0-dev-abc+build.1", "v0.1.0-dev-abc+build.2", true},
		{"v1.0.0", "v0.99.99", true},
		{"v1.1.0", "v1.0.99", true},
		{"v0.1.0-rc.2", "v0.1.0-rc.10", false},
	} {
		t.Run(tc.current+"/"+tc.latest, func(t *testing.T) {
			current := model.NewSemVer(tc.current)
			assert.Equal(t, tc.current, current.String())
			assert.Equal(t, tc.currentEnough, current.IsCurrent(model.NewSemVer(tc.latest)))
		})
	}
}
