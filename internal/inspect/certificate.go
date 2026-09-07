// SPDX-License-Identifier: Apache-2.0
package inspect

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
	"time"
)

// Certificates projects public certificate metadata only; errors never include input bytes.
func Certificates(data []byte, now time.Time) (string, error) {
	if len(data) > 1024*1024 {
		return "", fmt.Errorf("certificate bundle exceeds 1 MiB")
	}
	var out strings.Builder
	count := 0
	for len(bytes.TrimSpace(data)) > 0 {
		if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("-----BEGIN CERTIFICATE-----")) {
			return "", fmt.Errorf("expected certificate PEM block")
		}
		block, rest := pem.Decode(data)
		if block == nil || block.Type != "CERTIFICATE" {
			return "", fmt.Errorf("invalid certificate PEM")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return "", fmt.Errorf("invalid X.509 certificate")
		}
		count++
		state := "Within validity period (trust not checked)"
		if now.Before(cert.NotBefore) {
			state = "NOT YET VALID"
		} else if !now.Before(cert.NotAfter) {
			state = "EXPIRED"
		}
		fmt.Fprintf(&out, "CERTIFICATE %d\n%s\nSubject: %s\nIssuer: %s\n"+
			"Not before: %s\nNot after: %s\nDNS SANs: %s\nIP SANs: %v\n"+
			"URI SANs: %v\nEmail SANs: %v\nCA: %t\nSHA256: %X\n\n",
			count, state, cert.Subject, cert.Issuer,
			cert.NotBefore.UTC().Format(time.RFC3339), cert.NotAfter.UTC().Format(time.RFC3339),
			strings.Join(cert.DNSNames, ", "), cert.IPAddresses, cert.URIs, cert.EmailAddresses, cert.IsCA, sha256.Sum256(cert.Raw))
		data = rest
	}
	if count == 0 {
		return "", fmt.Errorf("no certificates in tls.crt")
	}
	out.WriteString("Public certificate metadata only. Private key not inspected.\nBundle order does not prove a valid trust chain. No endpoint was contacted.\n")
	return out.String(), nil
}
