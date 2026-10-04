// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s
// Modified for k9+; see NOTICE.

package view

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/dao"
	"github.com/derailed/k9s/internal/watch"
	"github.com/derailed/tview"
	authv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
)

// operationSession freezes the destination before confirmation. Its generation
// belongs to the originating table, so results cannot paint a replacement view.
// Writes keep running after navigation; leaving a screen does not undo a request.
type operationSession struct {
	app        *App
	table      *Table
	context    string
	namespace  string
	generation uint64
	timeout    time.Duration
	dynamic    dynamic.Interface
	typed      kubernetes.Interface
}

const maxOperationTargets = 100

type operationOutcome struct {
	Target       SelectedResourceTarget
	Err          error
	NotSubmitted bool
}

func captureOperationScreen(v ResourceViewer) (*operationSession, error) {
	if v == nil || v.App() == nil || v.App().Config == nil || v.GetTable() == nil {
		return nil, errors.New("open a resource list before starting an operation")
	}
	app := v.App()
	if app.Config.IsReadOnly() {
		return nil, errors.New("operations are unavailable in read-only mode")
	}
	timeout := 10 * time.Second
	if app.Conn() != nil {
		timeout = app.Conn().Config().CallTimeout()
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	t := v.GetTable()
	return &operationSession{app: app, table: t, context: app.Config.ActiveContextName(),
		namespace: t.GetModel().GetNamespace(), generation: t.operationGeneration.Load(), timeout: timeout}, nil
}

func captureOperation(v ResourceViewer) (*operationSession, error) {
	s, err := captureOperationScreen(v)
	if err != nil {
		return nil, err
	}
	if v.App().Conn() == nil {
		return nil, errors.New("operation clients are unavailable")
	}
	pinned, err := pinInspectionConnection(v.App().Conn())
	if err != nil {
		return nil, err
	}
	s.dynamic, _ = pinned.DynDial()
	s.typed, _ = pinned.Dial()
	if s.dynamic == nil || s.typed == nil {
		return nil, errors.New("operation clients are unavailable")
	}
	return s, nil
}

func (s *operationSession) current() bool {
	if s.app.Config.ActiveContextName() != s.context || s.table.operationGeneration.Load() != s.generation ||
		s.table.GetModel().GetNamespace() != s.namespace {
		return false
	}
	v, ok := s.app.Content.Top().(TableViewer)
	return ok && v.GetTable() == s.table
}

func (s *operationSession) confirm() bool {
	if s.app.Config.IsReadOnly() || !s.current() {
		s.app.Flash().Warn("Destination or read-only mode changed; confirm the action again")
		return false
	}
	return true
}

func (s *operationSession) dispatch(fn func()) {
	if !s.app.IsRunning() {
		return
	}
	s.app.QueueUpdateDraw(func() {
		if s.app.IsRunning() && s.current() {
			fn()
		}
	})
}

func captureOperationTargets(v ResourceViewer, contextName string, paths []string) ([]SelectedResourceTarget, error) {
	if len(paths) > maxOperationTargets {
		return nil, fmt.Errorf("select at most %d resources per operation", maxOperationTargets)
	}
	targets := make([]SelectedResourceTarget, 0, len(paths))
	for _, path := range paths {
		target := selectedResourceForPath(v, contextName, path)
		if target.UnavailableReason == "" && target.UID == "" {
			target.UnavailableReason = fmt.Sprintf("identity unavailable for %s; refresh and confirm again", path)
		}
		targets = append(targets, target)
	}
	return targets, nil
}

func operationDestination(targets []SelectedResourceTarget) string {
	var b strings.Builder
	for i, target := range targets {
		if i > 0 {
			b.WriteByte('\n')
		}
		uid := string(target.UID)
		if uid == "" {
			uid = "unknown; this target will not be submitted"
		}
		fmt.Fprintf(&b, "%s %s (UID %s)", operationResourceName(&target), target.Path(), uid)
	}
	return b.String()
}

// startOperationBatch returns immediately. Targets share a bounded batch deadline;
// every target receives an outcome even when its request cannot start.
func startOperationBatch(timeout time.Duration, targets []SelectedResourceTarget,
	work func(context.Context, SelectedResourceTarget) error,
	publish func(operationOutcome), done func([]operationOutcome),
) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	targets = append([]SelectedResourceTarget(nil), targets...)
	go func() {
		batchCtx, batchCancel := context.WithTimeout(context.Background(), timeout)
		defer batchCancel()
		outcomes := make([]operationOutcome, 0, len(targets))
		for _, target := range targets {
			ctx, cancel := context.WithTimeout(batchCtx, timeout)
			err := ctx.Err()
			notSubmitted := err != nil
			if err == nil && target.UnavailableReason != "" {
				err, notSubmitted = target.Err(), true
			}
			if err == nil {
				err = work(ctx, target)
			}
			cancel()
			outcome := operationOutcome{Target: target, Err: err, NotSubmitted: notSubmitted}
			outcomes = append(outcomes, outcome)
			if publish != nil {
				publish(outcome)
			}
		}
		if done != nil {
			done(outcomes)
		}
	}()
}

