// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/session"
	"github.com/derailed/k9s/internal/ui/dialog"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type containerSessionLaunch struct {
	app                 *App
	owner               model.Component
	target              SelectedResourceTarget
	revision            uint64
	selection           string
	container, platform string
	kind                session.Kind
	config              *rest.Config
}

func launchContainerSession(app *App, owner model.Component, path, container string, kind session.Kind) error {
	if app.Config.IsReadOnly() {
		return fmt.Errorf("container shell and attach are unavailable in read-only mode")
	}
	if app.factory == nil || app.Conn() == nil {
		return fmt.Errorf("container session connection is unavailable")
	}
	target := resourceTargetForPath(client.PodGVR, app.Config.ActiveContextName(), path)
	if err := target.Err(); err != nil {
		return err
	}
	pod, err := cachedLocalSessionPod(app.factory, app.Config.CachedNamespace(), target.Path())
	if err != nil {
		return err
	}
	target.UID = pod.UID
	if identityErr := checkOperationTarget(&target); identityErr != nil {
		return identityErr
	}
	actor, err := app.Conn().RestConfig()
	if err != nil {
		return err
	}
	launch := &containerSessionLaunch{app: app, owner: owner, target: target, revision: app.Config.DestinationRevision(), container: container,
		kind: kind, config: rest.CopyConfig(actor), platform: "linux"}
	if runner, ok := owner.(Runner); ok {
		launch.selection = runner.GetSelectedItem()
	}
	if platform, ok := osFromSelector(pod.Spec.NodeSelector); ok {
		launch.platform = platform
	} else if object, cacheErr := app.factory.CachedGet(client.NodeGVR, "", pod.Spec.NodeName); cacheErr == nil {
		if metadata, accessorErr := meta.Accessor(object); accessorErr == nil {
			if platform, ok := osFromSelector(metadata.GetLabels()); ok {
				launch.platform = platform
			}
		}
	}
	if container != "" {
		launch.start()
		return nil
	}
	if preferred, ok := dao.GetDefaultContainer(&pod.ObjectMeta, &pod.Spec); ok {
		launch.container = preferred
		launch.start()
		return nil
	}
	containers := fetchContainers(&pod.ObjectMeta, &pod.Spec, false)
	if len(containers) == 0 {
		return fmt.Errorf("selected Pod has no available containers")
	}
	if len(containers) == 1 {
		launch.container = containers[0]
		launch.start()
		return nil
	}
	picker := NewPicker()
	picker.populate(containers)
	picker.SetSelectedFunc(func(_ int, selected, _ string, _ rune) {
		if app.Content.Top() != picker || app.Config.DestinationRevision() != launch.revision {
			return
		}
		app.Content.Pop()
		launch.container = selected
		launch.start()
	})
	return app.inject(picker, false)
}

func (l *containerSessionLaunch) current() bool {
	if !l.app.IsRunning() || l.app.Config.IsReadOnly() || l.app.Config.ActiveContextName() != l.target.Context ||
		l.app.Config.DestinationRevision() != l.revision || l.app.Content.Top() != l.owner {
		return false
	}
	if runner, ok := l.owner.(Runner); ok {
		return runner.GetSelectedItem() == l.selection
	}
	return true
}

func (l *containerSessionLaunch) start() {
	if !l.current() {
		l.app.Flash().Warn("Container session destination or selection changed; reopen the launch action")
		return
	}
	start := func() { l.submit() }
	if l.kind == session.Attach {
		styles := l.app.Styles.Dialog()
		dialog.ShowConfirm(&styles, l.app.Content.Pages, "Attach to captured container",
			fmt.Sprintf("Context: %s\nPod: %s\nUID: %s\nContainer: %s\nAttach connects to the existing process. "+
				"Ctrl+C may interrupt that remote process. Local cancellation does not undo remote effects.",
				l.target.Context, l.target.Path(), l.target.UID, l.container), start, func() {})
		return
	}
	start()
}

