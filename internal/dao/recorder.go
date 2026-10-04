// Modified for k9+; see NOTICE.
package dao

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/slogs"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/cache"
	mv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

var (
	MxRecorder *Recorder
	recorderMu sync.Mutex
)

const (
	seriesCacheSize   = 600
	seriesCacheExpiry = 3 * time.Hour
	seriesRecordRate  = 1 * time.Minute
	nodeMetrics       = "node"
	podMetrics        = "pod"
)

type MetricsChan chan TimeSeries

type TimeSeries []Point

type Point struct {
	Time   time.Time
	Tags   map[string]string
	Value  client.NodeMetrics
	Sample client.MetricSample
}

type Recorder struct {
	conn        client.Connection
	contextName string
	series      *cache.LRUExpireCache
	mxChan      MetricsChan
	mx          sync.RWMutex
	lastPoints  map[string]Point
	watchMu     sync.Mutex
	watchCancel context.CancelFunc
}

func DialRecorder(c client.Connection) *Recorder {
	recorderMu.Lock()
	defer recorderMu.Unlock()
	return dialRecorder(c)
}

func dialRecorder(c client.Connection) *Recorder {
	contextName := ""
	if c != nil {
		contextName = c.ActiveContext()
	}
	if MxRecorder != nil && MxRecorder.contextName == contextName {
		return MxRecorder
	}
	MxRecorder = &Recorder{
		conn:        c,
		contextName: contextName,
		series:      cache.NewLRUExpireCache(seriesCacheSize),
		lastPoints:  make(map[string]Point),
	}

	return MxRecorder
}

func ResetRecorder(c client.Connection) {
	recorderMu.Lock()
	defer recorderMu.Unlock()
	MxRecorder = nil
	dialRecorder(c)
}

func (r *Recorder) Clear() {
	r.mx.Lock()
	defer r.mx.Unlock()

	kk := r.series.Keys()
	clear(r.lastPoints)
	for _, k := range kk {
		r.series.Remove(k)
	}
}

func (r *Recorder) dispatchSeries(ctx context.Context, kind, ns string) {
	r.mx.RLock()
	defer r.mx.RUnlock()
	if r.mxChan == nil {
		return
	}
	kk := r.series.Keys()
	hour := time.Now().Add(-1 * time.Hour)
	ts := make(TimeSeries, 0, len(kk))
	for _, k := range kk {
		if v, ok := r.series.Get(k); ok {
			if pt, cool := v.(Point); cool {
				if pt.Tags["type"] != kind || pt.Time.Sub(hour) < 0 {
					continue
				}
				pt.Sample = client.NewMetricSample(pt.Sample.Values, pt.Sample.ObservedAt, pt.Sample.Source, time.Now())
				switch kind {
				case nodeMetrics:
					ts = append(ts, pt)
				case podMetrics:
					if client.IsAllNamespaces(ns) || pt.Tags["namespace"] == ns {
						ts = append(ts, pt)
					}
				}
			}
		}
	}
	if len(ts) > 0 {
		sort.Slice(ts, func(i, j int) bool { return ts[i].Time.Before(ts[j].Time) })
		select {
		case r.mxChan <- ts:
		case <-ctx.Done():
		}
	}
}

func (r *Recorder) Watch(ctx context.Context, ns string) MetricsChan {
	r.watchMu.Lock()
	defer r.watchMu.Unlock()
	// Cancel before taking mx: a previous publisher may be waiting for its
	// consumer while holding mx. Replacement must release that send first.
	if r.watchCancel != nil {
		r.watchCancel()
	}
	ctx, r.watchCancel = context.WithCancel(ctx)
	r.mx.Lock()
	if r.mxChan != nil {
		close(r.mxChan)
		r.mxChan = nil
	}
	r.mxChan = make(MetricsChan, 2)
	channel := r.mxChan
	r.mx.Unlock()

	go func() {
		kind := podMetrics
		if client.IsAllNamespaces(ns) {
			kind = nodeMetrics
		}
		r.dispatchSeries(ctx, kind, ns)
		switch kind {
		case podMetrics:
			if err := r.recordPodMetrics(ctx, ns); err != nil {
				slog.Error("Record pod metrics failed", slogs.Error, err)
			}
		case nodeMetrics:
			if err := r.recordNodeMetrics(ctx); err != nil {
				slog.Error("Record node metrics failed", slogs.Error, err)
				r.publishFailure(ctx, nodeMetrics, ns, client.NodeMetricsSource, err)
			}
		}
		<-ctx.Done()
		r.mx.Lock()
		if r.mxChan == channel {
			close(channel)
			r.mxChan = nil
		}
		r.mx.Unlock()
	}()

	return channel
}

