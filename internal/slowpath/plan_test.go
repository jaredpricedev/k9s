// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.

package slowpath_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/derailed/k9s/internal/observability"
	"github.com/derailed/k9s/internal/slowpath"
	"github.com/derailed/k9s/internal/slowpath/slowtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOneBatchResolvesEveryHopInPathOrder(t *testing.T) {
	plan := slowpath.BuildPlan(slowtest.Sources(), slowtest.Path(), time.Hour, slowtest.Now.Add(17*time.Second))
	assert.Equal(t, slowtest.Now, plan.End, "the window ends on a sample time")
	assert.Equal(t, slowtest.Now.Add(-time.Hour), plan.Start)
	assert.Equal(t, 30*time.Second, plan.Step)
	assert.Equal(t, 2*time.Minute, plan.RateWindow, "the rate window :graph and :impact use for this step")

	var labels []string
	for i := range plan.Hops {
		labels = append(labels, plan.Hops[i].Label())
		assert.Empty(t, plan.Hops[i].NotRead, plan.Hops[i].ID)
	}
	assert.Equal(t, []string{"client → ingress", "ingress → service", "service → pods", "pods → postgresql", "pods → redis-master", "pods → gitaly", "dns", "network"}, labels)

	require.Len(t, plan.Queries, 35)
	assert.LessOrEqual(t, len(plan.Queries), slowpath.MaxQueries)
	perHop := map[string][]string{}
	text := map[string]string{}
	for i := range plan.Queries {
		query := &plan.Queries[i]
		hop := plan.Hops[query.HopIndex].ID
		perHop[hop] = append(perHop[hop], query.Signal.Name)
		text[hop+"/"+query.Signal.Name] = query.Sent.String()
		require.NoError(t, query.Sent.Err(), query.Signal.Name)
		assert.Equal(t, hop == slowpath.HopApp || hop == slowpath.HopService || hop == slowpath.HopNetwork ||
			(hop == slowpath.HopDNS && query.Signal.Name == "dns-errors"), query.PerPod, "%s/%s", hop, query.Signal.Name)
	}
	assert.Equal(t, []string{"requests", "499", "5xx", "p99", "upstream-p99"}, perHop[slowpath.HopIngress])
	assert.Equal(t, []string{slowpath.SignalReady}, perHop[slowpath.HopService])
	assert.Equal(t, []string{"p99", "requests", "5xx", "queued", "free-threads", "busy-threads", "threads", "throttled", "cpu", slowpath.SignalMemory, slowpath.SignalRestarts},
		perHop[slowpath.HopApp])
	assert.Equal(t, []string{"p99", "servfail", "nxdomain", "requests", "dns-errors"}, perHop[slowpath.HopDNS],
		"the Hubble DNS signal is read with the DNS hop")
	assert.Equal(t, []string{"drops", "rst", "flows"}, perHop[slowpath.HopNetwork])

	pods := `pod=~"gitlab-webservice-default-6f9c7d5b84-2bq7k|gitlab-webservice-default-6f9c7d5b84-8wzpd|gitlab-webservice-default-6f9c7d5b84-hx4mt"`
	assert.Equal(t, `sum(rate(nginx_ingress_controller_requests{namespace="gitlab",service="gitlab-webservice-default",ingress="gitlab-webservice-default",status="499"}[120s]))`,
		text["ingress/499"], "pinned to the one Ingress that routes here")
	assert.Equal(t, `histogram_quantile(0.99, sum by (le,pod) (rate(http_request_duration_seconds_bucket{namespace="gitlab",`+pods+`}[120s])))`, text["app/p99"])
	assert.Equal(t, `min by (pod) (kube_pod_status_ready{namespace="gitlab",`+pods+`,condition="true"})`, text["service/ready"])
	assert.Equal(t, `sum by (pod) (kube_pod_container_status_restarts_total{namespace="gitlab",`+pods+`})`, text["app/restarts"])
	assert.Contains(t, text["app/memory"], `max by (pod,container) (kube_pod_container_resource_limits{namespace="gitlab",`+pods+`,resource="memory"})`)
	assert.Equal(t, `sum by (destination_pod,reason) (rate(hubble_drop_total{destination_namespace="gitlab",destination_`+pods+`}[120s]))`, text["network/drops"])
	assert.Equal(t, `min(pg_up{namespace="gitlab",pod="gitlab-postgresql-0"})`, text["dep:gitlab/gitlab-postgresql/up"],
		"a dependency is pinned to the Pods its EndpointSlices name")
	for _, query := range text {
		assert.NotContains(t, query, "$")
	}
}

