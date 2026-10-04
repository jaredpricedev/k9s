// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package review

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

const (
	RolloutTimedOut   = "timeout; outcome unknown"
	RolloutCanceled   = "canceled; outcome unknown"
	RolloutSuperseded = "superseded; outcome unknown"
	maxOutcomeReads   = 120
)

// RolloutOutcomeRequest captures one controller revision. AcceptedAt and
// OperationID may be supplied only by an operation's accepted-write receipt.
// An empty pair follows read-only evidence without claiming an operation ran.
type RolloutOutcomeRequest struct {
	Identity          inspect.ResourceIdentity
	Generation        int64
	TemplateSHA256    string
	AcceptedAt        time.Time
	OperationID       string
	Timeout, Interval time.Duration
	MaxReads          int
}

// RolloutOutcome retains controller-only follow-up evidence. It never relabels
// previously collected children with the new controller observation time.
type RolloutOutcome struct {
	Identity                                          inspect.ResourceIdentity
	Generation                                        int64
	TemplateSHA256                                    string
	OperationID                                       string
	AcceptedAt, StartedAt, LastObservedAt, FinishedAt time.Time
	State, Reason                                     string
	Reads                                             int
	Snapshot                                          *RolloutSnapshot
}

// FollowRollout performs bounded named GETs only. Controller completion is an
// observed status verdict, not application availability or a persisted-write
// guarantee. Canceling observation does not cancel or roll back an accepted write.
func FollowRollout(ctx context.Context, reader dynamic.Interface, request *RolloutOutcomeRequest, update func(*RolloutOutcome)) *RolloutOutcome {
	o := &RolloutOutcome{StartedAt: time.Now().UTC(), State: RolloutUnknown}
	if request == nil {
		return finishOutcome(o, RolloutUnknown, "Captured outcome target unavailable")
	}
	captured := *request
	request = &captured
	o.Identity, o.Generation, o.TemplateSHA256 = request.Identity, request.Generation, request.TemplateSHA256
	o.AcceptedAt, o.OperationID = request.AcceptedAt, request.OperationID
	gvr, kind, err := outcomeTarget(request)
	if err != nil || reader == nil {
		return finishOutcome(o, RolloutUnknown, "Captured controller UID, generation, template or accepted receipt unavailable")
	}
	timeout, interval, reads := outcomeLimits(request)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for o.Reads < reads {
		if ctx.Err() != nil {
			return outcomeStopped(o, ctx.Err())
		}
		read := boundedGet(ctx, reader, gvr, request.Identity.Namespace, request.Identity.Name)
		o.Reads++
		if read.err != nil {
			return outcomeReadFailed(o, read.err)
		}
		object := read.object
		if object == nil || object.GetAPIVersion() != rolloutAppsAPI || object.GetKind() != kind ||
			object.GetNamespace() != request.Identity.Namespace || object.GetName() != request.Identity.Name || string(object.GetUID()) != request.Identity.UID {
			return finishOutcome(o, RolloutSuperseded, "Captured resource identity changed or disappeared; no replacement was followed")
		}
		snapshot := NewRolloutSnapshot(object, nil, nil, []RolloutCoverage{{Source: "Follow-up children", State: inspect.ObservationUnknown,
			Detail: "Controller-only named GET; original retained child evidence keeps its original source and capture time"}}, request.Identity.Context, time.Now().UTC())
		if snapshot.Generation == nil || *snapshot.Generation != request.Generation || snapshot.TemplateSHA256 != request.TemplateSHA256 {
			return finishOutcome(o, RolloutSuperseded, "Generation or template changed; another update cannot establish this target's outcome")
		}
		o.Snapshot, o.LastObservedAt = snapshot, snapshot.CapturedAt
		o.State, o.Reason = snapshot.Progress()
		if o.State != RolloutProgressing {
			o.FinishedAt = time.Now().UTC()
			return o
		}
		if update != nil {
			retained := *o
			update(&retained)
		}
		timer := time.NewTimer(interval)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return outcomeStopped(o, ctx.Err())
		}
	}
	return finishOutcome(o, RolloutTimedOut, "Bounded observation read limit reached; completion was not established")
}

func outcomeTarget(r *RolloutOutcomeRequest) (schema.GroupVersionResource, string, error) {
	var resource, kind string
	switch r.Identity.GVR {
	case "apps/v1/deployments":
		resource, kind = "deployments", rolloutDeployment
	case "apps/v1/statefulsets":
		resource, kind = "statefulsets", rolloutStatefulSet
	case "apps/v1/daemonsets":
		resource, kind = "daemonsets", rolloutDaemonSet
	}
	if resource == "" || !validReviewName(r.Identity.Namespace) || !validReviewName(r.Identity.Name) || r.Identity.UID == "" ||
		r.Generation <= 0 || len(r.TemplateSHA256) != 64 ||
		(r.AcceptedAt.IsZero() != (r.OperationID == "")) {
		return schema.GroupVersionResource{}, "", fmt.Errorf("Invalid captured outcome request")
	}
	if _, err := hex.DecodeString(r.TemplateSHA256); err != nil {
		return schema.GroupVersionResource{}, "", err
	}
	return schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: resource}, kind, nil
}

func outcomeLimits(r *RolloutOutcomeRequest) (timeout, interval time.Duration, reads int) {
	timeout = r.Timeout
	if timeout <= 0 || timeout > 5*time.Minute {
		timeout = 2 * time.Minute
	}
	interval = r.Interval
	if interval < 100*time.Millisecond {
		interval = time.Second
	}
	reads = r.MaxReads
	if reads <= 0 || reads > maxOutcomeReads {
		reads = maxOutcomeReads
	}
	return timeout, interval, reads
}

func finishOutcome(o *RolloutOutcome, state, reason string) *RolloutOutcome {
	o.State, o.Reason, o.FinishedAt = state, reason, time.Now().UTC()
	return o
}

func outcomeStopped(o *RolloutOutcome, err error) *RolloutOutcome {
	if errors.Is(err, context.Canceled) {
		return finishOutcome(o, RolloutCanceled, "Observation canceled; no rollback or cancellation of accepted changes was performed")
	}
	return finishOutcome(o, RolloutTimedOut, "Bounded observation deadline reached; completion was not established")
}

func outcomeReadFailed(o *RolloutOutcome, err error) *RolloutOutcome {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return outcomeStopped(o, err)
	}
	if apierrors.IsNotFound(err) {
		return finishOutcome(o, RolloutSuperseded, "Captured resource disappeared; no replacement was followed")
	}
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		return finishOutcome(o, RolloutUnknown, "Controller follow-up denied; accepted changes may still be progressing")
	}
	return finishOutcome(o, RolloutUnknown, "Controller follow-up unavailable; completion was not established")
}
