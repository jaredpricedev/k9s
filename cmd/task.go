// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package cmd

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/task"
	"github.com/derailed/k9s/internal/workspace"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	pathvalidation "k8s.io/apimachinery/pkg/api/validation/path"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/dynamic"
)

type taskStatusError struct {
	code    int
	message string
}

func (e taskStatusError) Error() string { return e.message }
func (e taskStatusError) ExitCode() int { return e.code }

func taskCmd() *cobra.Command {
	flags := genericclioptions.NewConfigFlags(true)
	var format string
	var deadline time.Duration
	var allowPartial bool
	command := &cobra.Command{Use: "task", Short: "Read-only task reports without a full-screen terminal", SilenceUsage: true,
		Long: "Collect bounded, explicitly scoped read-only task evidence, or replay a retained evidence bundle. " +
			"Text and versioned JSON work with pipes, TERM=dumb and screen readers. No raw mode, prompts or optional provider commands."}
	flags.AddFlags(command.PersistentFlags())
	command.PersistentFlags().StringVarP(&format, "output", "o", "text", "Output format: text or json")
	command.PersistentFlags().DurationVar(&deadline, "deadline", 30*time.Second, "Total API task deadline (1s to 60s)")
	command.PersistentFlags().BoolVar(&allowPartial, "allow-partial", false, "Return success for a usable partial report; coverage stays explicit")
	finish := func(c *cobra.Command, r task.Report) error {
		if err := task.Write(c.OutOrStdout(), r, format); err != nil {
			return err
		}
		if !r.Complete && !allowPartial {
			return taskStatusError{code: 2, message: "Task evidence is incomplete; inspect coverage and limits in the report"}
		}
		return nil
	}
	validate := func() error {
		if format != "text" && format != "json" {
			return errors.New("output must be text or json")
		}
		if deadline < time.Second || deadline > 60*time.Second {
			return errors.New("deadline must be between 1s and 60s")
		}
		return nil
	}
	var kinds []string
	var selector string
	ws := &cobra.Command{Use: "workspace", SilenceUsage: true, Short: "Summarize current findings in explicit namespaces", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if err := validate(); err != nil {
				return err
			}
			scope, err := taskScope(flags, kinds, selector)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(c.Context(), deadline)
			defer cancel()
			reader, err := taskClient(ctx, scope.Context, flags)
			if err != nil {
				return err
			}
			r := task.Workspace(scope, workspace.Collect(ctx, reader, scope, time.Now().UTC()))
			return finish(c, r)
		}}
	ws.Flags().StringSliceVar(&kinds, "kinds", nil, "Curated plural aliases (pods,deployments) or full GVRs; Secrets excluded")
	ws.Flags().StringVar(&selector, "selector", "", "Kubernetes label selector within the selected namespaces")
	inv := &cobra.Command{Use: "investigate RESOURCE NAME", SilenceUsage: true, Short: "Retain current resource status, previous exits and bounded UID-matching events",
		Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			if err := validate(); err != nil {
				return err
			}
			scope, err := taskScope(flags, []string{args[0]}, "")
			if err != nil {
				return err
			}
			if len(scope.Namespaces) != 1 {
				return errors.New("investigation requires one explicit namespace")
			}
			gvr, _, err := workspace.ResolveKind(args[0])
			if err != nil {
				return err
			}
			if len(pathvalidation.IsValidPathSegmentName(args[1])) != 0 || args[1] == "" {
				return errors.New("supply a resource name; namespace is a separate flag")
			}
			ctx, cancel := context.WithTimeout(c.Context(), deadline)
			defer cancel()
			reader, err := taskClient(ctx, scope.Context, flags)
			if err != nil {
				return err
			}
			return finish(c, collectTaskInvestigation(ctx, reader, gvr, scope, args[1]))
		}}
	ev := &cobra.Command{Use: "evidence FILE", SilenceUsage: true, Short: "Replay a safe retained evidence bundle offline", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if err := validate(); err != nil {
				return err
			}
			bundle, err := inspect.ReadBundle(args[0])
			if err != nil {
				return errors.New("cannot read a valid retained evidence bundle")
			}
			r, err := task.Evidence(bundle)
			if err != nil {
				return err
			}
			return finish(c, r)
		}}
	command.AddCommand(ws, inv, ev)
	return command
}

func taskScope(flags *genericclioptions.ConfigFlags, kinds []string, selector string) (workspace.Scope, error) {
	if flags.Context == nil || strings.TrimSpace(*flags.Context) == "" {
		return workspace.Scope{}, errors.New("task collection requires an explicit --context")
	}
	if flags.Namespace == nil || strings.TrimSpace(*flags.Namespace) == "" {
		return workspace.Scope{}, errors.New("task collection requires an explicit --namespace (comma-separated names supported)")
	}
	return workspace.NormalizeScope(workspace.Scope{Name: "linear task", Context: *flags.Context,
		Namespaces: strings.Split(*flags.Namespace, ","), Kinds: kinds, LabelSelector: selector})
}

