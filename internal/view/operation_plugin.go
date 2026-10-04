// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/session"
	"github.com/derailed/k9s/internal/ui/dialog"
	"k8s.io/client-go/rest"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

type pluginInvocation struct {
	runner            Runner
	plugin            config.Plugin
	env               Env
	path, contextName string
	revision          uint64
	tableGeneration   uint64
	target            SelectedResourceTarget
	session           *operationSession
	actorREST         func() (*rest.Config, error)
	namespace         string
	requiredVerbs     []string
	destination       *clientcmdapi.Config
	prepared          bool
	debugWrite        bool
}

func capturePluginInvocation(r Runner, p *config.Plugin) (*pluginInvocation, error) {
	effective := *p
	if len(pluginDebugCommands(effective.Command, effective.Args, effective.Pipes)) > 0 {
		effective.Dangerous = true
	}
	p = &effective
	if p.Dangerous && r.App().Config.IsReadOnly() {
		return nil, fmt.Errorf("plugin is unavailable in read-only mode")
	}
	if r.EnvFn() == nil {
		return nil, fmt.Errorf("plugin environment is unavailable")
	}
	pluginCopy := *p
	pluginCopy.Args, pluginCopy.Pipes = slices.Clone(p.Args), slices.Clone(p.Pipes)
	pluginCopy.Inputs = slices.Clone(p.Inputs)
	for index := range pluginCopy.Inputs {
		pluginCopy.Inputs[index].Options = slices.Clone(pluginCopy.Inputs[index].Options)
	}
	if p.Confirm != nil {
		confirm := *p.Confirm
		pluginCopy.Confirm = &confirm
	}
	inv := &pluginInvocation{
		runner: r, plugin: pluginCopy, env: maps.Clone(r.EnvFn()()), path: r.GetSelectedItem(),
		contextName: r.App().Config.ActiveContextName(), revision: r.App().Config.DestinationRevision(),
	}
	if owner, ok := r.(TableViewer); ok {
		inv.tableGeneration = owner.GetTable().operationGeneration.Load()
	}
	inv.target = SelectedResourceTarget{Context: inv.contextName, Name: inv.path}
	if connection := r.App().Conn(); connection != nil && connection.Config() != nil {
		inv.actorREST = connection.Config().RESTConfig
		inv.namespace = client.CleanseNamespace(r.App().Config.ActiveNamespace())
	}
	if v, ok := r.(ResourceViewer); ok {
		inv.target.GVR = v.GVR()
		selected := selectedResourceForPath(v, inv.contextName, inv.path)
		if p.Dangerous && selected.Err() != nil && resourceTargetForPath(v.GVR(), inv.contextName, inv.path).Err() == nil {
			return nil, selected.Err()
		}
		if selected.Err() == nil {
			inv.target = selected
			if p.Dangerous {
				if err := checkOperationTarget(&selected); err != nil {
					return nil, err
				}
				operation, err := captureOperation(v)
				if err != nil {
					return nil, err
				}
				inv.session = operation
			}
		}
	}
	return inv, nil
}

func (i *pluginInvocation) current() bool {
	a := i.runner.App()
	if a.Config.ActiveContextName() != i.contextName || a.Config.DestinationRevision() != i.revision ||
		i.runner.GetSelectedItem() != i.path || (i.plugin.Dangerous && a.Config.IsReadOnly()) {
		return false
	}
	if i.session != nil {
		return i.session.current()
	}
	if owner, ok := i.runner.(TableViewer); ok {
		currentOwner, currentOK := a.Content.Top().(TableViewer)
		// Resource decorators expose the same table as their embedded runner.
		// Comparing wrapper pointers would reject valid Pod/Service plugins.
		return currentOK && currentOwner.GetTable() == owner.GetTable() &&
			owner.GetTable().operationGeneration.Load() == i.tableGeneration
	}
	if owner, ok := i.runner.(Viewer); ok {
		return a.Content.Top() == owner
	}
	return true
}

func (i *pluginInvocation) resolveDestination() (*clientcmdapi.Config, error) {
	if !pluginUsesNativeDestination(i.plugin.Command, i.plugin.Pipes) {
		return nil, nil
	}
	if i.actorREST == nil {
		return nil, fmt.Errorf("plugin destination configuration is unavailable")
	}
	resolved, err := i.actorREST()
	if err != nil {
		return nil, err
	}
	return cliDestinationFromREST(resolved, i.contextName, i.namespace)
}