func (r *Recorder) Record(ctx context.Context) error {
	if err := r.recordNodeMetrics(ctx); err != nil {
		return err
	}
	return r.recordPodMetrics(ctx, client.NamespaceAll)
}

func (r *Recorder) recordNodeMetrics(ctx context.Context) error {
	f, ok := ctx.Value(internal.KeyFactory).(Factory)
	if !ok {
		return errors.New("expecting factory in context")
	}
	go func() {
		for {
			if err := r.sampleNodeMetrics(ctx, f); err != nil {
				slog.Error("Record node metrics failed", slogs.Error, err)
				r.publishFailure(ctx, nodeMetrics, "", client.NodeMetricsSource, err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(seriesRecordRate):
			}
		}
	}()
	return nil
}

// Recheck discovery, permissions and node capacity on every observation so
// a missing or temporarily denied metrics API can recover in the open view.
func (r *Recorder) sampleNodeMetrics(ctx context.Context, f Factory) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := client.MetricsAccess(r.conn, client.ClusterScope, client.NmxGVR); err != nil {
		return err
	}
	authorized, err := r.conn.CanI(client.ClusterScope, client.NodeGVR, "", client.ListAccess)
	if err != nil {
		return err
	}
	if !authorized {
		return fmt.Errorf("node list prerequisite: %w", client.ErrMetricsDenied)
	}
	nn, err := FetchNodes(ctx, f, "")
	if err != nil {
		return err
	}

	r.recordClusterMetrics(ctx, nn)
	return nil
}

func (r *Recorder) recordClusterMetrics(ctx context.Context, nn *v1.NodeList) {
	dial := client.DialMetrics(r.conn)
	nmx, err := dial.FetchNodesMetrics(ctx)
	if err != nil {
		slog.Error("Fetch node metrics failed", slogs.Error, err)
		r.publishFailure(ctx, nodeMetrics, "", client.NodeMetricsSource, err)
		return
	}
	var values client.ClusterMetrics
	if err := dial.ClusterLoad(nn, nmx, &values); err != nil {
		r.publishFailure(ctx, nodeMetrics, "", client.NodeMetricsSource, err)
		return
	}

	mx := make(client.NodesMetrics, len(nn.Items))
	dial.NodesMetrics(nn, nmx, mx)
	var cmx client.NodeMetrics
	for _, m := range mx {
		cmx.CurrentCPU += m.CurrentCPU
		cmx.CurrentMEM += m.CurrentMEM
		cmx.AllocatableCPU += m.AllocatableCPU
		cmx.AllocatableMEM += m.AllocatableMEM
		cmx.TotalCPU += m.TotalCPU
		cmx.TotalMEM += m.TotalMEM
	}
	pt := Point{
		Time:   time.Now(),
		Value:  cmx,
		Sample: client.NewMetricSample(values, client.NodeMetricsObservedAt(nmx), client.NodeMetricsSource, time.Now()),
		Tags: map[string]string{
			"type": nodeMetrics,
		},
	}
	if pt.Sample.HasValue() {
		pt.Time = pt.Sample.ObservedAt
	}
	r.publishPoint(ctx, pt)
}

func (r *Recorder) recordPodMetrics(ctx context.Context, ns string) error {
	go func() {
		if err := r.recordPodsMetrics(ctx, ns); err != nil {
			slog.Error("Record pod metrics failed", slogs.Error, err)
			r.publishFailure(ctx, podMetrics, ns, client.PodMetricsSource, err)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(seriesRecordRate):
				// case <-time.After(5 * time.Second):
				if err := r.recordPodsMetrics(ctx, ns); err != nil {
					slog.Error("Record pod metrics failed", slogs.Error, err)
					r.publishFailure(ctx, podMetrics, ns, client.PodMetricsSource, err)
				}
			}
		}
	}()

	return nil
}

