package hubble

import (
	"context"
	"net"
	"testing"
	"time"

	flow "github.com/cilium/cilium/api/v1/flow"
	observer "github.com/cilium/cilium/api/v1/observer"
	"google.golang.org/grpc"
)

type testObserver struct {
	observer.UnimplementedObserverServer
}

func (testObserver) ServerStatus(context.Context, *observer.ServerStatusRequest) (*observer.ServerStatusResponse, error) {
	return &observer.ServerStatusResponse{Version: "test"}, nil
}
func (testObserver) GetNodes(context.Context, *observer.GetNodesRequest) (*observer.GetNodesResponse, error) {
	return &observer.GetNodesResponse{}, nil
}
func (testObserver) GetFlows(r *observer.GetFlowsRequest, s observer.Observer_GetFlowsServer) error {
	if !r.Follow {
		return s.Send(&observer.GetFlowsResponse{ResponseTypes: &observer.GetFlowsResponse_Flow{Flow: &flow.Flow{Source: &flow.Endpoint{Namespace: "ns", PodName: "a"}}}})
	}
	if err := s.Send(&observer.GetFlowsResponse{ResponseTypes: &observer.GetFlowsResponse_LostEvents{LostEvents: &flow.LostEvent{NumEventsLost: 7}}}); err != nil {
		return err
	}
	<-s.Context().Done()
	return s.Context().Err()
}
func TestNativeStreamLossAndCancellation(t *testing.T) {
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	observer.RegisterObserverServer(srv, testObserver{})
	go func() { _ = srv.Serve(l) }()
	defer srv.Stop()
	s := NewSession(Config{Address: l.Addr().String(), Plaintext: true}, Scope{Pods: []string{"ns/a"}}, Query{}, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s.Status().Lost == 7 {
			break
		}
		time.Sleep(time.Millisecond * 10)
	}
	if s.Status().Lost != 7 {
		t.Fatal(s.Status())
	}
	ee, _ := s.Store.Snapshot()
	if len(ee) != 1 || ee[0].Origin != "buffered history" {
		t.Fatal(ee)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream leaked")
	}
}
func TestTLSConfigRejectsAmbiguity(t *testing.T) {
	for _, c := range []Config{{}, {Address: "localhost:1", Plaintext: true, CAFile: "ca"}, {Address: "localhost:1", CertFile: "cert"}} {
		if _, err := c.Credentials(); err == nil {
			t.Fatal("accepted", c)
		}
	}
}
