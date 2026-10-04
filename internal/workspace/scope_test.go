// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package workspace

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const (
	fixtureWorkspaceName       = "Checkout"
	fixtureSearchName          = "Waiting"
	fixtureChangedValue        = "changed"
	fixtureInvalidSelector     = "app in ("
	fixtureInvalidSelectorCase = "invalid selector"
	fixtureRunningStatus       = "Running"
	fixtureNamespaceCheckout   = "checkout"
	fixturePodGVR              = "v1/pods"
)

func exampleScope() Scope {
	return Scope{
		Name: fixtureWorkspaceName, Context: "dev-cluster", Namespaces: []string{fixtureNamespaceCheckout, "payments"},
		Kinds: []string{podsResourceName, "apps/v1/deployments"}, LabelSelector: "app=checkout",
		Pins:     []ResourceRef{{GVR: fixturePodGVR, Namespace: fixtureNamespaceCheckout, Name: "checkout-abc", UID: "uid-1"}},
		Searches: []SavedSearch{{Name: fixtureSearchName, Query: "Pending"}}, Layout: "queue",
	}
}

func TestNormalizeScopePreservesExplicitScopeAndCopiesSlices(t *testing.T) {
	scope := exampleScope()
	scope.Name = " Checkout "
	scope.Context = " dev-cluster "
	scope.Namespaces = []string{" checkout ", "payments", fixtureNamespaceCheckout}
	scope.Kinds = []string{" Pods ", "apps/v1/deployments", podsResourceName}
	scope.Pins = append(scope.Pins, scope.Pins[0])
	scope.Searches = append(scope.Searches, scope.Searches[0])
	got, err := NormalizeScope(scope)
	if err != nil {
		t.Fatal(err)
	}
	want := exampleScope()
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("normalized scope = %#v, want %#v", got, want)
	}
	got.Namespaces[0] = fixtureChangedValue
	got.Kinds[0] = fixtureChangedValue
	got.Pins[0].Name = fixtureChangedValue
	got.Searches[0].Name = fixtureChangedValue
	if scope.Namespaces[0] != " checkout " || scope.Kinds[0] != " Pods " || scope.Pins[0].Name != "checkout-abc" || scope.Searches[0].Name != fixtureSearchName {
		t.Fatal("normalization mutated caller-owned slices")
	}
}

