package hubble

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	observer "github.com/cilium/cilium/api/v1/observer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func TestVerifiedMutualTLS(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "relay.test"}, DNSNames: []string{"relay.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	ca := x509.NewCertPool()
	ca.AppendCertsFromPEM(certPEM)
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: ca})))
	observer.RegisterObserverServer(srv, testObserver{})
	go func() { _ = srv.Serve(listener) }()
	defer srv.Stop()
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if err = os.WriteFile(certPath, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, serverName string
		client, ok       bool
	}{{"verified", "relay.test", true, true}, {"wrong server name", "wrong.test", true, false}, {"missing client certificate", "relay.test", false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Address: listener.Addr().String(), CAFile: certPath, ServerName: tc.serverName}
			if tc.client {
				cfg.CertFile = certPath
				cfg.KeyFile = keyPath
			}
			creds, err := cfg.Credentials()
			if err != nil {
				t.Fatal(err)
			}
			conn, err := grpc.NewClient(cfg.Address, grpc.WithTransportCredentials(creds))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err = observer.NewObserverClient(conn).ServerStatus(ctx, &observer.ServerStatusRequest{})
			if (err == nil) != tc.ok {
				t.Fatalf("TLS success=%v expected %v: %v", err == nil, tc.ok, err)
			}
		})
	}
}
func TestDisconnectPreservesRetainedEvents(t *testing.T) {
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	observer.RegisterObserverServer(srv, testObserver{})
	go func() { _ = srv.Serve(l) }()
	defer srv.Stop()
	s := NewSession(Config{Address: l.Addr().String(), Plaintext: true}, Scope{Pods: []string{"ns/a"}}, Query{}, 10)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	for s.Status().Lost == 0 && ctx.Err() == nil {
		time.Sleep(time.Millisecond * 10)
	}
	srv.Stop()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("disconnect not detected")
	}
	if s.Status().Phase != phaseDisconnected || s.Status().Error == "" || s.Status().CoverageKnown {
		t.Fatal(s.Status())
	}
	ee, _ := s.Store.Snapshot()
	if len(ee) != 1 {
		t.Fatal("retained data lost")
	}
}