func (r *Recorder) recordPodsMetrics(ctx context.Context, ns string) error {
	f, ok := ctx.Value(internal.KeyFactory).(Factory)
	if !ok {
		return errors.New("expecting factory in context")
	}
	if err := client.MetricsAccess(r.conn, ns, client.PmxGVR); err != nil {
		return err
	}
	authorized, err := r.conn.CanI(ns, client.PodGVR, "", client.ListAccess)
	if err != nil {
		return err
	}
	if !authorized {
		return fmt.Errorf("pod list prerequisite: %w", client.ErrMetricsDenied)
	}
	pp, err := FetchPods(ctx, f, ns)
	if err != nil {
		return err
	}

	pt := Point{
		Time:  time.Now(),
		Value: client.NodeMetrics{},
		Tags: map[string]string{
			"namespace": ns,
			"type":      podMetrics,
		},
	}
	dial := client.DialMetrics(r.conn)
	metrics, err := dial.FetchPodsMetrics(ctx, ns)
	if err != nil {
		return err
	}
	if len(pp.Items) == 0 || len(metrics.Items) == 0 {
		return client.ErrMetricsEmpty
	}
	byName := make(map[string]*mv1beta1.PodMetrics, len(metrics.Items))
	for i := range metrics.Items {
		metric := &metrics.Items[i]
		byName[client.FQN(metric.Namespace, metric.Name)] = metric
	}
	for i := range pp.Items {
		p := pp.Items[i]
		fqn := client.FQN(p.Namespace, p.Name)
		pmx, ok := byName[fqn]
		if !ok || len(pmx.Containers) == 0 {
			return fmt.Errorf("%w: missing pod %s", client.ErrMetricsEmpty, fqn)
		}
		for _, c := range pmx.Containers {
			if _, ok := c.Usage[v1.ResourceCPU]; !ok {
				return client.ErrMetricsEmpty
			}
			if _, ok := c.Usage[v1.ResourceMemory]; !ok {
				return client.ErrMetricsEmpty
			}
			pt.Value.CurrentCPU += c.Usage.Cpu().MilliValue()
			pt.Value.CurrentMEM += client.ToMB(c.Usage.Memory().Value())
		}
	}
	if len(pp.Items) > 0 {
		pt.Value.AllocatableCPU = pt.Value.CurrentCPU
		pt.Value.AllocatableMEM = pt.Value.CurrentMEM
		pt.Sample = client.NewMetricSample(client.ClusterMetrics{
			PercCPU: client.ToPercentage(pt.Value.CurrentCPU, pt.Value.AllocatableCPU),
			PercMEM: client.ToPercentage(pt.Value.CurrentMEM, pt.Value.AllocatableMEM),
		}, client.PodMetricsObservedAt(metrics), client.PodMetricsSource, time.Now())
		if pt.Sample.HasValue() {
			pt.Time = pt.Sample.ObservedAt
		}
		r.publishPoint(ctx, pt)
	}

	return nil
}

func (r *Recorder) publishFailure(ctx context.Context, kind, namespace, source string, err error) {
	if kind == nodeMetrics {
		namespace = ""
	}
	r.mx.RLock()
	previous := r.lastPoints[kind+"|"+namespace]
	r.mx.RUnlock()
	point := Point{Time: time.Now(), Tags: map[string]string{"type": kind, "namespace": namespace}}
	point.Sample = client.MetricFailure(previous.Sample, client.MetricErrorState(err), source, err.Error())
	if point.Sample.HasValue() {
		point.Value = previous.Value
	}
	r.publishPoint(ctx, point)
}

//nolint:gocritic // Keep captured observations immutable across worker and UI boundaries.
func (r *Recorder) publishPoint(ctx context.Context, point Point) {
	if ctx.Err() != nil {
		return
	}
	r.mx.Lock()
	defer r.mx.Unlock()
	if ctx.Err() != nil {
		return
	}
	key := point.Tags["type"] + "|" + point.Tags["namespace"]
	if !point.Sample.HasValue() {
		previous := r.lastPoints[key]
		point.Sample = client.MetricFailure(previous.Sample, point.Sample.State, point.Sample.Source, point.Sample.Reason)
		if point.Sample.HasValue() {
			point.Value = previous.Value
		}
	}
	if point.Sample.Fresh() {
		if r.lastPoints == nil {
			r.lastPoints = make(map[string]Point)
		}
		r.lastPoints[key] = point
		r.series.Add(point.Time, point, seriesCacheExpiry)
	}
	if r.mxChan != nil {
		select {
		case r.mxChan <- TimeSeries{point}:
		case <-ctx.Done():
		}
	}
}

// FetchPods retrieves all pods in a given namespace.
func FetchPods(_ context.Context, f Factory, ns string) (*v1.PodList, error) {
	auth, err := f.Client().CanI(ns, client.PodGVR, "pods", []string{client.ListVerb})
	if err != nil {
		return nil, err
	}
	if !auth {
		return nil, fmt.Errorf("user is not authorized to list pods")
	}

	oo, err := f.List(client.PodGVR, ns, false, labels.Everything())
	if err != nil {
		return nil, err
	}
	pp := make([]v1.Pod, 0, len(oo))
	for _, o := range oo {
		var pod v1.Pod
		err = runtime.DefaultUnstructuredConverter.FromUnstructured(o.(*unstructured.Unstructured).Object, &pod)
		if err != nil {
			return nil, err
		}
		pp = append(pp, pod)
	}

	return &v1.PodList{Items: pp}, nil
}
