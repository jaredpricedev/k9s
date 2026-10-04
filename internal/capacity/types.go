// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package capacity

import (
	"strings"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/inspect"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
)

type State string

const (
	Complete      State = "complete"
	Empty         State = "empty"
	Partial       State = "partial"
	Denied        State = "denied"
	Absent        State = "absent"
	Unavailable   State = "unavailable"
	NotConfigured State = "not configured"
)

const (
	SourcePods       = "Pods"
	SourceNodes      = "Nodes"
	SourceQuota      = "Quota"
	SourceLimitRange = "LimitRange"
	SourceHPA        = "HPA"
	SourceVPA        = "VPA"
	SourceMetrics    = "Metrics"
	SourceHistory    = "History"
	unknownValue     = "unknown"
	notReported      = "not reported"
)

// Scope is captured before collection. Selector membership is not ownership.
type Scope struct {
	Identity                           inspect.ResourceIdentity
	Kind                               string
	Namespace, Selector, Node, PodName string
}

type Coverage struct {
	Source         string
	State          State
	ReadAt         time.Time
	Visible, Bound int
	Detail         string
}

type Pod struct {
	Identity                            inspect.ResourceIdentity
	Phase, Node                         string
	Requests, Limits, SchedulerRequests corev1.ResourceList
	Containers                          []inspect.ResourceBudget
	Usage                               Usage
	Pending                             []corev1.PodCondition
	Placement                           []string
	Terminal                            bool
	LimitGaps                           []string
}

type Usage struct {
	Sample     client.MetricSample
	Values     corev1.ResourceList
	Window     time.Duration
	Containers map[string]corev1.ResourceList
	Complete   bool
}

type Node struct {
	Identity      inspect.ResourceIdentity
	Allocatable   corev1.ResourceList
	Unschedulable bool
	Conditions    []corev1.NodeCondition
	Taints        []corev1.Taint
}

type Quota struct {
	Identity      inspect.ResourceIdentity
	Hard, Used    corev1.ResourceList
	Desired       corev1.ResourceList
	Scopes        []corev1.ResourceQuotaScope
	ScopeSelector *corev1.ScopeSelector
}

type LimitRange struct {
	Identity inspect.ResourceIdentity
	Limits   []corev1.LimitRangeItem
}

type Autoscaler struct {
	Identity              inspect.ResourceIdentity
	Target                autoscalingv2.CrossVersionObjectReference
	Min                   *int32
	Max, Current, Desired int32
	CurrentReported       bool
	DesiredReported       bool
	Generation            int64
	ObservedGeneration    *int64
	Metrics               []autoscalingv2.MetricSpec
	CurrentMetrics        []autoscalingv2.MetricStatus
	Conditions            []autoscalingv2.HorizontalPodAutoscalerCondition
}

type Recommendation struct {
	Identity     inspect.ResourceIdentity
	Target, Mode string
	APIVersion   string
	Containers   []ContainerRecommendation
}

type ContainerRecommendation struct {
	Name                 string
	Target, Lower, Upper corev1.ResourceList
}

type Snapshot struct {
	Scope           Scope
	CapturedAt      time.Time
	Pods            []Pod
	Nodes           []Node
	Quotas          []Quota
	LimitRanges     []LimitRange
	Autoscalers     []Autoscaler
	Recommendations []Recommendation
	Coverage        []Coverage
}

func (s *Snapshot) Partial() bool {
	for _, c := range s.Coverage {
		if c.Source == SourceVPA && c.State == Absent {
			continue
		}
		if c.State != Complete && c.State != Empty && c.State != NotConfigured {
			return true
		}
	}
	for i := range s.Pods {
		pod := &s.Pods[i]
		if !pod.Terminal && (!pod.Usage.Complete || !pod.Usage.Sample.At(time.Now()).Fresh()) {
			return true
		}
	}
	for i := range s.Nodes {
		for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
			if value, found := s.Nodes[i].Allocatable[name]; !found || value.Sign() < 0 {
				return true
			}
		}
	}
	for i := range s.Autoscalers {
		hpa := &s.Autoscalers[i]
		if hpa.ObservedGeneration == nil || *hpa.ObservedGeneration < hpa.Generation ||
			!hpa.CurrentReported || !hpa.DesiredReported || len(hpa.MissingInputs()) > 0 {
			return true
		}
	}
	for i := range s.Quotas {
		if len(s.Quotas[i].Desired) > 0 && len(s.Quotas[i].Hard) == 0 {
			return true
		}
		for name := range s.Quotas[i].Hard {
			if _, found := s.Quotas[i].Used[name]; !found {
				return true
			}
		}
	}
	return false
}

func (s *Snapshot) Source(name string) Coverage {
	for _, source := range s.Coverage {
		if source.Source == name {
			return source
		}
	}
	return Coverage{Source: name, State: Unavailable, Detail: "Not collected"}
}

// MetricStateKey changes when retained available usage ages into stale usage.
// The UI uses it to update evidence without resetting a tab's scroll/search.
func (s *Snapshot) MetricStateKey(now time.Time) string {
	var key strings.Builder
	for i := range s.Pods {
		sample := s.Pods[i].Usage.Sample.At(now)
		key.WriteString(string(sample.State))
		key.WriteByte('/')
		key.WriteString(string(sample.Failure))
		key.WriteByte(';')
	}
	return key.String()
}
