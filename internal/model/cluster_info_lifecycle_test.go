// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package model

import (
	"sync/atomic"
	"testing"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/stretchr/testify/require"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

type lifecycleHeaderConnection struct {
	client.Connection
	configuration *client.Config
	calls         atomic.Int32
	entered       chan struct{}
	release       chan struct{}
}

func (c *lifecycleHeaderConnection) Config() *client.Config { return c.configuration }
func (c *lifecycleHeaderConnection) ActiveNamespace() string {
	if c.calls.Load() == 0 {
		return "old"
	}
	return "new"
}
func (c *lifecycleHeaderConnection) ConnectionOK() bool {
	if c.calls.Add(1) == 1 {
		close(c.entered)
		<-c.release
	}
	return false
}

type lifecycleHeaderListener struct{ updates chan *ClusterMeta }

func (l lifecycleHeaderListener) ClusterInfoChanged(_, current *ClusterMeta) { l.updates <- current }
func (l lifecycleHeaderListener) ClusterInfoUpdated(current *ClusterMeta)    { l.updates <- current }

func TestClusterInfoLatestRefreshAndSameContextResetInvalidateQueuedMetadata(t *testing.T) {
	contextName, clusterName, userName := "A", "cluster-A", "user"
	flags := genericclioptions.NewConfigFlags(false)
	flags.Context, flags.ClusterName, flags.AuthInfoName = &contextName, &clusterName, &userName
	conn := &lifecycleHeaderConnection{configuration: client.NewConfig(flags), entered: make(chan struct{}), release: make(chan struct{})}
	factory := headerMetricsFactory{conn: conn}
	info := NewClusterInfo(factory, "v1.0.0", &config.K9s{SkipLatestRevCheck: true})
	listener := lifecycleHeaderListener{updates: make(chan *ClusterMeta, 3)}
	info.AddListener(listener)
	finished := make(chan struct{})
	go func() { info.Refresh(); close(finished) }()
	<-conn.entered
	info.Refresh()
	current := <-listener.updates
	require.Equal(t, "new", current.Namespace)
	require.True(t, current.IsCurrent())
	close(conn.release)
	<-finished
	require.Empty(t, listener.updates, "a superseded refresh must not publish after the newer observation")
	info.Reset(factory)
	replacement := <-listener.updates
	require.Equal(t, current.Context, replacement.Context)
	require.False(t, current.IsCurrent(), "queued metadata must be invalidated even if the destination's name matches again")
	require.True(t, replacement.IsCurrent())
	info.RemoveListener(listener)
	info.Refresh()
	require.Empty(t, listener.updates)
}
