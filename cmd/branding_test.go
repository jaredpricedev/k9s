// SPDX-License-Identifier: Apache-2.0
// Copyright k9+ contributors
// Modified for k9+; see NOTICE.

package cmd

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestK9PlusCommandIdentity(t *testing.T) {
	assert.Equal(t, "k9plus", rootCmd.Name())
	assert.Contains(t, rootCmd.Long, "k9+")
	assert.Contains(t, rootCmd.Long, "k9s")
	assert.Contains(t, rootCmd.Flags().Lookup("headless").Usage, "k9+")
	var buffer bytes.Buffer
	previous := out
	out = &buffer
	t.Cleanup(func() { out = previous })
	printVersion(false)
	assert.Contains(t, buffer.String(), "k9+")
}
