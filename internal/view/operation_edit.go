// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"fmt"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/config"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/util/sets"
)

const (
	nativeEditCommand    = "edit"
	nativeKubectlCommand = "kubectl"
)

// Native text and aggregate views do not have a native resource table. Capture
// their owner and selection explicitly, then use the same guarded CLI worker.
type nativeEditRunner struct {
	app  *App
	path string
}

func (r *nativeEditRunner) App() *App               { return r.app }
func (r *nativeEditRunner) GetSelectedItem() string { return r.path }
func (*nativeEditRunner) Aliases() sets.Set[string] { return sets.New[string]() }
func (*nativeEditRunner) EnvFn() EnvFunc            { return func() Env { return Env{} } }

func editRes(app *App, gvr *client.GVR, path string) error {
	if app.Config.IsReadOnly() {
		return fmt.Errorf("editing is unavailable in read-only mode")
	}
	if app.factory == nil || app.Conn() == nil {
		return fmt.Errorf("edit operation clients are unavailable")
	}
	target := resourceTargetForPath(gvr, app.Config.ActiveContextName(), path)
	if err := target.Err(); err != nil {
		return err
	}
	obj, err := app.factory.CachedGet(gvr, target.Namespace, target.Path())
	if err != nil {
		return fmt.Errorf("edit identity is not retained; reopen the native resource list")
	}
	metadata, err := meta.Accessor(obj)
	if err != nil {
		return err
	}
	target.UID = metadata.GetUID()
	if targetErr := checkOperationTarget(&target); targetErr != nil {
		return targetErr
	}
	pinned, err := pinInspectionConnection(app.Conn())
	if err != nil {
		return err
	}
	owner := app.Content.Top()
	originalSelection := ""
	if runner, ok := owner.(Runner); ok {
		originalSelection = runner.GetSelectedItem()
	}
	session := &operationSession{app: app, context: target.Context, revision: app.Config.DestinationRevision()}
	session.dynamic, _ = pinned.DynDial()
	session.typed, _ = pinned.Dial()
	if session.dynamic == nil || session.typed == nil {
		return fmt.Errorf("edit operation clients are unavailable")
	}
	session.stillCurrent = func() bool {
		if app.Content.Top() != owner {
			return false
		}
		if runner, ok := owner.(Runner); ok {
			return runner.GetSelectedItem() == originalSelection
		}
		return true
	}
	args := []string{nativeEditCommand, gvr.FQN(target.Name)}
	if target.Namespace != "" {
		args = append(args, "-n", target.Namespace)
	}
	runner := &nativeEditRunner{app: app, path: path}
	plugin := &config.Plugin{Command: nativeKubectlCommand, Args: args, Description: "Edit " + gvr.R(), Dangerous: true}
	invocation, err := capturePluginInvocation(runner, plugin)
	if err != nil {
		return err
	}
	invocation.session, invocation.target = session, target
	invocation.requiredVerbs = []string{client.GetVerb, client.PatchVerb}
	invocation.execute(nil)
	return nil
}