func TestScopeRejectsInvalidEntriesInsteadOfWidening(t *testing.T) {
	cases := map[string]func(*Scope){
		"no namespace":             func(s *Scope) { s.Namespaces = nil },
		"empty namespace":          func(s *Scope) { s.Namespaces = []string{fixtureNamespaceCheckout, ""} },
		"wildcard namespace":       func(s *Scope) { s.Namespaces = []string{"*"} },
		"all namespace":            func(s *Scope) { s.Namespaces = []string{"ALL"} },
		"invalid namespace":        func(s *Scope) { s.Namespaces = []string{"checkout/payments"} },
		"uppercase namespace":      func(s *Scope) { s.Namespaces = []string{fixtureWorkspaceName} },
		fixtureInvalidSelectorCase: func(s *Scope) { s.LabelSelector = fixtureInvalidSelector },
		"selector control":         func(s *Scope) { s.LabelSelector = "app=check\x1bout" },
		"wildcard kind":            func(s *Scope) { s.Kinds = []string{"*"} },
		"empty kind":               func(s *Scope) { s.Kinds = []string{""} },
		"all kind":                 func(s *Scope) { s.Kinds = []string{"all"} },
		"subresource kind":         func(s *Scope) { s.Kinds = []string{"v1/pods/log"} },
		"empty gvr group":          func(s *Scope) { s.Kinds = []string{"/v1/pods"} },
		"invalid gvr group":        func(s *Scope) { s.Kinds = []string{"group!/v1/pods"} },
		"singular alias":           func(s *Scope) { s.Kinds = []string{"pod"} },
		"missing name":             func(s *Scope) { s.Name = " " },
		"missing context":          func(s *Scope) { s.Context = " " },
		"name control":             func(s *Scope) { s.Name = "Checkout\x1b[31m" },
		"context control":          func(s *Scope) { s.Context = "dev\ncluster" },
		"edge context control":     func(s *Scope) { s.Context = "dev-cluster\n" },
		"edge namespace control":   func(s *Scope) { s.Namespaces[0] = "checkout\n" },
		"edge selector control":    func(s *Scope) { s.LabelSelector = "app=checkout\n" },
		"long name":                func(s *Scope) { s.Name = strings.Repeat("n", 97) },
		"long context":             func(s *Scope) { s.Context = strings.Repeat("c", 513) },
		"outside pin":              func(s *Scope) { s.Pins[0].Namespace = "kube-system" },
		"empty pin ns":             func(s *Scope) { s.Pins[0].Namespace = "" },
		"alias pin":                func(s *Scope) { s.Pins[0].GVR = podsResourceName },
		"invalid pin name":         func(s *Scope) { s.Pins[0].Name = "pod/child" },
		"control pin uid":          func(s *Scope) { s.Pins[0].UID = "uid\x00" },
		"long search":              func(s *Scope) { s.Searches[0].Query = strings.Repeat("q", 1025) },
		"empty search":             func(s *Scope) { s.Searches[0].Query = " " },
		"unknown layout":           func(s *Scope) { s.Layout = "all-clusters" },
		"conflicting pin":          func(s *Scope) { pin := s.Pins[0]; pin.UID = "replacement"; s.Pins = append(s.Pins, pin) },
		"conflicting search": func(s *Scope) {
			s.Searches = append(s.Searches, SavedSearch{Name: fixtureSearchName, Query: fixtureRunningStatus})
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			scope := exampleScope()
			change(&scope)
			if err := ValidateScope(scope); err == nil {
				t.Fatalf("invalid scope accepted: %#v", scope)
			}
		})
	}
}

func TestScopeCountBounds(t *testing.T) {
	for _, count := range []int{16, 17} {
		scope := exampleScope()
		scope.Pins = nil
		scope.Namespaces = nil
		for i := range count {
			scope.Namespaces = append(scope.Namespaces, fmt.Sprintf("namespace-%d", i))
		}
		if err := ValidateScope(scope); (err != nil) != (count > 16) {
			t.Fatalf("%d namespaces: %v", count, err)
		}
	}
	for _, count := range []int{12, 13} {
		scope := exampleScope()
		scope.Kinds = nil
		for i := range count {
			scope.Kinds = append(scope.Kinds, fmt.Sprintf("resources%d", i)+"s")
		}
		if err := ValidateScope(scope); (err != nil) != (count > 12) {
			t.Fatalf("%d kinds: %v", count, err)
		}
	}
	scope := exampleScope()
	scope.Pins = make([]ResourceRef, 65)
	if err := ValidateScope(scope); err == nil {
		t.Fatal("65 pins accepted")
	}
	scope = exampleScope()
	scope.Searches = make([]SavedSearch, 33)
	if err := ValidateScope(scope); err == nil {
		t.Fatal("33 searches accepted")
	}
}

func TestScopeAllowsLocalFiltersAndEmptyUID(t *testing.T) {
	scope := exampleScope()
	scope.Context = "arn:aws:eks:us-east-1:123456789012:cluster/checkout"
	scope.Kinds = nil
	scope.LabelSelector = "environment in (dev,test),!paused"
	scope.Pins[0].UID = ""
	for _, layout := range []string{"", "queue", "inventory", "pins", "coverage"} {
		scope.Layout = layout
		if err := ValidateScope(scope); err != nil {
			t.Fatal(err)
		}
	}
	scope.LabelSelector = ""
	got, err := NormalizeScope(scope)
	if err != nil || !reflect.DeepEqual(got.Namespaces, scope.Namespaces) {
		t.Fatalf("empty selector changed explicit namespaces: %#v %v", got, err)
	}
}
