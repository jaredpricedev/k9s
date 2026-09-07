// SPDX-License-Identifier: Apache-2.0
package inspect

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TrustRoots uses system roots unless the user explicitly selects a local CA bundle.
func TrustRoots(path string) (*x509.CertPool, string, error) {
	if path == "" {
		roots, err := x509.SystemCertPool()
		return roots, "system roots on the k9plus machine", err
	}
	if !filepath.IsAbs(path) {
		return nil, "", fmt.Errorf("CA path must be absolute")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() {
		return nil, "", fmt.Errorf("CA path must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > 1024*1024 {
		return nil, "", fmt.Errorf("CA file exceeds 1 MiB")
	}
	roots := x509.NewCertPool()
	certs, err := ParseBundle(data)
	if err != nil {
		return nil, "", fmt.Errorf("invalid CA certificate bundle")
	}
	for _, cert := range certs {
		if !cert.IsCA {
			return nil, "", fmt.Errorf("CA file contains a non-CA certificate")
		}
		roots.AddCert(cert)
	}
	return roots, "explicit CA file: " + path, nil
}

// ParseBundle rejects non-certificate PEM and never exposes the supplied bytes in errors.
func ParseBundle(data []byte) ([]*x509.Certificate, error) {
	if len(data) > 1024*1024 {
		return nil, fmt.Errorf("certificate bundle exceeds 1 MiB")
	}
	var certs []*x509.Certificate
	for strings.TrimSpace(string(data)) != "" {
		if !strings.HasPrefix(strings.TrimSpace(string(data)), "-----BEGIN CERTIFICATE-----") {
			return nil, fmt.Errorf("expected certificate PEM block")
		}
		block, rest := pem.Decode(data)
		if block == nil || block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("invalid certificate PEM")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("invalid X.509 certificate")
		}
		certs = append(certs, cert)
		data = rest
	}
	if len(certs) == 0 {
		return nil, fmt.Errorf("no certificates in bundle")
	}
	return certs, nil
}

// VerifyBundle verifies the first certificate as a server leaf; supplied bundle members are intermediates, never implicit trust anchors.
func VerifyBundle(data []byte, roots *x509.CertPool, hostname string, now time.Time) (string, error) {
	certs, err := ParseBundle(data)
	if err != nil {
		return "", err
	}
	inter := x509.NewCertPool()
	for _, cert := range certs[1:] {
		inter.AddCert(cert)
	}
	chains, err := certs[0].Verify(x509.VerifyOptions{
		Roots: roots, Intermediates: inter, DNSName: hostname, CurrentTime: now,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	if err != nil {
		return "", err
	}
	result := fmt.Sprintf("VERIFIED: server-auth chain (%d valid chain paths).", len(chains))
	if hostname == "" {
		result += " Hostname not checked."
	} else {
		result += " Hostname matched: " + hostname
	}
	return result + " Revocation not checked; no endpoint contacted.", nil
}

// Probe performs only a verified TLS handshake from the process running k9plus.
func Probe(ctx context.Context, address, serverName string, roots *x509.CertPool) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || port == "" || serverName == "" {
		return "", fmt.Errorf("provide host:port and explicit serverName")
	}
	dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 8 * time.Second}, Config: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: serverName}}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		return "", fmt.Errorf("TLS connection unavailable")
	}
	state := tlsConn.ConnectionState()
	var data []byte
	for _, cert := range state.PeerCertificates {
		data = append(data, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})...)
	}
	report, err := Certificates(data, time.Now())
	if err != nil {
		return "", err
	}
	report = strings.ReplaceAll(report, "No endpoint was contacted.", "Endpoint contacted by an explicit TLS probe.")
	return fmt.Sprintf("VERIFIED TLS HANDSHAKE\nExecution: k9plus machine (not a pod)\n"+
		"Target: %s\nRemote: %s\nServer name: %s\nVersion: %s\nCipher: %s\n"+
		"No HTTP request sent. Revocation not checked.\n\n%s",
		address, conn.RemoteAddr(), serverName, tls.VersionName(state.Version), tls.CipherSuiteName(state.CipherSuite), report), nil
}
