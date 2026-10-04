// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/model"
	"github.com/derailed/k9s/internal/session"
	"github.com/derailed/k9s/internal/ui/dialog"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type nodeSessionLaunch struct {
	app      *App
	owner    model.Component
	target   SelectedResourceTarget
	revision uint64
	config   *rest.Config
	pod      *v1.Pod
	platform string
}

func launchOwnedNodeShell(app *App, name string) error {
	if app.Config.IsReadOnly() {
		return fmt.Errorf("node debug shell creates a privileged Pod and is unavailable in read-only mode")
	}
	if app.Config.K9s.ShellPod == nil || app.factory == nil || app.Conn() == nil {
		return fmt.Errorf("node shell configuration or connection is unavailable")
	}
	target := SelectedResourceTarget{Context: app.Config.ActiveContextName(), GVR: client.NodeGVR, Name: name}
	object, err := app.factory.CachedGet(client.NodeGVR, "", target.Path())
	if err != nil {
		return fmt.Errorf("Node identity is not retained; reopen the native Node list")
	}
	metadata, err := meta.Accessor(object)
	if err != nil {
		return err
	}
	target.UID = metadata.GetUID()
	if identityErr := checkOperationTarget(&target); identityErr != nil {
		return identityErr
	}
	actor, err := app.Conn().RestConfig()
	if err != nil {
		return err
	}
	pod := k9sShellPod(name, app.Config.K9s.ShellPod).DeepCopy()
	if pod.Namespace == "" {
		pod.Namespace = client.DefaultNamespace
	}
	pod.GenerateName, pod.Name = pod.Name+"-", ""
	launch := &nodeSessionLaunch{app: app, owner: app.Content.Top(), target: target, revision: app.Config.DestinationRevision(),
		config: rest.CopyConfig(actor), pod: pod, platform: "linux"}
	if platform, ok := osFromSelector(metadata.GetLabels()); ok {
		launch.platform = platform
	}
	styles := app.Styles.Dialog()
	message := fmt.Sprintf("Context: %s\nNode: %s\nUID: %s\nCreates a privileged debug Pod in %s with host access. "+
		"k9+ owns only the returned Pod UID and attempts bounded cleanup of that Pod. Cancellation does not erase accepted creation; "+
		"receipts retain create and cleanup facts. Existing debug Pods are not removed.", target.Context, target.Name, target.UID, pod.Namespace)
	dialog.ShowConfirm(&styles, app.Content.Pages, "Create owned node debug session", message, launch.submit, func() {})
	return nil
}

func (l *nodeSessionLaunch) current() bool {
	if !l.app.IsRunning() || l.app.Config.IsReadOnly() || l.app.Config.DestinationRevision() != l.revision || l.app.Content.Top() != l.owner {
		return false
	}
	if runner, ok := l.owner.(Runner); ok {
		return runner.GetSelectedItem() == l.target.Name
	}
	return true
}

func (l *nodeSessionLaunch) submit() {
	if !l.current() {
		l.app.Flash().Warn("Node shell destination or selection changed; reopen the action")
		return
	}
	task := newOperationTask(maxOperationDeadline, []SelectedResourceTarget{l.target})
	if err := l.app.operations.add(task, "Owned node debug shell", l.target.Context); err != nil {
		task.cancel()
		l.app.Flash().Err(err)
		return
	}
	spec := localSessionSpec(session.Diagnostic, "Node debug shell", &l.target, l.revision)
	spec.Destination.Server = localSessionEndpoint(l.config.Host)
	spec.Destination.Container = k9sShell
	spec.OperationID = fmt.Sprintf("#%d", task.receipt().ID)
	spec.IdentityNote = "Explicit privileged Pod creation; cleanup is pinned to the returned Pod UID and captured client. Accepted writes remain in the operation receipt."
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
			handle.Finish(session.Unknown, "Node session completion was not fully reported; inspect its receipt.")
		}
	})
	l.app.Flash().Info("Owned node debug setup started; :sessions and :operations retain acceptance and cleanup")
}

func (l *nodeSessionLaunch) execute(ctx context.Context, launch *localLaunch) (result error) {
	clientset, err := kubernetes.NewForConfig(l.config)
	if err != nil {
		return err
	}
	if preflightErr := l.preflight(ctx, clientset); preflightErr != nil {
		return preflightErr
	}
	operationBeginWrite(ctx)
	createCtx, cancelCreate := context.WithTimeout(ctx, localSessionSetupTimeout)
	created, err := clientset.CoreV1().Pods(l.pod.Namespace).Create(createCtx, l.pod.DeepCopy(), metav1.CreateOptions{})
	cancelCreate()
	if err != nil {
		launch.handle.Event("Debug Pod creation was attempted; acceptance was not observed. Inspect the captured namespace before retrying.")
		return err
	}
	if created.UID == "" || created.Name == "" {
		return errors.Join(fmt.Errorf("created debug Pod identity was unavailable"), errExternalOperationOutcome)
	}
	operationAcceptWrite(ctx, "Debug Pod created: "+client.FQN(created.Namespace, created.Name)+" UID "+string(created.UID))
	launch.handle.Event("Owned debug Pod accepted: " + client.FQN(created.Namespace, created.Name) + " UID " + string(created.UID))
	defer func() { result = errors.Join(result, cleanupOwnedDebugPod(ctx, clientset, created, launch.handle)) }()
	if err := waitForOwnedDebugPod(ctx, clientset, created); err != nil {
		return err
	}
	return l.runCommand(ctx, created, launch)
}

