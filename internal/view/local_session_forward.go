// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/port"
	"github.com/derailed/k9s/internal/session"
	"github.com/derailed/k9s/internal/watch"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
)

const localSessionSetupTimeout = 8 * time.Second

type ownedForward struct {
	app       *App
	factory   *watch.Factory
	owner     model.Component
	revision  uint64
	target    SelectedResourceTarget
	path      string
	tunnel    port.PortTunnel
	config    *rest.Config
	forwarder *dao.PortForwarder
	launch    *localLaunch
	ctx       context.Context
	cancel    context.CancelFunc
}

type forwardDestination struct {
	app      *App
	factory  *watch.Factory
	owner    model.Component
	revision uint64
	target   SelectedResourceTarget
	origin   SelectedResourceTarget
	path     string
	config   *rest.Config
}

func (d *forwardDestination) start(tunnels port.PortTunnels) error {
	app := d.app
	if !app.IsRunning() || app.factory != d.factory || app.Content.Top() != d.owner || app.Config.DestinationRevision() != d.revision {
		return fmt.Errorf("destination changed; reopen the forwarding action")
	}
	for _, tunnel := range tunnels {
		if _, ok := d.factory.ForwarderFor(dao.PortForwardID(d.path, tunnel.Container, tunnel.PortMap())); ok {
			return fmt.Errorf("port-forward is already owned for the selected Pod and mapping")
		}
	}
	for _, tunnel := range tunnels {
		forward := &ownedForward{app: app, factory: d.factory, owner: d.owner, revision: d.revision,
			target: d.target, path: d.path, tunnel: tunnel, config: rest.CopyConfig(d.config), forwarder: dao.NewPortForwarder(d.factory)}
		forward.ctx, forward.cancel = context.WithCancel(app.sessionContext())
		spec := localSessionSpec(session.PortForward, "Pod port-forward", &d.target, forward.revision)
		spec.Destination.Server = localSessionEndpoint(d.config.Host)
		spec.Origin = localSessionSpec(session.PortForward, "", &d.origin, d.revision).Destination
		spec.Origin.Server = spec.Destination.Server
		spec.Destination.Container = tunnel.Container
		spec.Binding = forward.binding(nil)
		spec.IdentityNote = "Pod UID is checked before opening its endpoint. Kubernetes port-forward has no atomic UID precondition; " +
			"a same-name replacement after that check remains an endpoint limitation. This access does not create debug containers."
		handle, addErr := app.localSessions.Add(spec, func() { forward.cancel(); forward.forwarder.Stop() })
		if addErr != nil {
			forward.cancel()
			return addErr
		}
		forward.launch = app.ownLocalLaunch(handle, forward.owner)
		go forward.run()
	}
	app.Flash().Info("Port-forward setup started; :sessions shows readiness, binding and cleanup")
	return nil
}

func (f *ownedForward) current() bool {
	return f.app.IsRunning() && f.app.factory == f.factory && f.app.Config.DestinationRevision() == f.revision &&
		f.app.Content.Top() == f.owner && f.ctx.Err() == nil
}

func (f *ownedForward) run() {
	defer f.cancel()
	stopCancellation := context.AfterFunc(f.ctx, f.forwarder.Stop)
	defer stopCancellation()
	setup, cancelSetup := context.WithTimeout(f.ctx, localSessionSetupTimeout)
	defer cancelSetup()
	if err := (port.PortTunnels{f.tunnel}).CheckAvailable(setup); err != nil {
		f.launch.handle.Finish(session.Failed, "Local binding is unavailable; no listener was started and no existing process was stopped.")
		return
	}
	stream, err := f.forwarder.StartCaptured(setup, f.ctx, f.config, f.path, f.tunnel, f.target.UID)
	if err != nil {
		f.finishSetup(err)
		return
	}
	registered := make(chan error, 1)
	if f.app.IsRunning() {
		go f.app.QueueUpdateDraw(func() {
			if !f.current() || setup.Err() != nil {
				registered <- context.Canceled
				return
			}
			if _, exists := f.factory.ForwarderFor(f.forwarder.ID()); exists {
				registered <- fmt.Errorf("owned mapping already exists")
				return
			}
			f.forwarder.SetActive(true)
			f.factory.AddForwarder(f.forwarder)
			registered <- nil
		})
	}
	select {
	case err = <-registered:
	case <-setup.Done():
		err = setup.Err()
	}
	if err != nil {
		f.forwarder.Stop()
		f.finishSetup(err)
		return
	}
	f.launch.handle.Event("Endpoint and captured UID checked; opening local listener.")
	finished := make(chan struct{})
	go f.observeReady(stream, finished, cancelSetup)
	err = stream.ForwardPorts()
	close(finished)
	f.factory.DeleteOwnedForwarder(f.forwarder)
	if f.ctx.Err() != nil {
		f.launch.handle.Finish(session.Stopped, "Owned local forward stopped; listener cleanup completed.")
	} else if err != nil {
		f.launch.handle.Finish(session.Failed, "Local binding or remote stream failed; owned listener ended. No automatic retry.")
	} else {
		f.launch.handle.Finish(session.Stopped, "Owned local forward ended; listener cleanup completed.")
	}
}

func (f *ownedForward) observeReady(stream *portforward.PortForwarder, finished <-chan struct{}, cancelSetup context.CancelFunc) {
	select {
	case <-f.forwarder.Ready():
		cancelSetup()
		ports, err := stream.GetPorts()
		if err != nil {
			return
		}
		f.launch.running(f.binding(ports))
	case <-finished:
	case <-f.ctx.Done():
	}
}

func (f *ownedForward) finishSetup(err error) {
	if f.ctx.Err() != nil {
		f.launch.handle.Finish(session.Stopped, "Pending forward canceled before a local listener was started.")
	} else {
		f.launch.handle.Finish(session.Failed, localForwardSetupNote(err))
	}
}

func localForwardSetupNote(err error) string {
	switch {
	case errors.Is(err, dao.ErrForwardIdentityChanged):
		return "Captured resource was replaced; no listener started. Reopen the native resource before retrying."
	case errors.Is(err, errForwardNoCandidate):
		return "No running Pod endpoint found in the bounded controller lookup; open the native Pods list to choose an endpoint."
	case errors.Is(err, dao.ErrForwardPodNotRunning):
		return "Captured Pod is not running; no local listener started. Check its current lifecycle before retrying."
	case errors.Is(err, dao.ErrForwardEndpointDenied) || apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err):
		return "Endpoint access denied; no local listener started. Check access for the captured destination."
	case apierrors.IsNotFound(err):
		return "Captured Pod is absent; no local listener started. Reopen the resource list."
	case errors.Is(err, context.DeadlineExceeded):
		return "Bounded setup deadline reached; no local listener started. Inspect the captured destination before retrying."
	default:
		return "Connection or endpoint setup failed; no local listener started. No automatic retry."
	}
}

func (f *ownedForward) binding(ports []portforward.ForwardedPort) string {
	local, remote := f.tunnel.LocalPort, f.tunnel.ContainerPort
	if len(ports) > 0 {
		local, remote = fmt.Sprint(ports[0].Local), fmt.Sprint(ports[0].Remote)
	}
	return net.JoinHostPort(f.tunnel.Address, local) + " -> " + remote
}
