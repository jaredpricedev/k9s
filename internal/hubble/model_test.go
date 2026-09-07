package hubble

import (
	flow "github.com/cilium/cilium/api/v1/flow"
	"testing"
)

func TestFilterValidation(t *testing.T) {
	for _, s := range []string{"verdict=oops", "port=70000", "protocol=>tcp", "wat=x", "verdict=", "verdict!=dropped", "ip=oops"} {
		if _, err := Compile(s); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
	q, err := Compile("verdict=dropped protocol=tcp port=443")
	if err != nil || q.Filter.GetVerdict()[0] != flow.Verdict_DROPPED {
		t.Fatalf("%+v %v", q, err)
	}
	q, err = Compile("some ordinary text")
	if err != nil || q.Text != "some ordinary text" {
		t.Fatal(q, err)
	}
}
func TestSnapshotSurvivesEviction(t *testing.T) {
	s := NewStore(2)
	s.Add(Event{Verdict: "FORWARDED"})
	s.Add(Event{Verdict: "DROPPED"})
	frozen, _ := s.Snapshot()
	for i := 0; i < 100000; i++ {
		s.Add(Event{Verdict: "FORWARDED"})
	}
	now, evicted := s.Snapshot()
	if len(now) != 2 || evicted != 100000 || frozen[0].ID != 1 || frozen[1].Verdict != "DROPPED" {
		t.Fatal("snapshot or capacity changed")
	}
}
func TestSafeProjectionAndExternalPeers(t *testing.T) {
	f := &flow.Flow{Source: &flow.Endpoint{Namespace: "ns", PodName: "a"}, Destination: &flow.Endpoint{Labels: []string{"reserved:world"}}, IP: &flow.IP{Destination: "1.1.1.1"}, Summary: "secret", L7: &flow.Layer7{Record: &flow.Layer7_Http{Http: &flow.HTTP{Url: "https://secret/?token=x", Headers: []*flow.HTTPHeader{{Key: "Authorization", Value: "secret"}}, Code: 200}}}}
	e := Normalize(f, "live")
	if e.Destination.Kind != "world" || e.Destination.IP != "1.1.1.1" || e.L7 != "HTTP status=200 (payload redacted)" {
		t.Fatalf("%+v", e)
	}
	if e.Source.Pod != "ns/a" {
		t.Fatal(e.Source)
	}
	f.L7 = nil
	if Normalize(f, "live").L7 != "Not reported; L7 visibility unknown" {
		t.Fatal("invented L7 visibility")
	}
}