func (l *nodeSessionLaunch) preflight(ctx context.Context, clientset kubernetes.Interface) error {
	setup, cancel := context.WithTimeout(ctx, localSessionSetupTimeout)
	defer cancel()
	operation := &operationSession{typed: clientset}
	if preflightErr := operation.authorize(setup, &l.target, "", client.GetVerb); preflightErr != nil {
		return preflightErr
	}
	node, err := clientset.CoreV1().Nodes().Get(setup, l.target.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err := verifySelectedIdentity(l.target, node); err != nil {
		return err
	}
	podTarget := SelectedResourceTarget{Context: l.target.Context, GVR: client.PodGVR, Namespace: l.pod.Namespace}
	return operation.authorize(setup, &podTarget, "", client.GetVerb, client.CreateVerb, client.DeleteVerb)
}

func waitForOwnedDebugPod(ctx context.Context, clientset kubernetes.Interface, pod *v1.Pod) error {
	return wait.PollUntilContextTimeout(ctx, time.Second, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		current, err := clientset.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if current.UID != pod.UID {
			return false, fmt.Errorf("owned debug Pod was replaced; replacement will not be used")
		}
		return current.Status.Phase == v1.PodRunning, nil
	})
}

func (l *nodeSessionLaunch) runCommand(ctx context.Context, pod *v1.Pod, launch *localLaunch) error {
	operation := &operationSession{}
	clientset, err := kubernetes.NewForConfig(l.config)
	if err != nil {
		return err
	}
	operation.typed = clientset
	target := SelectedResourceTarget{Context: l.target.Context, GVR: client.PodGVR, Namespace: pod.Namespace, Name: pod.Name, UID: pod.UID}
	setup, cancel := context.WithTimeout(ctx, localSessionSetupTimeout)
	defer cancel()
	if preflightErr := operation.authorize(setup, &target, "exec", client.CreateVerb); preflightErr != nil {
		return preflightErr
	}
	raw, err := cliDestinationFromREST(l.config, l.target.Context, pod.Namespace)
	if err != nil {
		return err
	}
	path, cleanup, err := writeCLIDestination(raw)
	if err != nil {
		return err
	}
	defer cleanup()
	binary, err := exec.LookPath(nativeKubectlCommand)
	if err != nil {
		return fmt.Errorf("kubectl executable is unavailable")
	}
	args := computeShellArgs(target.Path(), k9sShell, nil, l.platform)
	container := &l.pod.Spec.Containers[0]
	if len(container.Command) > 0 {
		args = append(buildShellArgs("exec", target.Path(), k9sShell, nil), "--")
		args = append(args, container.Command...)
		args = append(args, container.Args...)
	}
	opts := shellOpts{binary: binary, args: args, ctx: ctx, clear: true, env: append(os.Environ(), "KUBECONFIG="+path),
		banner: fmt.Sprintf(bannerFmt, target.Path(), k9sShell), onStart: func() { launch.running("") }}
	cancel()
	return runGuardedInteractive(ctx, l.app, &opts, make(chan string, 1), l.current)
}

func cleanupOwnedDebugPod(operationCtx context.Context, clientset kubernetes.Interface, pod *v1.Pod, handle *session.Handle) error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	current, err := clientset.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
	if localSessionNamedPodMissing(err, pod) {
		handle.Event("Owned debug Pod is already absent; no delete was sent.")
		return nil
	}
	if err != nil {
		handle.Event("Owned debug Pod cleanup could not be checked; inspect its captured UID in the receipt.")
		return errors.Join(err, errExternalOperationOutcome)
	}
	if current.UID != pod.UID {
		handle.Event("Debug Pod name now belongs to a different UID; replacement was retained.")
		return nil
	}
	uid := pod.UID
	err = clientset.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	if localSessionNamedPodMissing(err, pod) {
		return nil
	}
	if err != nil {
		handle.Event("Owned debug Pod removal was not confirmed; inspect its captured UID before manual cleanup.")
		return errors.Join(err, errExternalOperationOutcome)
	}
	operationAcceptWrite(operationCtx, "Owned debug Pod deletion accepted for UID "+string(uid)+"; final disappearance was not separately observed")
	handle.Event("Owned debug Pod deletion accepted using a UID precondition; final disappearance was not separately observed.")
	return nil
}

// A proxy or unsupported route 404 does not establish absence of this Pod.
func localSessionNamedPodMissing(err error, pod *v1.Pod) bool {
	if err == nil || apierrors.IsUnexpectedServerError(err) {
		return false
	}
	var status apierrors.APIStatus
	if !errors.As(err, &status) {
		return false
	}
	value := status.Status()
	return value.Reason == metav1.StatusReasonNotFound && value.Code == 404 && value.Details != nil &&
		value.Details.Name == pod.Name && value.Details.Group == "" && value.Details.Kind == "pods"
}