// Resolve local configuration before any input or confirmation dialog. The
// child receives this copied destination even if the source kubeconfig changes.
func (i *pluginInvocation) prepare(next func()) {
	if i.prepared {
		next()
		return
	}
	go func() {
		destination, err := i.resolveDestination()
		if !i.runner.App().IsRunning() {
			return
		}
		i.runner.App().QueueUpdateDraw(func() {
			if !i.runner.App().IsRunning() || !i.current() {
				return
			}
			if err != nil {
				i.runner.App().Flash().Err(err)
				return
			}
			i.destination, i.prepared = destination, true
			next()
		})
	}()
}

func (i *pluginInvocation) execute(values dialog.PluginInputValues) {
	if !i.current() {
		i.runner.App().Flash().Warn("Plugin destination, selection or read-only mode changed; reopen the action")
		return
	}
	if !i.prepared {
		i.prepare(func() { i.execute(values) })
		return
	}
	env := maps.Clone(i.env)
	for name, value := range values {
		env["INPUT_"+strings.ToUpper(name)] = value
	}
	args := make([]string, len(i.plugin.Args))
	for index, arg := range i.plugin.Args {
		value, err := env.Substitute(arg)
		if err != nil {
			i.runner.App().Flash().Err(err)
			return
		}
		args[index] = value
	}
	if len(pluginDebugCommands(i.plugin.Command, args, i.plugin.Pipes)) > 0 {
		if err := i.guardDebugWrite(); err != nil {
			i.runner.App().Flash().Err(err)
			return
		}
	}
	start := func() {
		if !i.current() {
			i.runner.App().Flash().Warn("Plugin destination, selection or read-only mode changed; reopen the action")
			return
		}
		task := newOperationTask(maxOperationDeadline, []SelectedResourceTarget{i.target})
		if err := i.runner.App().operations.add(task, "Plugin: "+i.plugin.Description, i.contextName); err != nil {
			task.cancel()
			i.runner.App().Flash().Err(err)
			return
		}
		i.runner.App().Flash().Info("Plugin started; :operations reviews or cancels remaining work")
		spec := localSessionSpec(session.Plugin, i.plugin.Description, &i.target, i.revision)
		if i.destination != nil {
			if entry := i.destination.Contexts[i.contextName]; entry != nil {
				if cluster := i.destination.Clusters[entry.Cluster]; cluster != nil {
					spec.Destination.Server = localSessionEndpoint(cluster.Server)
				}
			}
		}
		spec.OperationID = fmt.Sprintf("#%d", task.receipt().ID)
		spec.IdentityNote = "External plugin owns its explicit destinations and arguments; captured UID does not impose a CLI write precondition."
		handle, err := i.runner.App().localSessions.Add(spec, task.cancelRemaining)
		if err != nil {
			task.cancelRemaining()
			task.start(func(context.Context, SelectedResourceTarget) error { return context.Canceled }, nil, nil)
			i.runner.App().Flash().Err(err)
			return
		}
		launch := i.runner.App().ownLocalLaunch(handle, i.runner.App().Content.Top())
		task.start(func(ctx context.Context, target SelectedResourceTarget) error {
			if i.session != nil {
				verbs := i.requiredVerbs
				if len(verbs) == 0 {
					verbs = []string{client.GetVerb}
				}
				if err := i.session.authorize(ctx, &target, "", verbs...); err != nil {
					return err
				}
				if _, err := i.session.readTarget(ctx, &target); err != nil {
					return err
				}
				if i.debugWrite {
					if err := i.authorizeDebugWrite(ctx, &target, args); err != nil {
						return err
					}
				}
			}
			opts := shellOpts{binary: i.plugin.Command, background: i.plugin.Background, args: args, pipes: i.plugin.Pipes, ctx: ctx,
				onStart: func() { launch.running("") }}
			// Freeze the default kubeconfig for native CLIs. Explicit plugin
			// destination arguments remain owned by that configured plugin.
			if i.destination != nil {
				configPath, cleanup, err := writeCLIDestination(i.destination)
				if err != nil {
					return err
				}
				defer cleanup()
				opts.env = append(os.Environ(), "KUBECONFIG="+configPath)
			}
			statuses := make(chan string, 1)
			var err error
			if i.plugin.Background {
				err = execute(&opts, statuses)
			} else {
				err = runGuardedInteractive(ctx, i.runner.App(), &opts, statuses, i.current)
			}
			if err != nil {
				return err
			}
			fmt.Fprintln(operationOutput(ctx), "External command exited successfully. Remote effects and Kubernetes acceptance were not separately observed.")
			if i.plugin.OverwriteOutput {
				for status := range statuses {
					if strings.HasPrefix(status, outputPrefix) && i.runner.App().IsRunning() {
						text := strings.TrimSpace(strings.TrimPrefix(status, outputPrefix))
						go i.runner.App().QueueUpdateDraw(func() {
							if i.runner.App().IsRunning() && i.current() {
								i.runner.App().Flash().Info(text)
							}
						})
					}
				}
			}
			return nil
		}, nil, func(outcomes []operationOutcome) {
			if len(outcomes) == 1 {
				finishLocalCommand(handle, outcomes[0].Err)
			} else {
				handle.Finish(session.Unknown, "Local command completion was not fully reported; inspect the operation receipt.")
			}
		})
	}
	if i.plugin.Dangerous || i.plugin.ShouldConfirm() {
		identity := string(i.target.UID)
		if identity == "" {
			identity = "provider/input scope; no Kubernetes UID assertion"
		}
		msg := fmt.Sprintf("Run external action %s?\nContext: %s\nSelection: %s\nSelection identity: %s\nExternal plugin owns "+
			"its destinations; its writes do not receive Kubernetes UID preconditions from k9+. Completion is "+
			"not a Kubernetes outcome.\nCancellation does not undo accepted writes. :operations cancels "+
			"background work; Ctrl+C interrupts an interactive command. Arguments run directly, without shell "+
			"interpolation.", i.plugin.Description, i.contextName, i.path, identity)
		d := i.runner.App().Styles.Dialog()
		dialog.ShowConfirm(&d, i.runner.App().Content.Pages, "Confirm external action", msg, start, func() {})
		return
	}
	start()
}