func (s *operationSession) submit(action string, targets []SelectedResourceTarget,
	work func(context.Context, SelectedResourceTarget) error, accepted func(SelectedResourceTarget),
) {
	if !s.confirm() {
		return
	}
	s.app.Flash().Infof("Submitting %s for %d resource(s) in context %s...", action, len(targets), s.context)
	startOperationBatch(s.timeout, targets, work, nil, func(outcomes []operationOutcome) {
		s.dispatch(func() {
			if accepted != nil {
				for _, outcome := range outcomes {
					if outcome.Err == nil {
						accepted(outcome.Target)
					}
				}
			}
			s.showResults(action, outcomes)
		})
	})
}

func (s *operationSession) showResults(action string, outcomes []operationOutcome) {
	var b strings.Builder
	fmt.Fprintf(&b, "Context: %s\nOperation: %s\n\n", s.context, action)
	var accepted, notSubmitted int
	for _, outcome := range outcomes {
		if outcome.Err == nil {
			accepted++
			fmt.Fprintf(&b, "ACCEPTED  %s %s\n", operationResourceName(&outcome.Target), outcome.Target.Path())
		} else if outcome.NotSubmitted {
			notSubmitted++
			fmt.Fprintf(&b, "NOT SUBMITTED  %s %s: %s\n", operationResourceName(&outcome.Target), outcome.Target.Path(), operationOutcomeError(&outcome))
		} else {
			fmt.Fprintf(&b, "FAILED    %s %s: %s\n", operationResourceName(&outcome.Target), outcome.Target.Path(), operationOutcomeError(&outcome))
		}
	}
	b.WriteString("\n" +
		"Acceptance confirms the API request, not controller completion.\n" +
		"Watch READY / STATUS and events for the resulting state.\n" +
		"Leaving this view does not undo a submitted request.\n")
	if len(outcomes) == 1 {
		if outcomes[0].Err != nil {
			s.app.Flash().Errf("%s %s: %s", action, outcomes[0].Target.Path(), operationOutcomeError(&outcomes[0]))
		} else {
			s.app.Flash().Infof("%s accepted for %s; watch READY / STATUS for completion", action, outcomes[0].Target.Path())
		}
		return
	}
	s.app.Flash().Infof("%s: %d accepted, %d failed, %d not submitted", action, accepted, len(outcomes)-accepted-notSubmitted, notSubmitted)
	d := NewDetails(s.app, "Operation results", action, contentTXT, false).Update(tview.Escape(b.String()))
	if err := s.app.inject(d, false); err != nil {
		s.app.Flash().Err(err)
	}
}

func operationOutcomeError(outcome *operationOutcome) string {
	if outcome.NotSubmitted && errors.Is(outcome.Err, context.DeadlineExceeded) {
		return "batch deadline ended before this request started"
	}
	return operationError(outcome.Err)
}

func operationResourceName(target *SelectedResourceTarget) string {
	if target.GVR == nil {
		return "resource"
	}
	return target.GVR.R()
}

func operationError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "request wait ended; server acceptance may be unknown. Inspect state before retrying"
	}
	return err.Error()
}

func (s *operationSession) resource(target *SelectedResourceTarget) dynamic.ResourceInterface {
	r := s.dynamic.Resource(target.GVR.GVR())
	if target.Namespace == "" || client.IsClusterScoped(target.Namespace) {
		return r
	}
	return r.Namespace(target.Namespace)
}

