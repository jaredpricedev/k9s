// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"errors"
	"net/url"
	"sync"
	"sync/atomic"

	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/session"
)

type localLaunch struct {
	handle *session.Handle
	owner  model.Component
	ready  atomic.Bool
}

// Pending launches belong to their originating page; ready local resources
// belong to the session registry and survive navigation. Stack callbacks run on
// the UI dispatcher while workers only publish readiness or completion.
type localLaunchBook struct {
	mx       sync.Mutex
	bound    bool
	launches map[string]*localLaunch
}

func (a *App) ownLocalLaunch(handle *session.Handle, owner model.Component) *localLaunch {
	if !a.localLaunches.bound && a.Content != nil {
		a.Content.AddListener(&a.localLaunches)
		a.localLaunches.bound = true
	}
	launch := &localLaunch{handle: handle, owner: owner}
	a.localLaunches.mx.Lock()
	if a.localLaunches.launches == nil {
		a.localLaunches.launches = make(map[string]*localLaunch)
	}
	a.localLaunches.launches[handle.ID()] = launch
	a.localLaunches.mx.Unlock()
	go func() {
		<-handle.Done()
		a.localLaunches.mx.Lock()
		delete(a.localLaunches.launches, handle.ID())
		a.localLaunches.mx.Unlock()
	}()
	return launch
}

func (l *localLaunch) running(binding string) {
	l.ready.Store(true)
	l.handle.Running(binding)
}

func (b *localLaunchBook) StackPushed(top model.Component) {
	b.cancelPending(func(owner model.Component) bool { return owner != top })
}
func (b *localLaunchBook) StackPopped(old, _ model.Component) {
	b.cancelPending(func(owner model.Component) bool { return owner == old })
}
func (*localLaunchBook) StackTop(model.Component) {}

func (b *localLaunchBook) cancelPending(matches func(model.Component) bool) {
	b.mx.Lock()
	var handles []*session.Handle
	for _, launch := range b.launches {
		if !launch.ready.Load() && matches(launch.owner) {
			handles = append(handles, launch.handle)
		}
	}
	b.mx.Unlock()
	for _, handle := range handles {
		handle.Event("Originating page closed or changed before readiness; launch canceled.")
		handle.Cancel()
	}
}

func localSessionSpec(kind session.Kind, label string, target *SelectedResourceTarget, revision uint64) *session.Spec {
	spec := &session.Spec{Kind: kind, Label: label, Destination: session.Destination{Revision: revision}}
	if target != nil {
		spec.Destination.Context = target.Context
		targetGVR := ""
		if target.GVR != nil {
			targetGVR = target.GVR.String()
		}
		spec.Destination.GVR = targetGVR
		spec.Destination.Namespace, spec.Destination.Name, spec.Destination.UID = target.Namespace, target.Name, string(target.UID)
	}
	return spec
}

// A captured server identifies same-name contexts after reconnect without
// copying URL user-info, query credentials or fragments into lifecycle logs.
func localSessionEndpoint(host string) string {
	endpoint, err := url.Parse(host)
	if err != nil {
		return "captured endpoint unavailable for display"
	}
	endpoint.User, endpoint.RawQuery, endpoint.Fragment = nil, "", ""
	return endpoint.String()
}

func finishLocalCommand(handle *session.Handle, err error) {
	if err == nil {
		handle.Finish(session.Completed, "Owned child exited successfully; remote effects were not separately observed.")
		return
	}
	if errors.Is(err, errExternalOperationOutcome) {
		handle.Finish(session.Unknown, "Owned child ended; remote command effects are unknown. Inspect the captured destination before retrying.")
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		handle.Finish(session.Stopped, "Local launch canceled before a command outcome was accepted.")
		return
	}
	handle.Finish(session.Failed, "Local launch or command failed; no automatic retry. See the operation receipt for its known outcome.")
}
