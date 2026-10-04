// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package workspace

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func queueHistoryFixture() (*QueueWindow, Snapshot) {
	at := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	w := NewQueueWindow(Scope{Name: "app", Context: "lab", Namespaces: []string{"apps"}, Kinds: []string{"pods"}, LabelSelector: "app=api"}, at)
	f := Finding{Ref: ResourceRef{GVR: "v1/pods", Namespace: "apps", Name: "api", UID: "pod-a"}, Kind: "Pod", Category: "fault", Reason: "CrashLoopBackOff", Detail: "api: current state", ObservedAt: at}
	return w, Snapshot{ObservedAt: at, Findings: []Finding{f}, Resources: []Resource{{Ref: f.Ref, Kind: "Pod"}}, Coverage: []Coverage{{GVR: "v1/pods", Namespace: "apps", State: coverageComplete}}}
}

func TestQueueWindowObservedPersistentAddedAndClearedHaveSourceIntervals(t *testing.T) {
	w, s := queueHistoryFixture()
	w.Observe(&s, nil)
	if len(w.History) != 1 || w.History[0].State != QueueFirstObserved {
		t.Fatal(w.History)
	}
	s.ObservedAt = s.ObservedAt.Add(time.Minute)
	w.Observe(&s, nil)
	a := w.Active()[0]
	if a.State != QueuePersistent || !a.FirstObservedAt.Equal(w.StartedAt) || !a.LastObservedAt.Equal(s.ObservedAt) || len(w.History) != 1 {
		t.Fatal(a, w.History)
	}
	s.ObservedAt = s.ObservedAt.Add(time.Minute)
	s.Findings = nil
	w.Observe(&s, nil)
	c := w.History[1]
	if len(w.Active()) != 0 || c.State != QueueCleared || c.Ref.UID != "pod-a" || !c.LastObservedAt.Equal(w.StartedAt.Add(time.Minute)) || c.Coverage.State != coverageComplete || !strings.Contains(c.Detail, "not proof") {
		t.Fatal(c)
	}
	s.Findings = []Finding{{Ref: s.Resources[0].Ref, Category: "fault", Reason: "CrashLoopBackOff"}}
	s.ObservedAt = s.ObservedAt.Add(time.Minute)
	w.Observe(&s, nil)
	if w.History[2].State != QueueAdded {
		t.Fatal(w.History)
	}
}

func TestQueueWindowEveryIncompleteQueryRetainsUnknownRatherThanCleared(t *testing.T) {
	for _, state := range []string{"denied", "absent", "stale", "truncated", "unavailable", "canceled", "unknown"} {
		t.Run(state, func(t *testing.T) {
			w, s := queueHistoryFixture()
			w.Observe(&s, nil)
			s.ObservedAt = s.ObservedAt.Add(time.Minute)
			s.Findings = nil
			s.Resources = nil
			s.Coverage[0].State = state
			w.Observe(&s, nil)
			a := w.Active()
			if len(a) != 1 || a[0].State != QueueUnknown || !a[0].LastObservedAt.Equal(w.StartedAt) || len(w.History) != 2 || w.History[1].State != QueueGap {
				t.Fatal(a, w.History)
			}
		})
	}
	for _, variant := range []string{"truncated flag", "missing coverage", "ambiguous coverage", "canceled collector"} {
		t.Run(variant, func(t *testing.T) {
			w, s := queueHistoryFixture()
			w.Observe(&s, nil)
			s.ObservedAt = s.ObservedAt.Add(time.Minute)
			s.Findings = nil
			var err error
			switch variant {
			case "truncated flag":
				s.Coverage[0].Truncated = true
			case "missing coverage":
				s.Coverage = nil
			case "ambiguous coverage":
				s.Coverage = append(s.Coverage, s.Coverage[0])
			case "canceled collector":
				err = errors.New("deadline")
			}
			w.Observe(&s, err)
			if len(w.Active()) != 1 || w.Active()[0].State != QueueUnknown {
				t.Fatal(w.Active())
			}
		})
	}
}