// Use the captured typed client for RBAC reviews, not APIClient.CanI: that
// mutable connection may already refer to a different context.
func (s *operationSession) authorize(ctx context.Context, target *SelectedResourceTarget, subresource string, verbs ...string) error {
	for _, verb := range verbs {
		r, err := s.typed.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authv1.SelfSubjectAccessReview{
			Spec: authv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authv1.ResourceAttributes{
				Namespace: client.CleanseNamespace(target.Namespace), Group: target.GVR.G(), Version: target.GVR.V(),
				Resource: target.GVR.R(), Subresource: subresource, Name: target.Name, Verb: verb,
			}},
		}, metav1.CreateOptions{})
		if err != nil {
			return err
		}
		if r == nil || !r.Status.Allowed {
			return fmt.Errorf("%s access denied for %s %s", verb, target.GVR.R(), target.Path())
		}
	}
	return nil
}

func checkOperationTarget(target *SelectedResourceTarget) error {
	if err := target.Err(); err != nil {
		return err
	}
	if target.UID == "" {
		return fmt.Errorf("identity unavailable for %s; refresh and confirm again", target.Path())
	}
	return nil
}

// Retry only an explicit server rejection caused by optimistic concurrency.
// Each attempt re-reads the object and validates its original UID. An ambiguous
// timeout or transport error is never retried, since the write may be accepted.
func retryOperationConflict(ctx context.Context, work func() error) error {
	var conflict error
	err := wait.ExponentialBackoffWithContext(ctx, retry.DefaultBackoff, func(context.Context) (bool, error) {
		err := work()
		if apierrors.IsConflict(err) {
			conflict = err
			return false, nil
		}
		return true, err
	})
	if errors.Is(err, wait.ErrWaitTimeout) && conflict != nil {
		return conflict
	}
	return err
}

func (s *operationSession) readTarget(ctx context.Context, target *SelectedResourceTarget) (*unstructured.Unstructured, error) {
	if target.UID == "" {
		return nil, fmt.Errorf("identity unavailable for %s; refresh and confirm again", target.Path())
	}
	o, err := s.resource(target).Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if err := verifySelectedIdentity(*target, o); err != nil {
		return nil, err
	}
	if o.GetResourceVersion() == "" {
		return nil, fmt.Errorf("resource version unavailable for %s", target.Path())
	}
	return o, nil
}

//nolint:gocritic // Retain the immutable captured identity across the worker boundary and retries.
func (s *operationSession) restart(ctx context.Context, target SelectedResourceTarget, opts metav1.PatchOptions) error {
	if err := checkOperationTarget(&target); err != nil {
		return err
	}
	if target.GVR.G() != "apps" || (target.GVR.R() != "deployments" && target.GVR.R() != "daemonsets" && target.GVR.R() != "statefulsets") {
		return errors.New("resource is not restartable")
	}
	if err := s.authorize(ctx, &target, "", client.GetVerb, client.PatchVerb); err != nil {
		return err
	}
	restartedAt := time.Now().Format(time.RFC3339Nano)
	return retryOperationConflict(ctx, func() error {
		o, err := s.readTarget(ctx, &target)
		if err != nil {
			return err
		}
		if paused, _, _ := unstructured.NestedBool(o.Object, "spec", "paused"); paused {
			return errors.New("cannot restart a paused deployment; resume it first")
		}
		patch, err := json.Marshal(map[string]any{
			"metadata": map[string]any{"uid": string(target.UID), "resourceVersion": o.GetResourceVersion()},
			"spec": map[string]any{"template": map[string]any{"metadata": map[string]any{"annotations": map[string]string{
				"kubectl.kubernetes.io/restartedAt": restartedAt,
			}}}},
		})
		if err != nil {
			return err
		}
		_, err = s.resource(&target).Patch(ctx, target.Name, types.MergePatchType, patch, opts)
		return err
	})
}

func (s *operationSession) readScaleTarget(ctx context.Context, target *SelectedResourceTarget) (*unstructured.Unstructured, error) {
	if target.UID == "" {
		return nil, fmt.Errorf("identity unavailable for %s; refresh and confirm again", target.Path())
	}
	o, err := s.resource(target).Get(ctx, target.Name, metav1.GetOptions{}, "scale")
	if err != nil {
		return nil, err
	}
	if err := verifySelectedIdentity(*target, o); err != nil {
		return nil, err
	}
	if o.GetResourceVersion() == "" {
		return nil, fmt.Errorf("resource version unavailable for %s", target.Path())
	}
	return o, nil
}

