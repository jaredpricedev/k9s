package model

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/derailed/k9s/internal"
	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/render"
	"github.com/derailed/k9s/internal/slogs"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	pulseRate         = 10 * time.Second
	pulseCheckTimeout = 5 * time.Second
)

type HealthState string

const (
	HealthLoading     HealthState = "loading"
	HealthAvailable   HealthState = "available"
	HealthEmpty       HealthState = "empty"
	HealthDenied      HealthState = "denied"
	HealthAbsent      HealthState = "absent"
	HealthUnavailable HealthState = "unavailable"
	HealthStale       HealthState = "stale"
)

type HealthPoint struct {
	GVR                        *client.GVR
	Total, Faults              int
	State, Failure             HealthState
	Source, Namespace, Message string
	ObservedAt, CheckedAt      time.Time
}

func (p HealthPoint) HasValue() bool {
	return !p.ObservedAt.IsZero() && (p.State == HealthAvailable || p.State == HealthEmpty || p.State == HealthStale)
}

// At keeps a stopped or delayed collector from presenting retained data as fresh.
func (p HealthPoint) At(now time.Time) HealthPoint {
	if p.HasValue() && p.State != HealthStale && now.Sub(p.CheckedAt) > 2*pulseRate {
		p.State, p.Failure, p.Message = HealthStale, HealthUnavailable, "No recent collection; check the cluster connection"
	}
	return p
}

type GVRs []*client.GVR

var PulseGVRs = client.GVRs{
	client.NodeGVR,
	client.NsGVR,
	client.SvcGVR,
	client.EvGVR,

	client.PodGVR,
	client.DpGVR,
	client.StsGVR,
	client.DsGVR,

	client.JobGVR,
	client.CjGVR,
	client.PvGVR,
	client.PvcGVR,

	client.HpaGVR,
	client.IngGVR,
	client.NpGVR,
	client.SaGVR,
}

func (g GVRs) First() *client.GVR {
	return g[0]
}

func (g GVRs) Last() *client.GVR {
	return g[len(g)-1]
}

func (g GVRs) Index(gvr *client.GVR) int {
	for i := range g {
		if g[i] == gvr {
			return i
		}
	}

	return -1
}

// PulseHealth tracks resources health.
type PulseHealth struct {
	factory dao.Factory
	mx      sync.Mutex
	busy    map[*client.GVR]bool
}

// NewPulseHealth returns a new instance.
func NewPulseHealth(f dao.Factory) *PulseHealth {
	return &PulseHealth{factory: f, busy: make(map[*client.GVR]bool)}
}

func (h *PulseHealth) Watch(ctx context.Context, ns string) HealthChan {
	c := make(HealthChan, 2)
	ctx = context.WithValue(ctx, internal.KeyWithMetrics, false)

	go func(ctx context.Context, ns string, c HealthChan) {
		kinds := PulseGVRs
		if ns != "" && !client.IsAllNamespaces(ns) {
			kinds = PulseGVRs[2:]
		}
		last := make(map[*client.GVR]HealthPoint, len(kinds))
		defer close(c)
		for _, gvr := range kinds {
			select {
			case c <- HealthPoint{GVR: gvr, State: HealthLoading, Namespace: ns}:
			case <-ctx.Done():
				return
			}
		}
		if err := h.checkPulse(ctx, ns, c, last, kinds); err != nil {
			slog.Error("Pulse check failed", slogs.Error, err)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(pulseRate):
				if err := h.checkPulse(ctx, ns, c, last, kinds); err != nil {
					slog.Error("Pulse check failed", slogs.Error, err)
				}
			}
		}
	}(ctx, ns, c)

	return c
}

func (h *PulseHealth) checkPulse(ctx context.Context, ns string, c HealthChan, last map[*client.GVR]HealthPoint, kinds client.GVRs) error {
	slog.Debug("Checking pulses...")
	var failures []error
	for _, gvr := range kinds {
		if err := ctx.Err(); err != nil {
			return err
		}
		checkCtx, cancel := context.WithTimeout(ctx, pulseCheckTimeout)
		check, err := h.checkBounded(checkCtx, ns, gvr)
		cancel()
		if err != nil {
			failures = append(failures, err)
		}
		if check.State != HealthAvailable && check.State != HealthEmpty && last[gvr].HasValue() {
			previous := last[gvr]
			check.Total, check.Faults, check.ObservedAt, check.Source = previous.Total, previous.Faults, previous.ObservedAt, previous.Source
			check.Failure, check.State = check.State, HealthStale
		}
		last[gvr] = check
		select {
		case c <- check:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return errors.Join(failures...)
}

// A few inherited cache/authorization APIs do not accept a context. Keep at
// most one such read per kind in flight and never let it hold up other kinds
// beyond this attempt's deadline. Late results cannot replace a newer attempt.
func (h *PulseHealth) checkBounded(ctx context.Context, ns string, gvr *client.GVR) (HealthPoint, error) {
	unavailable := func(message string) HealthPoint {
		return HealthPoint{GVR: gvr, Namespace: ns, State: HealthUnavailable, CheckedAt: time.Now(), Message: message}
	}
	h.mx.Lock()
	if h.busy[gvr] {
		h.mx.Unlock()
		return unavailable("Previous collection is still running"), errors.New("previous collection is still running")
	}
	h.busy[gvr] = true
	h.mx.Unlock()
	type result struct {
		point HealthPoint
		err   error
	}
	results := make(chan result, 1)
	go func() {
		var point HealthPoint
		var err error
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("resource health decoding failed: %v", recovered)
				point = unavailable(err.Error())
			}
			h.mx.Lock()
			delete(h.busy, gvr)
			h.mx.Unlock()
			results <- result{point, err}
		}()
		point, err = h.check(ctx, ns, gvr)
	}()
	select {
	case r := <-results:
		return r.point, r.err
	case <-ctx.Done():
		return unavailable(ctx.Err().Error()), ctx.Err()
	}
}