// Lifecycle and framework suspension stay on the event loop. The child does
// its IO in a worker while Suspend owns the terminal; closing the app or
// canceling the operation releases the operation worker even if a queued
// callback cannot run. A late callback checks cancellation before handoff.
func runGuardedInteractive(ctx context.Context, app *App, opts *shellOpts, statuses chan<- string, current func() bool) error {
	return runInteractiveHandoff(ctx, app, current, func() error {
		opts.terminalOwned = true
		return execute(opts, statuses)
	})
}

// A cancellation decision and terminal handoff must share one boundary. Before
// entry, cancellation makes a queued callback ineligible. After entry, callers
// retain their owned files and receipts until child and terminal cleanup finish.
type interactiveHandoff struct {
	mu                sync.Mutex
	entered, canceled bool
}

func (h *interactiveHandoff) enter(ctx context.Context) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.canceled || ctx.Err() != nil {
		return false
	}
	h.entered = true
	return true
}

func (h *interactiveHandoff) cancelBeforeEntry() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.entered {
		return false
	}
	h.canceled = true
	return true
}

func runInteractiveHandoff(ctx context.Context, app *App, current func() bool, work func() error) error {
	if !app.IsRunning() {
		return fmt.Errorf("terminal handoff is unavailable")
	}
	result := make(chan error, 1)
	handoff := new(interactiveHandoff)
	go app.QueueUpdateDraw(func() {
		if !app.IsRunning() {
			result <- fmt.Errorf("terminal handoff is unavailable")
			return
		}
		if err := ctx.Err(); err != nil {
			result <- err
			return
		}
		if !current() {
			result <- fmt.Errorf("plugin destination, selection or read-only mode changed before terminal handoff")
			return
		}
		releaseTerminal, acquireErr := acquireCommandTerminal(ctx)
		if acquireErr != nil {
			result <- acquireErr
			return
		}
		defer releaseTerminal()
		if !current() {
			result <- fmt.Errorf("plugin destination changed while waiting for terminal ownership")
			return
		}
		if !handoff.enter(ctx) {
			result <- ctx.Err()
			return
		}
		app.Halt()
		var commandErr error
		if !app.Suspend(func() {
			completed := make(chan error, 1)
			go func() { completed <- work() }()
			commandErr = <-completed
		}) {
			commandErr = fmt.Errorf("terminal handoff is unavailable")
		}
		if app.IsRunning() {
			app.Resume()
		}
		result <- commandErr
	})
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		if handoff.cancelBeforeEntry() {
			return ctx.Err()
		}
		// execute enforces its bounded deadline and child WaitDelay. The
		// result is sent only after Suspend has restored framework ownership.
		return <-result
	}
}
