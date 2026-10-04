// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/slogs"
	"k8s.io/apimachinery/pkg/util/cache"
)

const (
	k9sGitURL       = "https://api.github.com/repos/jaredpricedev/k9s/releases/latest"
	cacheSize       = 10
	cacheExpiry     = 1 * time.Hour
	k9sLatestRevKey = "k9sRev"
)

// ClusterInfoListener registers a listener for model changes.
type ClusterInfoListener interface {
	// ClusterInfoChanged notifies the cluster meta was changed.
	ClusterInfoChanged(prev, curr *ClusterMeta)

	// ClusterInfoUpdated notifies the cluster meta was updated.
	ClusterInfoUpdated(*ClusterMeta)
}

// ClusterMeta represents cluster meta data.
type ClusterMeta struct {
	Context, Cluster    string
	Namespace           string
	Connected           bool
	User                string
	K9sVer, K9sLatest   string
	K8sVer              string
	Cpu, Mem, Ephemeral int
	Metrics             client.MetricSample
	publication         *atomic.Uint64
	revision            uint64
}

// IsCurrent prevents a queued callback from publishing superseded metadata,
// including an A -> B -> A context switch where the context name matches again.
func (c *ClusterMeta) IsCurrent() bool {
	return c != nil && (c.publication == nil || c.publication.Load() == c.revision)
}

// NewClusterMeta returns a new instance.
func NewClusterMeta() *ClusterMeta {
	return &ClusterMeta{
		Context:   client.NA,
		Cluster:   client.NA,
		User:      client.NA,
		K9sVer:    client.NA,
		K8sVer:    client.NA,
		Cpu:       0,
		Mem:       0,
		Ephemeral: 0,
		Metrics:   client.MetricSample{State: client.MetricsUnavailable, Source: client.NodeMetricsSource, Reason: "waiting for a sample"},
	}
}

// Deltas diffs cluster meta return true if different, false otherwise.
func (c *ClusterMeta) Deltas(n *ClusterMeta) bool {
	if c.Cpu != n.Cpu || c.Mem != n.Mem || c.Ephemeral != n.Ephemeral {
		return true
	}

	return c.Context != n.Context ||
		c.Namespace != n.Namespace ||
		c.Connected != n.Connected ||
		c.Metrics != n.Metrics ||
		c.Cluster != n.Cluster ||
		c.User != n.User ||
		c.K8sVer != n.K8sVer ||
		c.K9sVer != n.K9sVer ||
		c.K9sLatest != n.K9sLatest
}

// ClusterInfo models cluster metadata.
type ClusterInfo struct {
	cluster     *Cluster
	factory     dao.Factory
	data        *ClusterMeta
	version     string
	cfg         *config.K9s
	listeners   []ClusterInfoListener
	cache       *cache.LRUExpireCache
	mx          sync.RWMutex
	request     uint64
	publication atomic.Uint64
}

// NewClusterInfo returns a new instance.
func NewClusterInfo(f dao.Factory, v string, cfg *config.K9s) *ClusterInfo {
	c := ClusterInfo{
		factory: f,
		cluster: NewCluster(f),
		data:    NewClusterMeta(),
		version: v,
		cfg:     cfg,
		cache:   cache.NewLRUExpireCache(cacheSize),
	}

	return &c
}

func (c *ClusterInfo) fetchK9sLatestRev() string {
	rev, ok := c.cache.Get(k9sLatestRevKey)
	if ok {
		return rev.(string)
	}

	latestRev, err := fetchLatestRev()
	if err != nil {
		slog.Warn("k9+ latest rev fetch failed", slogs.Error, err)
	} else {
		c.cache.Add(k9sLatestRevKey, latestRev, cacheExpiry)
	}

	return latestRev
}

// Reset resets context and reload.
func (c *ClusterInfo) Reset(f dao.Factory) {
	c.RebindFactory(f)
	c.Refresh()
}

