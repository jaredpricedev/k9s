// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
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
	target            SelectedResourceTarget
	session           *operationSession
	actorREST         func() (*rest.Config, error)
	namespace         string
	requiredVerbs     []string
	destination       *clientcmdapi.Config
	prepared          bool
}

func capturePluginInvocation(r Runner, p *config.Plugin) (*pluginInvocation, error) {
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
				session, err := captureOperation(v)
				if err != nil {
					return nil, err
				}
				inv.session = session
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
	if owner, ok := i.runner.(Viewer); ok {
		return a.Content.Top() == owner
	}
	return true
}

func (i *pluginInvocation) resolveDestination() (*clientcmdapi.Config, error) {
	binary := filepath.Base(i.plugin.Command)
	if binary != nativeKubectlCommand && binary != "helm" && binary != "kubectl.exe" && binary != "helm.exe" {
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
			}
			opts := shellOpts{binary: i.plugin.Command, background: i.plugin.Background, args: args, pipes: i.plugin.Pipes, ctx: ctx}
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
		}, nil, nil)
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
	if !app.IsRunning() {
		return fmt.Errorf("terminal handoff is unavailable")
	}
	result := make(chan error, 1)
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
		opts.terminalOwned = true
		app.Halt()
		defer func() {
			if app.IsRunning() {
				app.Resume()
			}
		}()
		var commandErr error
		if !app.Suspend(func() {
			completed := make(chan error, 1)
			go func() { completed <- execute(opts, statuses) }()
			commandErr = <-completed
		}) {
			commandErr = fmt.Errorf("terminal handoff is unavailable")
		}
		result <- commandErr
	})
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