func TestThePlanSaysWhatCannotBeReadAndStaysBounded(t *testing.T) {
	path := slowtest.Path()
	path.Ingresses, path.Pods, path.Endpoints, path.EndpointsReady = nil, nil, 0, 0
	plan := slowpath.BuildPlan(nil, path, 0, slowtest.Now)
	assert.Equal(t, slowpath.DefaultWindow, plan.Window)
	reasons := map[string]string{}
	for i := range plan.Hops {
		if len(plan.Hops[i].NotRead) > 0 {
			reasons[plan.Hops[i].ID] = plan.Hops[i].NotRead[0].Reason
		}
	}
	assert.Equal(t, "no Ingress in namespace gitlab routes to Service gitlab-webservice-default; HTTPRoutes and Ingresses in other namespaces are not read here",
		reasons[slowpath.HopIngress])
	assert.Equal(t, "no Pod is known for this object, and the app hop is read per Pod", reasons[slowpath.HopApp])
	assert.Equal(t, "no Pod is known for this object, and the network hop is read per Pod", reasons[slowpath.HopNetwork])
	assert.Contains(t, reasons[slowpath.HopDependencies], "no dependency is declared for gitlab/gitlab-webservice-default")
	for i := range plan.Queries {
		assert.Equal(t, slowpath.HopDNS, plan.Hops[plan.Queries[i].HopIndex].ID, "only the cluster DNS can be read without Pods")
	}

	// Two Ingresses: the profile is not pinned to one of them.
	path = slowtest.Path()
	path.Ingresses = append(path.Ingresses, slowpath.Ingress{Name: "gitlab-extra", Entry: "extra.example.test /"})
	plan = slowpath.BuildPlan(slowtest.Sources(), path, time.Hour, slowtest.Now)
	assert.Equal(t, "2 Ingresses", plan.Hops[0].What)
	assert.NotContains(t, plan.Queries[0].Sent.String(), "ingress=")

	// Sixteen dependencies of four signals each do not fit one batch: the
	// rest is listed as not read instead of being sent.
	sources := slowtest.Sources()
	entry := sources.RequestPath.Services["gitlab/gitlab-webservice-default"]
	entry.Dependencies = nil
	for i := range observability.MaxDependencies {
		entry.Dependencies = append(entry.Dependencies, observability.Dependency{Service: fmt.Sprintf("gitlab/pg-%d", i), Profile: "postgres-exporter"})
	}
	sources.RequestPath.Services["gitlab/gitlab-webservice-default"] = entry
	plan = slowpath.BuildPlan(sources, slowtest.Path(), time.Hour, slowtest.Now)
	assert.Len(t, plan.Queries, slowpath.MaxQueries)
	skipped := 0
	for i := range plan.Hops {
		for _, missing := range plan.Hops[i].NotRead {
			if strings.Contains(missing.Reason, "over the limit of 48 range reads per batch") {
				skipped++
			}
		}
	}
	assert.Positive(t, skipped)
}

func TestTheBatchIsSentOnceAFewAtATimeAndStopsWhenCanceled(t *testing.T) {
	plan := slowpath.BuildPlan(slowtest.Sources(), slowtest.Path(), time.Hour, slowtest.Now)
	story := &slowtest.Story{}
	results := slowpath.Collect(t.Context(), story, plan)
	require.Len(t, results, len(plan.Queries))
	assert.EqualValues(t, len(plan.Queries), story.Calls.Load(), "every query is asked exactly once")
	for i := range results {
		assert.Equal(t, plan.Queries[i].Sent.String(), results[i].Query)
		assert.Equal(t, plan.Start, results[i].Start)
		assert.Equal(t, plan.End, results[i].End)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	unsent := &slowtest.Story{}
	results = slowpath.Collect(ctx, unsent, plan)
	assert.Zero(t, unsent.Calls.Load(), "a canceled batch sends nothing")
	for i := range results {
		assert.Equal(t, observability.StateUnavailable, results[i].State)
		assert.Equal(t, "canceled", results[i].Reason)
	}
	report := slowpath.Evaluate(plan, results, nil, slowtest.Now)
	assert.Equal(t, "unavailable: canceled", report.Failure)
}