//nolint:gocritic // Keep the captured dialog identity independent from subsequent UI selection.
func (s *operationSession) replicas(ctx context.Context, target SelectedResourceTarget) (int64, error) {
	if err := checkOperationTarget(&target); err != nil {
		return 0, err
	}
	if err := s.authorize(ctx, &target, "scale", client.GetVerb); err != nil {
		return 0, err
	}
	o, err := s.readScaleTarget(ctx, &target)
	if err != nil {
		return 0, err
	}
	n, found, err := unstructured.NestedInt64(o.Object, "spec", "replicas")
	if err != nil || !found {
		return 0, fmt.Errorf("desired replicas unavailable for %s", target.Path())
	}
	return n, nil
}

//nolint:gocritic // Retain the immutable captured identity across the worker boundary and retries.
func (s *operationSession) scale(ctx context.Context, target SelectedResourceTarget, replicas int32) error {
	if err := checkOperationTarget(&target); err != nil {
		return err
	}
	if replicas < 0 {
		return errors.New("replicas must be between 0 and 2147483647")
	}
	if err := s.authorize(ctx, &target, "scale", client.GetVerb, client.UpdateVerb); err != nil {
		return err
	}
	return retryOperationConflict(ctx, func() error {
		o, err := s.readScaleTarget(ctx, &target)
		if err != nil {
			return err
		}
		if setErr := unstructured.SetNestedField(o.Object, int64(replicas), "spec", "replicas"); setErr != nil {
			return setErr
		}
		_, err = s.resource(&target).Update(ctx, o, metav1.UpdateOptions{}, "scale")
		return err
	})
}

//nolint:gocritic // Retain the immutable captured identity across the worker boundary and retries.
func (s *operationSession) delete(ctx context.Context, target SelectedResourceTarget, propagation *metav1.DeletionPropagation, grace dao.Grace) error {
	if err := checkOperationTarget(&target); err != nil {
		return err
	}
	if err := s.authorize(ctx, &target, "", client.GetVerb, client.DeleteVerb); err != nil {
		return err
	}
	return retryOperationConflict(ctx, func() error {
		o, err := s.readTarget(ctx, &target)
		if err != nil {
			return err
		}
		uid, version := target.UID, o.GetResourceVersion()
		opts := metav1.DeleteOptions{PropagationPolicy: propagation, Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &version}}
		if grace != dao.DefaultGrace {
			period := int64(grace)
			opts.GracePeriodSeconds = &period
		}
		return s.resource(&target).Delete(ctx, target.Name, opts)
	})
}

// Synthetic accessors (Helm, files and port forwards) also run in the worker.
// Pin the config object and context name used by Helm before its dialog opens.
type pinnedOperationConnection struct {
	client.Connection
	config      *client.Config
	contextName string
}

func (c pinnedOperationConnection) Config() *client.Config { return c.config }
func (c pinnedOperationConnection) ActiveContext() string  { return c.contextName }

type pinnedOperationFactory struct {
	dao.Factory
	connection client.Connection
	forwarders watch.Forwarders
}

func (f pinnedOperationFactory) Client() client.Connection   { return f.connection }
func (f pinnedOperationFactory) DeleteForwarder(path string) { f.forwarders.Kill(path) }

func captureSyntheticDelete(v ResourceViewer, contextName string) (dao.Nuker, error) {
	app := v.App()
	conn := app.Conn()
	if conn != nil {
		flags := client.SnapshotConfigFlags(conn.Config().Flags())
		flags.Context = &contextName
		conn = pinnedOperationConnection{Connection: conn, config: client.NewConfig(flags), contextName: contextName}
	}
	forwarders := watch.NewForwarders()
	if v.GVR() == client.PfGVR {
		for k, forwarder := range app.factory.Forwarders() {
			forwarders[k] = forwarder
		}
	}
	accessor, err := dao.AccessorFor(pinnedOperationFactory{Factory: app.factory, connection: conn, forwarders: forwarders}, v.GVR())
	if err != nil {
		return nil, err
	}
	nuker, ok := accessor.(dao.Nuker)
	if !ok {
		return nil, fmt.Errorf("resource %s cannot be deleted", v.GVR())
	}
	return nuker, nil
}
