// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package maintenance

import (
	"time"

	"github.com/derailed/k9s/internal/inspect"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
)

const (
	MaxPods            = 500
	MaxBudgets         = 500
	MaxNamespaces      = 32
	ReadTimeout        = 3 * time.Second
	CollectTimeout     = 10 * time.Second
	Complete           = "complete"
	Partial            = "partial"
	Denied             = "denied"
	Unavailable        = "unavailable"
	PodsSource         = "Node Pods"
	BudgetsSource      = "PodDisruptionBudgets"
	ReadyStateReady    = "Ready"
	ReadyStateNotReady = "Not ready"
	ReadyStateUnknown  = "Unknown"
)

var Tabs = []string{"Overview", "Workloads", "PDBs", "Constraints", "Outcomes", "Evidence"}

// Only explicit, non-secret evidence is retained. Pod environments, container
// commands, annotations, volume credentials and arbitrary objects are excluded.
type Snapshot struct {
	Identity   inspect.ResourceIdentity
	CapturedAt time.Time
	Node       Node
	Pods       []Pod
	Budgets    []Budget
	Coverage   []Coverage
}

type Node struct {
	Unschedulable   bool
	Ready           string
	ResourceVersion string
}

type Pod struct {
	Identity        inspect.ResourceIdentity
	Phase, Ready    string
	Owner           string
	OwnerKind       string
	GraceSeconds    int64
	GraceDefault    bool
	Terminating     bool
	Mirror          bool
	EmptyDir        []string
	HostPath        []string
	Claims          []string
	Constraints     []string
	BudgetEvidence  []string
	MatchingBudgets int
}

type Budget struct {
	Identity                                                         inspect.ResourceIdentity
	Generation, ObservedGeneration                                   int64
	DisruptionsAllowed, CurrentHealthy, DesiredHealthy, ExpectedPods int32
	Policy, Selector, SelectorError                                  string
	Matches                                                          int
}

type Coverage struct {
	Source, Scope, State, Detail string
	ObservedAt                   time.Time
	Count, Limit                 int
}

func (s *Snapshot) Partial() bool {
	for index := range s.Coverage {
		source := &s.Coverage[index]
		if source.State != Complete {
			return true
		}
	}
	return !s.PDBsComplete()
}

func (s *Snapshot) PDBsComplete() bool {
	observed := false
	for index := range s.Coverage {
		source := &s.Coverage[index]
		if source.Source == BudgetsSource {
			observed = true
			if source.State != Complete {
				return false
			}
		}
	}
	for index := range s.Budgets {
		budget := &s.Budgets[index]
		if budget.SelectorError != "" || budget.Generation <= 0 || budget.Generation != budget.ObservedGeneration ||
			budget.DisruptionsAllowed < 0 || budget.CurrentHealthy < 0 || budget.DesiredHealthy < 0 || budget.ExpectedPods < 0 ||
			(budget.Policy != string(policyv1.AlwaysAllow) && budget.Policy != string(policyv1.IfHealthyBudget)) {
			return false
		}
	}
	for index := range s.Pods {
		pod := &s.Pods[index]
		if pod.MatchingBudgets > 1 || (pod.MatchingBudgets > 0 && pod.Phase == string(corev1.PodRunning) && pod.Ready == ReadyStateUnknown) {
			return false
		}
	}
	return observed
}

func (s *Snapshot) PodsComplete() bool {
	for index := range s.Coverage {
		source := &s.Coverage[index]
		if source.Source == PodsSource {
			return source.State == Complete
		}
	}
	return false
}

// ReviewedPods is an identity set, not an instruction to delete every Pod.
// Native kubectl filters still decide which reviewed Pods are eligible.
func (s *Snapshot) ReviewedPods() map[string]string {
	if !s.PodsComplete() {
		return nil
	}
	result := make(map[string]string, len(s.Pods))
	for index := range s.Pods {
		pod := &s.Pods[index]
		result[pod.Identity.Namespace+"/"+pod.Identity.Name] = pod.Identity.UID
	}
	return result
}