// Configured credential helpers can ignore cancellation. Stage private clients
// under the task deadline and discard late setup results; no TUI is initialized.
func taskClient(ctx context.Context, contextName string, flags *genericclioptions.ConfigFlags) (dynamic.Interface, error) {
	captured, err := client.NewConfig(flags).PinnedDiagnosticConfig(contextName)
	if err != nil {
		return nil, errors.New("cannot capture configuration for this named context")
	}
	type result struct {
		reader dynamic.Interface
		err    error
	}
	ready := make(chan result, 1)
	go func() {
		cfg, setupErr := captured.RESTConfig()
		if setupErr != nil {
			ready <- result{err: errors.New("named-context configuration could not be loaded")}
			return
		}
		cfg.Timeout = 10 * time.Second
		reader, setupErr := dynamic.NewForConfig(cfg)
		if setupErr != nil {
			setupErr = errors.New("named-context API client could not be created")
		}
		ready <- result{reader: reader, err: setupErr}
	}()
	select {
	case r := <-ready:
		if ctx.Err() != nil {
			return nil, errors.New("task setup canceled or timed out")
		}
		return r.reader, r.err
	case <-ctx.Done():
		return nil, errors.New("task setup canceled or timed out")
	}
}

func taskReadState(err error) (state, detail string) {
	switch {
	case apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err):
		return inspect.ObservationDenied, "API read denied for the captured actor"
	case apierrors.IsNotFound(err):
		return inspect.ObservationUnknown, "Named resource or API is absent"
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		return inspect.ObservationIncomplete, "Read canceled or timed out"
	default:
		return inspect.ObservationUnknown, "API read unavailable; use connection diagnostics to inspect credential, TLS or discovery state"
	}
}

//nolint:gocritic // The explicit scope value is a fixed captured destination, never mutated by collection.
func collectTaskInvestigation(ctx context.Context, reader dynamic.Interface, gvr schema.GroupVersionResource, scope workspace.Scope, name string) task.Report {
	now := time.Now().UTC()
	id := inspect.ResourceIdentity{Context: scope.Context, GVR: gvr.String(), Namespace: scope.Namespaces[0], Name: name}
	object, err := reader.Resource(gvr).Namespace(id.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		state, reason := taskReadState(err)
		return task.Investigation(inspect.Observation{Identity: id, Source: "named API GET", ObservedAt: now, State: state, Reason: reason}, nil)
	}
	if object == nil || object.GetNamespace() != id.Namespace || object.GetName() != name || object.GetUID() == "" {
		return task.Investigation(inspect.Observation{Identity: id, Source: "named API GET", ObservedAt: now, State: inspect.ObservationIncomplete,
			Reason: "Response did not retain the requested resource identity"}, nil)
	}
	id.UID = string(object.GetUID())
	observation := inspect.NewObservation(id, "named API GET", now, object.Object)
	if observation.Object == nil {
		return task.Investigation(observation, nil)
	}
	investigation := inspect.NewInvestigation(&unstructured.Unstructured{Object: observation.Object}, id.Context, id.GVR, now)
	events, err := reader.Resource(schema.GroupVersionResource{Version: "v1", Resource: "events"}).Namespace(id.Namespace).List(ctx,
		metav1.ListOptions{FieldSelector: "involvedObject.uid=" + id.UID, Limit: 100})
	coverage := inspect.InvestigationCoverage{Source: "core/v1 events / involvedObject.uid", State: inspect.ObservationComplete,
		Detail: "Bounded current retained API Events; not continuous history"}
	if err != nil {
		coverage.State, coverage.Detail = taskReadState(err)
	} else if events == nil {
		coverage.State, coverage.Detail = inspect.ObservationUnknown, "API returned no event response"
	} else {
		var retained []corev1.Event
		for index := range events.Items[:min(len(events.Items), 100)] {
			var event corev1.Event
			if conversionErr := runtime.DefaultUnstructuredConverter.FromUnstructured(events.Items[index].Object, &event); conversionErr != nil {
				coverage.State, coverage.Detail = inspect.ObservationIncomplete, "Some Events could not be decoded"
				continue
			}
			if event.Namespace != id.Namespace || event.InvolvedObject.Namespace != id.Namespace || string(event.InvolvedObject.UID) != id.UID {
				coverage.State, coverage.Detail = inspect.ObservationIncomplete, "Some Events did not retain the captured identity"
				continue
			}
			retained = append(retained, event)
		}
		investigation.AddEvents(retained)
		if events.GetContinue() != "" || len(events.Items) > 100 {
			coverage.State, coverage.Detail = inspect.ObservationIncomplete, "Event page limit reached; additional Events not read"
		}
	}
	investigation.Coverage = append(investigation.Coverage, coverage)
	return task.Investigation(observation, investigation)
}
