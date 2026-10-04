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

func TestDaemonSet(t *testing.T) {
	v := view.NewDaemonSet(client.DsGVR)

	require.NoError(t, v.Init(makeCtx(t)))
	assert.Equal(t, "DaemonSets", v.Name())

	assertActionRegistry(t, v)
}
