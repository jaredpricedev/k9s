package inspect

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestCertificatesValidityAndBundle(t *testing.T) {
	now := time.Now().UTC()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		start, end time.Time
		want       string
	}{
		{"valid", now.Add(-time.Hour), now.Add(time.Hour), "Within validity period"},
		{"expired", now.Add(-2 * time.Hour), now.Add(-time.Hour), "EXPIRED"},
		{"future", now.Add(time.Hour), now.Add(2 * time.Hour), "NOT YET VALID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: tc.start, NotAfter: tc.end, DNSNames: []string{"example.test"}}
			der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
			if err != nil {
				t.Fatal(err)
			}
			data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
			text, err := Certificates(append(data, data...), now)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{tc.want, "example.test", "CERTIFICATE 2", "trust chain", "No endpoint was contacted"} {
				if !strings.Contains(text, want) {
					t.Fatalf("missing %s", want)
				}
			}
		})
	}
}
func TestCertificatesRejectUnsafeOrMalformedInput(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("SECRET-MARKER"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("SECRET-MARKER")}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("SECRET-MARKER")})} {
		text, err := Certificates(data, time.Now())
		if err == nil || text != "" || strings.Contains(err.Error(), "SECRET-MARKER") {
			t.Fatal("unsafe error or accepted malformed input")
		}
	}
}
