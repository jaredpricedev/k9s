package hubble

import (
	"context"
	"os"
	"testing"
	"time"
)

// Opt-in: scripts/hubble/fixtures.yaml must be running in a disposable Cilium lab.
func TestRelayIntegration(t *testing.T) {
	address := os.Getenv("K9PLUS_HUBBLE_TEST_ADDRESS")
	if address == "" {
		t.Skip("set K9PLUS_HUBBLE_TEST_ADDRESS for real Relay integration")
	}
	for _, tc := range []struct {
		name, pod, filter string
		dropped, external bool
	}{{"allowed", "allowed", "protocol=tcp", false, false}, {"dropped", "denied", "verdict=dropped", true, false}, {"external", "allowed", "ip=1.1.1.1", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := Compile(tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			s := NewSession(Config{Address: address, Plaintext: true}, Scope{Pods: []string{"hubble-test/" + tc.pod}}, q, 10000)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() { s.Run(ctx); close(done) }()
			var events []Event
			for ctx.Err() == nil {
				events, _ = s.Store.Snapshot()
				if len(events) > 0 {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if len(events) == 0 {
				t.Fatalf("no matching events: %+v", s.Status())
			}
			found := false
			for _, e := range events {
				if tc.dropped && e.Verdict != "DROPPED" {
					t.Fatalf("server verdict filter ignored: %+v", e)
				}
				if tc.external && e.Source.IP != "1.1.1.1" && e.Destination.IP != "1.1.1.1" {
					t.Fatalf("server IP filter ignored: %+v", e)
				}
				if e.L7 != "Not reported; L7 visibility unknown" {
					t.Fatalf("fixture unexpectedly has L7: %s", e.L7)
				}
				if tc.dropped || tc.external || e.Verdict == "FORWARDED" {
					found = true
				}
			}
			if !found {
				t.Fatal("expected event absent")
			}
			t.Logf("%d observed events; status=%+v; example=%+v", len(events), s.Status(), events[0])
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("cancel did not close stream")
			}
		})
	}
}
