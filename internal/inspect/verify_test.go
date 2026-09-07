package inspect

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestVerifyAndProbeRejectWrongNameAndUnknownRoot(t *testing.T) {
	now := time.Now()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}
	rootDER, err := x509.CreateCertificate(rand.Reader, root, root, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	rootCert, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{"probe.test"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, leaf, rootCert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	bundle := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	roots := x509.NewCertPool()
	roots.AddCert(rootCert)
	if _, err := VerifyBundle(bundle, roots, "probe.test", now); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		roots *x509.CertPool
		now   time.Time
	}{{"wrong.test", roots, now}, {"probe.test", x509.NewCertPool(), now}, {"probe.test", roots, now.Add(2 * time.Hour)}} {
		if _, err := VerifyBundle(bundle, tc.roots, tc.name, tc.now); err == nil {
			t.Fatal("invalid identity/trust/time accepted")
		}
	}
	full := append(append([]byte{}, bundle...), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER})...)
	if _, err := VerifyBundle(full, x509.NewCertPool(), "probe.test", now); err == nil {
		t.Fatal("bundle root implicitly trusted")
	}
	var requests atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der, rootDER}, PrivateKey: key}}}
	server.StartTLS()
	defer server.Close()
	address := strings.TrimPrefix(server.URL, "https://")
	if report, err := Probe(t.Context(), address, "probe.test", roots); err != nil || !strings.Contains(report, "VERIFIED TLS HANDSHAKE") {
		t.Fatalf("%s %v", report, err)
	}
	if _, err := Probe(t.Context(), address, "wrong.test", roots); err == nil {
		t.Fatal("wrong probe hostname accepted")
	}
	if _, err := Probe(t.Context(), address, "probe.test", x509.NewCertPool()); err == nil {
		t.Fatal("untrusted probe accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Probe(ctx, address, "probe.test", roots); err == nil {
		t.Fatal("cancel ignored")
	}
	if requests.Load() != 0 {
		t.Fatal("probe sent application request")
	}
}
