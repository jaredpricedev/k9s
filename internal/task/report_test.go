// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package task

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/inspect"
	"github.com/derailed/k9s/internal/workspace"
	"github.com/stretchr/testify/require"
)

const reportTestContext = "demo"
const reportTestNS = "apps"

func TestWorkspacePartialEvidenceNeverBecomesHealth(t *testing.T) {
	scope := workspace.Scope{Context: reportTestContext, Namespaces: []string{reportTestNS}, Kinds: []string{"Pod"}, LabelSelector: "app=api"}
	r := Workspace(scope, workspace.Snapshot{ObservedAt: time.Now(), Coverage: []workspace.Coverage{
		{GVR: "v1/pods", Namespace: reportTestNS, State: "denied"},
	}})
	require.False(t, r.Complete)
	require.Contains(t, r.Facts[0].State, "collected evidence")
	require.Equal(t, "app=api", r.Selector)
	r = Workspace(scope, workspace.Snapshot{Coverage: []workspace.Coverage{{State: "complete", Truncated: true}}})
	require.False(t, r.Complete)
}

func TestReportOutputIsPrivateSanitizedAndOrdered(t *testing.T) {
	id := inspect.ResourceIdentity{Context: reportTestContext, GVR: "v1/pods", Namespace: reportTestNS, Name: "api", UID: "pod-uid"}
	bundle := inspect.NewBundle([]inspect.Observation{{Identity: id, Source: "retained API", ObservedAt: time.Now(), State: inspect.ObservationComplete,
		Object: map[string]any{"kind": "Pod", "metadata": map[string]any{"name": "api"}}}})
	r, err := Evidence(bundle)
	require.NoError(t, err)
	r.Facts[0].Detail = "Bearer private-token\x1b[2J observed"
	r.Facts[0].Resource = &id
	r.Context = "demo\x1b[31m"
	r.Namespaces = []string{"apps\x1b[2J"}
	original, err := json.Marshal(r)
	require.NoError(t, err)
	for _, format := range []string{"text", "json"} {
		var out bytes.Buffer
		require.NoError(t, Write(&out, r, format))
		for _, forbidden := range []string{"private-token", "\x1b", "\\u001b"} {
			require.NotContains(t, out.String(), forbidden)
		}
		require.Contains(t, out.String(), "pod-uid")
		if format == "text" {
			text := out.String()
			require.Less(t, strings.Index(text, "Context:"), strings.Index(text, "Coverage"))
			require.Less(t, strings.Index(text, "Coverage"), strings.Index(text, "Observed facts"))
		}
	}
	after, err := json.Marshal(r)
	require.NoError(t, err)
	require.Equal(t, original, after, "output must not mutate retained source data")
}

func TestEvidenceSecretExclusionAndOutputBound(t *testing.T) {
	b := inspect.NewBundle([]inspect.Observation{{Identity: inspect.ResourceIdentity{Context: reportTestContext, GVR: "v1/secrets", Namespace: reportTestNS, Name: "tls"},
		Source: "retained source", ObservedAt: time.Now(), State: inspect.ObservationComplete,
		Object: map[string]any{"kind": "Secret", "data": map[string]any{"password": "private-secret"}}}})
	r, err := Evidence(b)
	require.NoError(t, err)
	require.False(t, r.Complete)
	var out bytes.Buffer
	require.NoError(t, Write(&out, r, "json"))
	require.NotContains(t, out.String(), "private-secret")
	direct := Report{Version: Version, Complete: true, Evidence: &b}
	out.Reset()
	require.NoError(t, Write(&out, direct, "json"))
	var decoded Report
	require.NoError(t, json.Unmarshal(out.Bytes(), &decoded))
	require.False(t, decoded.Complete, "excluded Secret evidence cannot remain complete")
	require.True(t, direct.Complete, "output must not change the retained report")
	out.Reset()
	r.Evidence = nil
	r.Facts = []Fact{{Detail: strings.Repeat("x", MaxReportBytes+1)}}
	require.Error(t, Write(&out, r, "text"))
	require.Empty(t, out.String(), "oversized reports must not partially write")
}
