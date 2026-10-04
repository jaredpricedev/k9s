// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package view

import (
	"context"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/config"
	"github.com/derailed/k9s/internal/config/mock"
	"k8s.io/apimachinery/pkg/util/sets"
)

type capturedPluginRunner struct {
	app  *App
	path string
	env  Env
}

func (r *capturedPluginRunner) App() *App               { return r.app }
func (r *capturedPluginRunner) GetSelectedItem() string { return r.path }
func (*capturedPluginRunner) Aliases() sets.Set[string] { return sets.New[string]() }
func (r *capturedPluginRunner) EnvFn() EnvFunc          { return func() Env { return r.env } }

func TestGuardedPluginSnapshotsEnvironmentAndRejectsChangedSelectionOrReadOnly(t *testing.T) {
	r := &capturedPluginRunner{app: NewApp(mock.NewMockConfig(t)), path: guardedTestPluginPath, env: Env{nameCol: guardedTestOriginal}}
	confirm := false
	p := config.Plugin{Command: "missing-command", Args: []string{"$NAME"}, Pipes: []string{guardedTestPipeline}, Confirm: &confirm, Dangerous: true}
	inv, err := capturePluginInvocation(r, &p)
	if err != nil {
		t.Fatal(err)
	}
	r.env[nameCol] = "later"
	p.Args[0] = guardedTestChangedValue
	p.Pipes[0] = guardedTestChangedValue
	confirm = true
	if inv.env[nameCol] != guardedTestOriginal || inv.plugin.Args[0] != "$NAME" || inv.plugin.Pipes[0] != guardedTestPipeline || *inv.plugin.Confirm {
		t.Fatal("plugin review retained mutable input", inv)
	}
	if !inv.current() {
		t.Fatal("current destination rejected")
	}
	r.path = "ns/later"
	inv.execute(nil)
	if len(r.app.operations.list()) != 0 {
		t.Fatal("changed selection submitted command")
	}
	r.path = guardedTestPluginPath
	r.app.Config.K9s.ReadOnly = true
	if inv.current() {
		t.Fatal("new read-only mode accepted")
	}
	inv.execute(nil)
	if len(r.app.operations.list()) != 0 {
		t.Fatal("read-only command submitted")
	}
	if _, err := capturePluginInvocation(r, &p); err == nil {
		t.Fatal("dangerous plugin captured in read-only mode")
	}
}

func TestGuardedPluginCannotUseAClosedTerminal(t *testing.T) {
	app := NewApp(mock.NewMockConfig(t))
	task := startOperationBatch(time.Second, []SelectedResourceTarget{{Name: "plugin"}}, func(ctx context.Context, _ SelectedResourceTarget) error {
		return runGuardedInteractive(ctx, app, &shellOpts{}, make(chan string, 1), func() bool { return true })
	}, nil, nil)
	if got := waitOperationTask(t, task).Outcomes[0]; got.State != operationFailed {
		t.Fatal(got)
	}
}
