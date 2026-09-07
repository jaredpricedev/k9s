// SPDX-License-Identifier: Apache-2.0
package hubble

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	observer "github.com/cilium/cilium/api/v1/observer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"
	"io"
	"os"
	"sync"
	"time"
)

// Config belongs to a kube context, never to a global Hubble CLI configuration.
type Config struct {
	ClusterName string `yaml:"clusterName,omitempty"`
	Address     string `yaml:"address,omitempty"`
	Plaintext   bool   `yaml:"plaintext,omitempty"`
	CAFile      string `yaml:"caFile,omitempty"`
	CertFile    string `yaml:"certFile,omitempty"`
	KeyFile     string `yaml:"keyFile,omitempty"`
	ServerName  string `yaml:"serverName,omitempty"`
}

func (c Config) Credentials() (credentials.TransportCredentials, error) {
	if c.Address == "" {
		return nil, errors.New("Hubble Relay not configured: set k9s.hubble.address in this context's config.yaml")
	}
	if c.Plaintext {
		if c.CAFile != "" || c.CertFile != "" || c.KeyFile != "" || c.ServerName != "" {
			return nil, errors.New("plaintext cannot be combined with TLS settings")
		}
		return insecure.NewCredentials(), nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: c.ServerName}
	if c.CAFile != "" {
		pem, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read Hubble CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("invalid Hubble CA PEM")
		}
		cfg.RootCAs = pool
	}
	if (c.CertFile == "") != (c.KeyFile == "") {
		return nil, errors.New("both Hubble certFile and keyFile are required")
	}
	if c.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load Hubble client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return credentials.NewTLS(cfg), nil
}

type Node struct{ Name, State, Version string }
type Status struct {
	Phase, Error, CoverageError, NodeEvent, LossDetail, Version string
	Connected, Unavailable                                      uint32
	CoverageKnown                                               bool
	Lost                                                        uint64
	Nodes                                                       []Node
}
type Session struct {
	Store  *Store
	config Config
	scope  Scope
	query  Query
	mu     sync.Mutex
	status Status
}

func NewSession(c Config, s Scope, q Query, capacity int) *Session {
	return &Session{Store: NewStore(capacity), config: c, scope: s, query: q, status: Status{Phase: "connecting"}}
}
func (s *Session) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.status
	v.Nodes = append([]Node(nil), v.Nodes...)
	return v
}
func (s *Session) update(f func(*Status)) { s.mu.Lock(); defer s.mu.Unlock(); f(&s.status) }
func (s *Session) fail(err error) {
	s.update(func(v *Status) { v.Phase = "disconnected"; v.Error = Clean(err.Error()) })
}
func (s *Session) poll(ctx context.Context, c observer.ObserverClient) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	status, err := c.ServerStatus(ctx, &observer.ServerStatusRequest{})
	if err != nil {
		s.update(func(v *Status) { v.CoverageKnown = false; v.CoverageError = Clean(err.Error()) })
		return
	}
	nodes, nerr := c.GetNodes(ctx, &observer.GetNodesRequest{})
	s.update(func(v *Status) {
		v.Version = Clean(status.GetVersion())
		v.CoverageKnown = status.NumConnectedNodes != nil && status.NumUnavailableNodes != nil
		v.Connected = status.GetNumConnectedNodes().GetValue()
		v.Unavailable = status.GetNumUnavailableNodes().GetValue()
		v.CoverageError = ""
		v.Nodes = nil
		if nerr != nil {
			v.CoverageError = Clean(nerr.Error())
		} else {
			for _, n := range nodes.GetNodes() {
				v.Nodes = append(v.Nodes, Node{Clean(n.GetName()), n.GetState().String(), Clean(n.GetVersion())})
			}
		}
	})
}

// Run owns one connection. Explicit retry starts a new session and exposes the
// observation gap instead of implying lossless reconnect or durable history.
func (s *Session) Run(ctx context.Context) {
	creds, err := s.config.Credentials()
	if err != nil {
		s.fail(err)
		return
	}
	conn, err := grpc.NewClient(s.config.Address, grpc.WithTransportCredentials(creds))
	if err != nil {
		s.fail(err)
		return
	}
	defer conn.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c := observer.NewObserverClient(conn)
	s.poll(ctx, c)
	go func() {
		timer := time.NewTicker(5 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				s.poll(ctx, c)
			}
		}
	}()
	if len(s.scope.Pods) == 0 {
		s.update(func(v *Status) { v.Phase = "status only" })
		<-ctx.Done()
		return
	}
	s.update(func(v *Status) { v.Phase = "loading buffered history" })
	hctx, hcancel := context.WithTimeout(ctx, 10*time.Second)
	err = s.read(hctx, c, &observer.GetFlowsRequest{Number: 500, Whitelist: Filters(s.scope, s.query)}, "buffered history")
	hcancel()
	if err != nil && !errors.Is(err, io.EOF) {
		s.fail(err)
		return
	}
	// Separate requests make provenance explicit. Handoff is not guaranteed gapless.
	s.update(func(v *Status) { v.Phase = "live observation" })
	err = s.read(ctx, c, &observer.GetFlowsRequest{Follow: true, Since: timestamppb.Now(), Whitelist: Filters(s.scope, s.query)}, "live")
	if ctx.Err() == nil {
		if errors.Is(err, io.EOF) {
			err = errors.New("Relay ended live stream")
		}
		s.fail(err)
	}
}
func (s *Session) read(ctx context.Context, c observer.ObserverClient, r *observer.GetFlowsRequest, origin string) error {
	stream, err := c.GetFlows(ctx, r)
	if err != nil {
		return err
	}
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}
		if f := msg.GetFlow(); f != nil {
			e := Normalize(f, origin)
			if s.scope.Includes(e) {
				s.Store.Add(e)
			}
		}
		if loss := msg.GetLostEvents(); loss != nil {
			s.update(func(v *Status) {
				v.Lost += loss.GetNumEventsLost()
				v.LossDetail = Clean(fmt.Sprintf("%s: %s", msg.GetNodeName(), loss.GetSource()))
			})
		}
		if n := msg.GetNodeStatus(); n != nil {
			s.update(func(v *Status) {
				v.NodeEvent = Clean(fmt.Sprintf("%s %v %s", n.GetStateChange(), n.GetNodeNames(), n.GetMessage()))
			})
		}
	}
}
