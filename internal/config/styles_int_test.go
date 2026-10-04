// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_newStyle(t *testing.T) {
	s := newStyle()

	assert.Equal(t, Color("#0b0e11"), s.Body.BgColor)
	assert.Equal(t, Color("#e1e7e3"), s.Body.FgColor)
	assert.Equal(t, Color("#e1e7e3"), s.Frame.Status.NewColor)
}
