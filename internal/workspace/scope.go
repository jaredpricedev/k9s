// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

// Package workspace defines explicitly scoped, saved daily Kubernetes workspaces.
package workspace

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	maxNamespaces = 16
	maxKinds      = 12
	maxPins       = 64
	maxSearches   = 32
)

// Scope keeps a workspace tied to one context and an explicit namespace subset.
// Empty kinds use the collector's default set. An empty selector includes every
// label within the selected namespaces, never every namespace.
type Scope struct {
	Name          string        `yaml:"name"`
	Context       string        `yaml:"context"`
	Namespaces    []string      `yaml:"namespaces"`
	Kinds         []string      `yaml:"kinds,omitempty"`
	LabelSelector string        `yaml:"labelSelector,omitempty"`
	Pins          []ResourceRef `yaml:"pins,omitempty"`
	Searches      []SavedSearch `yaml:"searches,omitempty"`
	Layout        string        `yaml:"layout,omitempty"`
}

// ResourceRef includes an optional UID so a replacement object can be detected.
// GVR uses the same slash form as k9s: v1/pods or apps/v1/deployments.
type ResourceRef struct {
	GVR       string `yaml:"gvr"`
	Namespace string `yaml:"namespace"`
	Name      string `yaml:"name"`
	UID       string `yaml:"uid,omitempty"`
}

// SavedSearch is a named, literal workspace filter; it does not alter scope.
type SavedSearch struct {
	Name  string `yaml:"name"`
	Query string `yaml:"query"`
}

// ValidateScope checks the same invariants as NormalizeScope without modifying
// the caller's slices or silently repairing invalid namespace selections.
//
//nolint:gocritic // Public validation accepts an immutable scope value and leaves caller-owned slices untouched.
func ValidateScope(scope Scope) error {
	_, err := NormalizeScope(scope)
	return err
}

// NormalizeScope trims fields and deduplicates selections while preserving their
// order. Invalid entries are rejected, never dropped to broaden a selection.
//
//nolint:gocritic // The public API normalizes an immutable scope value into independent result slices.
func NormalizeScope(scope Scope) (Scope, error) {
	for _, field := range []struct {
		name, value string
		limit       int
	}{
		{"workspace name", scope.Name, 96}, {"context", scope.Context, 512},
		{"label selector", scope.LabelSelector, 2048}, {"workspace layout", scope.Layout, 32},
	} {
		if err := validateBoundedText(field.name, field.value, field.limit, false); err != nil {
			return Scope{}, err
		}
	}
	out := scope
	out.Name = strings.TrimSpace(scope.Name)
	out.Context = strings.TrimSpace(scope.Context)
	out.LabelSelector = strings.TrimSpace(scope.LabelSelector)
	out.Layout = strings.TrimSpace(scope.Layout)
	if err := validateBoundedText("workspace name", out.Name, 96, true); err != nil {
		return Scope{}, err
	}
	if err := validateBoundedText("context", out.Context, 512, true); err != nil {
		return Scope{}, err
	}
	var err error
	out.Namespaces, err = normalizeNamespaces(scope.Namespaces)
	if err != nil {
		return Scope{}, err
	}
	if validationErr := validateBoundedText("label selector", out.LabelSelector, 2048, false); validationErr != nil {
		return Scope{}, validationErr
	}
	selector, err := labels.Parse(out.LabelSelector)
	if err != nil {
		return Scope{}, fmt.Errorf("invalid label selector: %w", err)
	}
	out.LabelSelector = selector.String()
	out.Kinds, err = normalizeKinds(scope.Kinds)
	if err != nil {
		return Scope{}, err
	}
	out.Pins, err = normalizePins(scope.Pins, out.Namespaces)
	if err != nil {
		return Scope{}, err
	}
	out.Searches, err = normalizeSearches(scope.Searches)
	if err != nil {
		return Scope{}, err
	}
	switch out.Layout {
	case "", "queue", "inventory", "pins", "coverage", categoryHistory, "activity":
	default:
		return Scope{}, fmt.Errorf("unknown workspace layout %q", out.Layout)
	}
	return out, nil
}

func normalizeNamespaces(values []string) ([]string, error) {
	if len(values) == 0 || len(values) > 256 {
		return nil, fmt.Errorf("select 1–%d explicit namespaces", maxNamespaces)
	}
	var out []string
	namespaces := make(map[string]bool, len(values))
	for _, raw := range values {
		if err := validateBoundedText("namespace", raw, 128, false); err != nil {
			return nil, err
		}
		ns := strings.TrimSpace(raw)
		if ns == "" || ns == "*" || strings.EqualFold(ns, "all") {
			return nil, fmt.Errorf("namespace %q must be an explicit namespace, not an all-namespace selection", raw)
		}
		if problems := validation.IsDNS1123Label(ns); len(problems) != 0 {
			return nil, fmt.Errorf("invalid namespace %q: %s", raw, strings.Join(problems, "; "))
		}
		if !namespaces[ns] {
			out = append(out, ns)
			namespaces[ns] = true
		}
	}
	if len(out) > maxNamespaces {
		return nil, fmt.Errorf("at most %d namespaces are allowed", maxNamespaces)
	}
	return out, nil
}