func (h *PulseHealth) check(ctx context.Context, ns string, gvr *client.GVR) (HealthPoint, error) {
	if conn := h.factory.Client(); conn != nil && !conn.ConnectionOK() {
		return HealthPoint{GVR: gvr, Namespace: ns, State: HealthUnavailable, CheckedAt: time.Now(), Message: "Cluster disconnected"}, errors.New("cluster disconnected")
	}
	meta, ok := Registry[gvr]
	if !ok {
		meta = ResourceMeta{
			DAO:      new(dao.Table),
			Renderer: new(render.Table),
		}
	}
	// Registry accessors are prototypes; a collector must not mutate the DAO
	// being used by a browser or another Pulse generation.
	meta.DAO = pulseAccessor(gvr)

	meta.DAO.Init(h.factory, gvr)
	oo, err := meta.DAO.List(ctx, ns)
	c := HealthPoint{GVR: gvr, Namespace: ns, CheckedAt: time.Now(), Source: "informer cache"}
	if _, table := meta.DAO.(*dao.Table); table {
		c.Source = "API table read"
	}
	if err != nil {
		c.State, c.Message = healthErrorState(err), err.Error()
		return c, err
	}
	// Informer listers can return an empty list before their initial API read.
	if c.Source == "informer cache" {
		if syncer, ok := h.factory.(interface {
			HasSynced(*client.GVR, string) (bool, error)
		}); ok {
			synced, err := syncer.HasSynced(gvr, ns)
			if err != nil {
				c.State, c.Message = healthErrorState(err), err.Error()
				return c, err
			}
			if !synced {
				c.State, c.Message = HealthLoading, "Waiting for the initial informer read"
				return c, nil
			}
		}
	}
	c.State, c.ObservedAt, c.Total = HealthAvailable, c.CheckedAt, len(oo)
	if isTable(oo) {
		ta := oo[0].(*metav1.Table)
		c.Total = len(ta.Rows)
		for _, row := range ta.Rows {
			if err := meta.Renderer.Healthy(ctx, row); err != nil {
				c.Faults++
			}
		}
	} else {
		for _, o := range oo {
			if err := meta.Renderer.Healthy(ctx, o); err != nil {
				c.Faults++
			}
		}
	}
	if c.Total == 0 {
		c.State = HealthEmpty
	}
	slog.Debug("Checked", slogs.GVR, gvr, slogs.Config, c)

	return c, nil
}

func healthErrorState(err error) HealthState {
	switch {
	case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err), strings.Contains(err.Error(), "access denied"), strings.Contains(err.Error(), "not authorized"):
		return HealthDenied
	case apierrors.IsNotFound(err), meta.IsNoMatchError(err):
		return HealthAbsent
	default:
		return HealthUnavailable
	}
}

func pulseAccessor(gvr *client.GVR) dao.Accessor {
	switch gvr {
	case client.NodeGVR:
		return new(dao.Node)
	case client.NsGVR:
		return new(dao.Namespace)
	case client.SvcGVR:
		return new(dao.Service)
	case client.EvGVR:
		return new(dao.Table)
	case client.PodGVR:
		return new(dao.Pod)
	case client.DpGVR:
		return new(dao.Deployment)
	case client.StsGVR:
		return new(dao.StatefulSet)
	case client.DsGVR:
		return new(dao.DaemonSet)
	case client.JobGVR:
		return new(dao.Job)
	case client.CjGVR:
		return new(dao.CronJob)
	default:
		return new(dao.Resource)
	}
}

func isTable(oo []runtime.Object) bool {
	if len(oo) == 0 || len(oo) > 1 {
		return false
	}
	_, ok := oo[0].(*metav1.Table)

	return ok
}
