// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of K9s

package dao

import (
	"bytes"
	"fmt"
	"maps"
	"time"

	"github.com/derailed/k9s/internal/client"
	"github.com/derailed/k9s/internal/logstream"
	"github.com/derailed/tview"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LogOptions represents logger options.
type LogOptions struct {
	Context, Cluster, WorkloadKind, WorkloadName string
	Labels, Annotations                          map[string]string
	Source                                       logstream.Source
	Events                                       bool
	CreateDuration                               time.Duration
	Path                                         string
	Container                                    string
	DefaultContainer                             string
	SinceTime                                    string
	Lines                                        int64
	SinceSeconds                                 int64
	Head                                         bool
	Previous                                     bool
	SingleContainer                              bool
	MultiPods                                    bool
	ShowTimestamp                                bool
	AllContainers                                bool
	LogBufferSize                                int
}

// Info returns the option pod and container info.
func (o *LogOptions) Info() string {
	if o.Container != "" {
		return fmt.Sprintf("%s (%s)", o.Path, o.Container)
	}

	return o.Path
}

// Clone clones options.
func (o *LogOptions) Clone() *LogOptions {
	clone := *o
	clone.Labels, clone.Annotations = maps.Clone(o.Labels), maps.Clone(o.Annotations)
	return &clone
}

// HasContainer checks if a container is present.
func (o *LogOptions) HasContainer() bool {
	return o.Container != ""
}

// ToggleAllContainers toggles single or all-containers if possible.
func (o *LogOptions) ToggleAllContainers() {
	if o.SingleContainer {
		return
	}
	o.AllContainers = !o.AllContainers
	if o.AllContainers {
		o.DefaultContainer, o.Container = o.Container, ""
		return
	}

	if o.DefaultContainer != "" {
		o.Container = o.DefaultContainer
	}
}

// ToPodLogOptions returns pod log options.
func (o *LogOptions) ToPodLogOptions() *v1.PodLogOptions {
	opts := v1.PodLogOptions{
		Follow:     !o.Previous,
		Timestamps: true,
		Container:  o.Container,
		Previous:   o.Previous,
		TailLines:  &o.Lines,
	}
	if o.Head {
		var maxBytes int64 = 5000
		opts.Follow = false
		opts.TailLines, opts.SinceSeconds, opts.SinceTime = nil, nil, nil
		opts.LimitBytes = &maxBytes
		return &opts
	}
	if o.SinceSeconds < 0 {
		return &opts
	}

	if o.SinceSeconds != 0 {
		opts.SinceSeconds, opts.SinceTime = &o.SinceSeconds, nil
		return &opts
	}

	if o.SinceTime == "" {
		return &opts
	}
	if t, err := time.Parse(time.RFC3339, o.SinceTime); err == nil {
		opts.SinceTime = &metav1.Time{Time: t}
	}

	return &opts
}

// ToLogItem add a log header to display po/co information along with the log message.
func (o *LogOptions) ToLogItem(data []byte) *LogItem {
	raw := bytes.TrimSuffix(data, []byte{'\n'})
	var runtimeTime time.Time
	if i := bytes.IndexByte(raw, ' '); i > 0 {
		if t, err := time.Parse(time.RFC3339Nano, string(raw[:i])); err == nil {
			runtimeTime, raw = t, raw[i+1:]
		}
	}
	ns, pod := client.Namespaced(o.Path)
	source := o.Source
	source.Context, source.Cluster = o.Context, o.Cluster
	source.Namespace, source.Pod, source.Container = ns, pod, o.Container
	item := &LogItem{
		Raw: bytes.Clone(raw), Source: source, RuntimeTime: runtimeTime, SingleContainer: o.SingleContainer,
		Container: o.Container, Labels: maps.Clone(o.Labels), Annotations: maps.Clone(o.Annotations),
	}
	if o.MultiPods {
		item.Pod = pod
	}
	item.setDisplay(logstream.SafeEntry(item.Entry()))
	return item
}

//nolint:gocritic // The derived display value is intentionally isolated from the retained entry.
func (l *LogItem) setDisplay(e logstream.Entry) {
	prefix := ""
	if !l.RuntimeTime.IsZero() {
		prefix = l.RuntimeTime.Format(time.RFC3339Nano) + " "
	}
	l.Bytes = []byte(prefix + tview.Escape(logstream.Sanitize(e.Raw)) + "\n")
}

func (o *LogOptions) ToErrLogItem(err error) *LogItem {
	item := o.ToLogItem([]byte(time.Now().UTC().Format(time.RFC3339Nano) + " " + err.Error()))
	item.IsError = true
	item.Marker = &logstream.Marker{Kind: "error", Origin: "collector", Time: item.RuntimeTime, Message: err.Error()}
	return item
}