func normalizeKinds(values []string) ([]string, error) {
	if len(values) > 256 {
		return nil, fmt.Errorf("at most %d resource kinds are allowed", maxKinds)
	}
	var out []string
	kinds := make(map[string]bool, len(values))
	for _, raw := range values {
		if err := validateBoundedText("resource kind", raw, 320, false); err != nil {
			return nil, err
		}
		kind := strings.ToLower(strings.TrimSpace(raw))
		if err := validateResourceType(kind, true); err != nil {
			return nil, fmt.Errorf("invalid resource kind %q: %w", raw, err)
		}
		if !kinds[kind] {
			out = append(out, kind)
			kinds[kind] = true
		}
	}
	if len(out) > maxKinds {
		return nil, fmt.Errorf("at most %d resource kinds are allowed", maxKinds)
	}
	return out, nil
}

func normalizePins(values []ResourceRef, namespaces []string) ([]ResourceRef, error) {
	if len(values) > maxPins {
		return nil, fmt.Errorf("at most %d pinned resources are allowed", maxPins)
	}
	var out []ResourceRef
	pins := make(map[string]ResourceRef, len(values))
	for _, raw := range values {
		for _, field := range []struct {
			name, value string
			limit       int
		}{
			{"pinned GVR", raw.GVR, 320}, {"pinned namespace", raw.Namespace, 128},
			{"pinned resource name", raw.Name, 253}, {"pinned resource UID", raw.UID, 128},
		} {
			if err := validateBoundedText(field.name, field.value, field.limit, false); err != nil {
				return nil, err
			}
		}
		pin := ResourceRef{
			GVR: strings.ToLower(strings.TrimSpace(raw.GVR)), Namespace: strings.TrimSpace(raw.Namespace),
			Name: strings.TrimSpace(raw.Name), UID: strings.TrimSpace(raw.UID),
		}
		if err := validateResourceType(pin.GVR, false); err != nil {
			return nil, fmt.Errorf("invalid pinned GVR %q: %w", raw.GVR, err)
		}
		if !slices.Contains(namespaces, pin.Namespace) {
			return nil, fmt.Errorf("pinned namespace %q is outside the selected scope", pin.Namespace)
		}
		if problems := validation.IsDNS1123Subdomain(pin.Name); len(problems) != 0 {
			return nil, fmt.Errorf("invalid pinned resource name %q: %s", raw.Name, strings.Join(problems, "; "))
		}
		if err := validateBoundedText("pinned resource UID", pin.UID, 128, false); err != nil {
			return nil, err
		}
		key := pin.GVR + "/" + pin.Namespace + "/" + pin.Name
		if previous, exists := pins[key]; exists {
			if previous.UID != pin.UID {
				return nil, fmt.Errorf("pinned resource %q has conflicting UIDs", key)
			}
			continue
		}
		pins[key] = pin
		out = append(out, pin)
	}
	return out, nil
}

func normalizeSearches(values []SavedSearch) ([]SavedSearch, error) {
	if len(values) > maxSearches {
		return nil, fmt.Errorf("at most %d saved searches are allowed", maxSearches)
	}
	var out []SavedSearch
	searches := make(map[string]string, len(values))
	for _, raw := range values {
		if err := validateBoundedText("saved search name", raw.Name, 96, false); err != nil {
			return nil, err
		}
		if err := validateBoundedText("saved search query", raw.Query, 1024, false); err != nil {
			return nil, err
		}
		search := SavedSearch{Name: strings.TrimSpace(raw.Name), Query: strings.TrimSpace(raw.Query)}
		if err := validateBoundedText("saved search name", search.Name, 96, true); err != nil {
			return nil, err
		}
		if err := validateBoundedText("saved search query", search.Query, 1024, true); err != nil {
			return nil, err
		}
		if previous, exists := searches[search.Name]; exists {
			if previous != search.Query {
				return nil, fmt.Errorf("saved search %q has conflicting queries", search.Name)
			}
			continue
		}
		searches[search.Name] = search.Query
		out = append(out, search)
	}
	return out, nil
}

var (
	resourcePart   = regexp.MustCompile(`^[a-z][a-z0-9]*$`)
	apiVersionPart = regexp.MustCompile(`^v\d+(?:(?:alpha|beta)\d+)?$`)
)

func validateResourceType(value string, allowAlias bool) error {
	if value == "" || len(value) > 320 {
		return fmt.Errorf("use a plural resource name or full group/version/resource")
	}
	parts := strings.Split(value, "/")
	if len(parts) == 1 {
		if !allowAlias || value == "all" || !strings.HasSuffix(value, "s") || len(value) > 63 || !resourcePart.MatchString(value) {
			return fmt.Errorf("use a plural resource alias or full GVR")
		}
		return nil
	}
	if len(parts) != 2 && len(parts) != 3 {
		return fmt.Errorf("GVR must be version/resource or group/version/resource")
	}
	if len(parts) == 3 {
		if problems := validation.IsDNS1123Subdomain(parts[0]); len(problems) != 0 {
			return fmt.Errorf("invalid API group")
		}
	}
	version, resource := parts[len(parts)-2], parts[len(parts)-1]
	if len(version) > 63 || !apiVersionPart.MatchString(version) || len(resource) > 63 || !resourcePart.MatchString(resource) {
		return fmt.Errorf("invalid GVR version or resource (subresources are not supported)")
	}
	return nil
}

func validateBoundedText(field, value string, limit int, required bool) error {
	if required && value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if len(value) > limit || !utf8.ValidString(value) {
		return fmt.Errorf("%s exceeds %d bytes or contains invalid text", field, limit)
	}
	for _, r := range value {
		if unicode.IsControl(r) || !unicode.IsPrint(r) {
			return fmt.Errorf("%s contains a non-printable character", field)
		}
	}
	return nil
}
