// SPDX-License-Identifier: Apache-2.0
// Modified for k9+; see NOTICE.
package hubble

import (
	"encoding/json"
	"testing"

	flow "github.com/cilium/cilium/api/v1/flow"
	"github.com/stretchr/testify/require"
)

func TestDNSProjectionIsBoundedAndImmutableWithoutEnablingL7(t *testing.T) {
	dns := &flow.DNS{Query: "api.apps.svc.example.test", Ips: []string{"10.0.0.1"}, Cnames: []string{"alias.example.test"}}
	f := &flow.Flow{Summary: "PRIVATE-RAW-SUMMARY", L7: &flow.Layer7{LatencyNs: 500000, Record: &flow.Layer7_Dns{Dns: dns}}}
	e := Normalize(f, "retained window")
	require.True(t, e.DNS.Reported)
	require.Equal(t, uint64(500000), e.DNS.LatencyNs)
	store := NewStore(1)
	store.Add(e)
	frozen, _ := store.Snapshot()
	e.DNS.Query = "mutated caller"
	dns.Ips[0] = "mutated payload"
	current, _ := store.Snapshot()
	require.Equal(t, frozen[0].DNS, current[0].DNS)
	raw, err := json.Marshal(current)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "PRIVATE-RAW-SUMMARY")
	require.Contains(t, string(raw), "api.apps.svc.example.test")
	empty := Normalize(&flow.Flow{}, "live")
	require.False(t, empty.DNS.Reported)
	require.Zero(t, empty.Time, "missing source time must not become an epoch timestamp")
}
