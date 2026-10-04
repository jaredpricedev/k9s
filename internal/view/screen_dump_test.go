// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package view_test

import (
	"testing"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/view"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScreenDumpNew(t *testing.T) {
	po := view.NewScreenDump(client.SdGVR)

	require.NoError(t, po.Init(makeCtx(t)))
	assert.Equal(t, "ScreenDumps", po.Name())

	assertActionRegistry(t, po)
}