func TestQueueWindowMatchingNamespaceGVRAndUIDReplacement(t *testing.T) {
	w, s := queueHistoryFixture()
	w.Observe(&s, nil)
	s.ObservedAt = s.ObservedAt.Add(time.Minute)
	s.Findings = nil
	s.Coverage = []Coverage{{GVR: "apps/v1/deployments", Namespace: "apps", State: coverageComplete}, {GVR: "v1/pods", Namespace: "other", State: coverageComplete}}
	w.Observe(&s, nil)
	if len(w.Active()) != 1 || w.Active()[0].State != QueueUnknown {
		t.Fatal(w.Active())
	}
	s.ObservedAt = s.ObservedAt.Add(time.Minute)
	s.Coverage = []Coverage{{GVR: "v1/pods", Namespace: "apps", State: coverageComplete}}
	s.Resources[0].Ref.UID = "pod-b"
	w.Observe(&s, nil)
	c := w.History[len(w.History)-1]
	if c.State != QueueReplaced || c.Ref.UID != "pod-a" || c.ReplacementUID != "pod-b" || strings.Contains(c.Detail, "proven resolved") == false {
		t.Fatal(c)
	}
}

func TestQueueWindowStaleRefreshCannotRewindOrClear(t *testing.T) {
	w, s := queueHistoryFixture()
	w.Observe(&s, nil)
	s.ObservedAt = s.ObservedAt.Add(time.Minute)
	w.Observe(&s, nil)
	s.ObservedAt = w.StartedAt
	s.Findings = nil
	w.Observe(&s, nil)
	if !w.LastRefreshAt.Equal(w.StartedAt.Add(time.Minute)) || len(w.Active()) != 1 || w.Active()[0].State != QueueUnknown || w.History[len(w.History)-1].State != QueueGap {
		t.Fatal(w.Active(), w.History)
	}
}

func TestQueueWindowRetentionAndRestartAreExplicit(t *testing.T) {
	w, s := queueHistoryFixture()
	w.Observe(&s, nil)
	for range MaxQueueHistory + 10 {
		s.ObservedAt = s.ObservedAt.Add(time.Second)
		s.Coverage[0].State = "denied"
		w.Observe(&s, nil)
	}
	if len(w.History) != MaxQueueHistory || w.Dropped == 0 || w.PrunedBefore.IsZero() {
		t.Fatal(len(w.History), w.Dropped)
	}
	s.ObservedAt = s.ObservedAt.Add(QueueRetention + time.Minute)
	s.Coverage[0].State = coverageComplete
	w.Observe(&s, nil)
	if len(w.History) != 1 || w.History[0].State != QueueFirstObserved {
		t.Fatal(w.History)
	}
	restarted := NewQueueWindow(Scope{Name: w.ScopeName, Context: w.Context, Namespaces: []string{"apps"}, Kinds: []string{"pods"}}, s.ObservedAt)
	if len(restarted.History) != 0 || len(restarted.Active()) != 0 || !restarted.StartedAt.Equal(s.ObservedAt) {
		t.Fatal(restarted)
	}
	restarted.Observe(&s, nil)
	if restarted.History[0].State != QueueFirstObserved {
		t.Fatal(restarted.History)
	}
}

func TestQueueWindowBoundsTrackingAndExcludesUnidentifiedOrOutsideScope(t *testing.T) {
	w, s := queueHistoryFixture()
	for i := range MaxTrackedFindings + 10 {
		f := s.Findings[0]
		f.Ref.Name = fmt.Sprint(i)
		f.Ref.UID = fmt.Sprint(i)
		s.Findings = append(s.Findings, f)
	}
	f := s.Findings[0]
	f.Ref.UID = ""
	s.Findings = append(s.Findings, f)
	f.Ref.Namespace = "other"
	f.Ref.UID = "outside"
	s.Findings = append(s.Findings, f)
	w.Observe(&s, nil)
	if len(w.Active()) != MaxTrackedFindings || len(w.History) != MaxQueueHistory || w.Dropped == 0 {
		t.Fatal(len(w.Active()), len(w.History), w.Dropped)
	}
	for _, f := range w.Active() {
		if f.Finding.Ref.UID == "" || f.Finding.Ref.Namespace != "apps" {
			t.Fatal(f)
		}
	}
}

func TestQueueWindowDuplicateReasonsRetainBothContainersWithoutFalsePersistence(t *testing.T) {
	w, s := queueHistoryFixture()
	second := s.Findings[0]
	second.Detail = "proxy: current state"
	s.Findings = append(s.Findings, second)
	w.Observe(&s, nil)
	a := w.Active()
	if len(a) != 1 || len(w.History) != 1 || a[0].State != QueueFirstObserved || !strings.Contains(a[0].Finding.Detail, "api:") || !strings.Contains(a[0].Finding.Detail, "proxy:") {
		t.Fatal(a, w.History)
	}
	s.Findings[0].Detail = "caller mutation"
	if strings.Contains(w.History[0].Detail, "caller mutation") {
		t.Fatal("caller changed retained source evidence")
	}
}
