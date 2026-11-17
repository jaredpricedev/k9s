// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package observability

import (
	"errors"
	"fmt"
	"regexp"
)

// RED maps the request, error and duration signals of HTTP workloads onto
// the metric and label names a cluster actually uses. It is stored under
// k9s.observability.red in the context configuration; every field is
// optional and an empty field keeps its default. Declaring it contacts
// nothing, and the names only ever fill fixed query templates.
type RED struct {
	// Requests is the request counter. Default http_requests_total.
	Requests string `json:"requests,omitempty" yaml:"requests,omitempty"`
	// CodeLabel carries the response status on Requests. Default code.
	CodeLabel string `json:"codeLabel,omitempty" yaml:"codeLabel,omitempty"`
	// ErrorCodes is the regular expression on CodeLabel that counts as an
	// error. Default 5.. (server errors).
	ErrorCodes string `json:"errorCodes,omitempty" yaml:"errorCodes,omitempty"`
	// DurationBuckets is the request duration histogram, in seconds, with
	// an le label. Default http_request_duration_seconds_bucket.
	DurationBuckets string `json:"durationBuckets,omitempty" yaml:"durationBuckets,omitempty"`
	// NamespaceLabel carries the Kubernetes namespace. Default namespace.
	NamespaceLabel string `json:"namespaceLabel,omitempty" yaml:"namespaceLabel,omitempty"`
	// WorkloadLabel carries the workload name (for example service,
	// deployment, job or app). Empty, the default, selects series by the
	// pod label and the workload's pod-name pattern instead.
	WorkloadLabel string `json:"workloadLabel,omitempty" yaml:"workloadLabel,omitempty"`
	// PodLabel carries the pod name. Default pod.
	PodLabel string `json:"podLabel,omitempty" yaml:"podLabel,omitempty"`
}

// Default RED names, as exposed by the common Prometheus client libraries
// and attached by the usual Kubernetes service discovery.
const (
	DefaultRequests        = "http_requests_total"
	DefaultCodeLabel       = "code"
	DefaultErrorCodes      = "5.."
	DefaultDurationBuckets = "http_request_duration_seconds_bucket"
	DefaultNamespaceLabel  = "namespace"
	DefaultPodLabel        = "pod"
)

// WithDefaults returns the mapping with every empty field filled in. A nil
// receiver yields the defaults.
func (r *RED) WithDefaults() RED {
	var out RED
	if r != nil {
		out = *r
	}
	for _, f := range []struct {
		field    *string
		fallback string
	}{
		{&out.Requests, DefaultRequests}, {&out.CodeLabel, DefaultCodeLabel}, {&out.ErrorCodes, DefaultErrorCodes},
		{&out.DurationBuckets, DefaultDurationBuckets}, {&out.NamespaceLabel, DefaultNamespaceLabel}, {&out.PodLabel, DefaultPodLabel},
	} {
		if *f.field == "" {
			*f.field = f.fallback
		}
	}
	return out
}

// Validate reports why the mapping cannot fill a query template, or nil.
func (r *RED) Validate() error {
	m := r.WithDefaults()
	for _, name := range []string{m.Requests, m.DurationBuckets} {
		if !metricName.MatchString(name) {
			return fmt.Errorf("red: invalid metric name %q", Clean(name, 80))
		}
	}
	for _, name := range []string{m.CodeLabel, m.NamespaceLabel, m.PodLabel, m.WorkloadLabel} {
		if name != "" && !labelName.MatchString(name) {
			return fmt.Errorf("red: invalid label name %q", Clean(name, 80))
		}
	}
	if len(m.ErrorCodes) > 64 {
		return errors.New("red: errorCodes exceeds 64 characters")
	}
	if _, err := regexp.Compile(m.ErrorCodes); err != nil {
		return errors.New("red: errorCodes is not a regular expression")
	}
	return nil
}
