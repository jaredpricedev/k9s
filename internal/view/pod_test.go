// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package view_test

import (
	"context"
	"testing"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config/mock"
	"github.com/derailed/k9s/internal/view"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPodNew(t *testing.T) {
	po := view.NewPod(client.PodGVR)

	require.NoError(t, po.Init(makeCtx(t)))
	assert.Equal(t, "Pods", po.Name())

	assertActionRegistry(t, po)
}

// Helpers...

func makeCtx(t testing.TB) context.Context {
	cfg := mock.NewMockConfig(t)
	return context.WithValue(context.Background(), internal.KeyApp, view.NewApp(cfg))
}