// RebindFactory invalidates queued observations before exposing a new factory.
// It performs no API reads; Refresh may subsequently run on a worker.
func (c *ClusterInfo) RebindFactory(f dao.Factory) {
	if f == nil {
		return
	}

	c.mx.Lock()
	c.request++
	c.publication.Store(c.request)
	c.factory, c.cluster, c.data = f, NewCluster(f), NewClusterMeta()
	c.mx.Unlock()

}

// Refresh fetches the latest cluster meta.
func (c *ClusterInfo) Refresh() {
	c.mx.Lock()
	cluster, previous := c.cluster, c.data
	c.request++
	request := c.request
	c.mx.Unlock()
	data := NewClusterMeta()
	data.Context = cluster.ContextName()
	data.Cluster = cluster.ClusterName()
	data.User = cluster.UserName()
	data.Namespace = cluster.factory.Client().ActiveNamespace()
	if cluster.factory.Client().ConnectionOK() {
		data.Connected = true
		data.K8sVer = cluster.Version()
		ctx, cancel := context.WithTimeout(context.Background(), cluster.factory.Client().Config().CallTimeout())
		defer cancel()
		data.Metrics = cluster.MetricsSample(ctx, previous.Metrics)
	} else {
		data.Metrics = client.MetricFailure(previous.Metrics, client.MetricsUnavailable, client.NodeMetricsSource, "cluster disconnected")
	}
	data.Cpu, data.Mem, data.Ephemeral = data.Metrics.Values.PercCPU, data.Metrics.Values.PercMEM, data.Metrics.Values.PercEphemeral
	data.K9sVer = c.version
	v1 := NewSemVer(data.K9sVer)

	var latestRev string
	if !c.cfg.SkipLatestRevCheck {
		latestRev = c.fetchK9sLatestRev()
	}
	v2 := NewSemVer(latestRev)

	data.K9sVer, data.K9sLatest = v1.String(), v2.String()
	if v1.IsCurrent(v2) {
		data.K9sLatest = ""
	}

	c.mx.Lock()
	if c.cluster != cluster || c.request != request {
		c.mx.Unlock()
		return // A context switch superseded this collection.
	}
	data.publication, data.revision = &c.publication, request
	c.publication.Store(request)
	c.data = data
	c.mx.Unlock()
	if previous.Deltas(data) {
		c.fireMetaChanged(previous, data)
	} else {
		c.fireNoMetaChanged(data)
	}
}

// AddListener adds a new model listener.
func (c *ClusterInfo) AddListener(l ClusterInfoListener) {
	c.mx.Lock()
	defer c.mx.Unlock()
	c.listeners = append(c.listeners, l)
}

// RemoveListener delete a listener from the list.
func (c *ClusterInfo) RemoveListener(l ClusterInfoListener) {
	c.mx.Lock()
	defer c.mx.Unlock()
	victim := -1
	for i, lis := range c.listeners {
		if lis == l {
			victim = i
			break
		}
	}

	if victim >= 0 {
		c.listeners = append(c.listeners[:victim], c.listeners[victim+1:]...)
	}
}

func (c *ClusterInfo) fireMetaChanged(prev, cur *ClusterMeta) {
	for _, l := range c.listenerSnapshot() {
		l.ClusterInfoChanged(prev, cur)
	}
}

func (c *ClusterInfo) fireNoMetaChanged(data *ClusterMeta) {
	for _, l := range c.listenerSnapshot() {
		l.ClusterInfoUpdated(data)
	}
}

func (c *ClusterInfo) listenerSnapshot() []ClusterInfoListener {
	c.mx.RLock()
	defer c.mx.RUnlock()
	return append([]ClusterInfoListener(nil), c.listeners...)
}

// Helpers...

func fetchLatestRev() (string, error) {
	slog.Debug("Fetching latest k9+ rev...")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k9sGitURL, http.NoBody)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()

	// An independent fork may not have published its first release yet.
	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("release lookup returned HTTP %d", resp.StatusCode)
	}
	var release struct {
		Tag string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil {
		return "", err
	}
	if release.Tag == "" {
		return "", errors.New("no version found")
	}
	return release.Tag, nil
}