func (l *containerSessionLaunch) submit() {
	if !l.current() {
		l.app.Flash().Warn("Container session destination or selection changed; reopen the launch action")
		return
	}
	task := newOperationTask(maxOperationDeadline, []SelectedResourceTarget{l.target})
	if err := l.app.operations.add(task, "Container "+string(l.kind), l.target.Context); err != nil {
		task.cancel()
		l.app.Flash().Err(err)
		return
	}
	spec := localSessionSpec(l.kind, "Container "+string(l.kind), &l.target, l.revision)
	spec.Destination.Server = localSessionEndpoint(l.config.Host)
	spec.Destination.Container = l.container
	spec.OperationID = fmt.Sprintf("#%d", task.receipt().ID)
	spec.IdentityNote = "Pod UID and endpoint permission are checked before terminal handoff. " +
		"Native exec/attach does not provide an atomic UID precondition; remote effects may outlive local cancellation."
	handle, err := l.app.localSessions.Add(spec, task.cancelRemaining)
	if err != nil {
		task.cancelRemaining()
		task.start(func(context.Context, SelectedResourceTarget) error { return context.Canceled }, nil, nil)
		l.app.Flash().Err(err)
		return
	}
	launch := l.app.ownLocalLaunch(handle, l.owner)
	task.start(func(ctx context.Context, _ SelectedResourceTarget) error { return l.execute(ctx, launch) }, nil, func(outcomes []operationOutcome) {
		if len(outcomes) == 1 {
			finishLocalCommand(handle, outcomes[0].Err)
		} else {
			handle.Finish(session.Unknown, "Container command completion was not fully reported; inspect its operation receipt.")
		}
	})
	l.app.Flash().Info("Container session setup started; :sessions retains its captured identity and lifecycle")
}

func (l *containerSessionLaunch) execute(ctx context.Context, launch *localLaunch) error {
	setup, cancel := context.WithTimeout(ctx, localSessionSetupTimeout)
	defer cancel()
	clientset, err := kubernetes.NewForConfig(l.config)
	if err != nil {
		return err
	}
	operation := &operationSession{typed: clientset}
	if preflightErr := operation.authorize(setup, &l.target, "", client.GetVerb); preflightErr != nil {
		return preflightErr
	}
	pod, err := clientset.CoreV1().Pods(l.target.Namespace).Get(setup, l.target.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if identityErr := verifySelectedIdentity(l.target, pod); identityErr != nil {
		return identityErr
	}
	if pod.Status.Phase != v1.PodRunning {
		return fmt.Errorf("captured Pod is not running")
	}
	if _, err = locateContainer(l.container, pod.Spec.Containers); err != nil {
		if !hasSessionContainer(pod, l.container) {
			return fmt.Errorf("captured container is absent from the Pod")
		}
	}
	subresource := "exec"
	if l.kind == session.Attach {
		subresource = "attach"
	}
	if preflightErr := operation.authorize(setup, &l.target, subresource, client.CreateVerb); preflightErr != nil {
		return preflightErr
	}
	raw, err := cliDestinationFromREST(l.config, l.target.Context, l.target.Namespace)
	if err != nil {
		return err
	}
	configPath, cleanup, err := writeCLIDestination(raw)
	if err != nil {
		return err
	}
	defer cleanup()
	binary, err := exec.LookPath(nativeKubectlCommand)
	if err != nil || errors.Is(err, exec.ErrDot) {
		return fmt.Errorf("kubectl executable is unavailable in the configured PATH")
	}
	args := computeShellArgs(l.target.Path(), l.container, nil, l.platform)
	if l.kind == session.Attach {
		args = buildShellArgs(subresource, l.target.Path(), l.container, nil)
	}
	opts := shellOpts{binary: binary, args: args, ctx: ctx, clear: true, env: append(os.Environ(), "KUBECONFIG="+configPath),
		banner: fmt.Sprintf(bannerFmt, l.target.Path(), l.container), onStart: func() { launch.running("") }}
	cancel()
	statuses := make(chan string, 1)
	return runGuardedInteractive(ctx, l.app, &opts, statuses, l.current)
}

func hasSessionContainer(pod *v1.Pod, name string) bool {
	for i := range pod.Spec.InitContainers {
		if pod.Spec.InitContainers[i].Name == name {
			return true
		}
	}
	for i := range pod.Spec.EphemeralContainers {
		if pod.Spec.EphemeralContainers[i].Name == name {
			return true
		}
	}
	return false
}
