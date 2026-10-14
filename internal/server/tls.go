package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// Local PKI for TLS "auto": a CA (10 years) and a server certificate signed
// by it. Installing ca.crt on the thin clients once (e.g. via group policy)
// makes browsers trust the server without warnings, and agents pin the CA
// fingerprint at enrollment.

type pki struct {
	caCertPEM   []byte
	caFP        string // SHA-256 of the CA certificate (DER), hex
	certFile    string
	keyFile     string
	caCertPath  string
	certificate tls.Certificate
}

// CAFingerprint returns the hex SHA-256 fingerprint of a DER certificate.
func CAFingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

func loadOrCreatePKI(dataDir string, ips []string, hostnames []string) (*pki, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	caCertPath := filepath.Join(dataDir, "ca.crt")
	caKeyPath := filepath.Join(dataDir, "ca.key")
	certPath := filepath.Join(dataDir, "server.crt")
	keyPath := filepath.Join(dataDir, "server.key")

	caCert, caKey, err := loadPair(caCertPath, caKeyPath)
	if err != nil {
		caCert, caKey, err = createCA(caCertPath, caKeyPath)
		if err != nil {
			return nil, err
		}
		os.Remove(certPath) // re-issue the server cert under the new CA
	}

	host, _ := os.Hostname()
	wantDNS := uniq(append([]string{"localhost", host, host + ".local"}, hostnames...))
	wantIPs := uniq(append([]string{"127.0.0.1"}, ips...))

	leaf, _, err := loadPair(certPath, keyPath)
	if err != nil || !coversNames(leaf, wantDNS, wantIPs) || time.Until(leaf.NotAfter) < 30*24*time.Hour ||
		leaf.CheckSignatureFrom(caCert) != nil {
		if err := createLeaf(certPath, keyPath, caCert, caKey, wantDNS, wantIPs); err != nil {
			return nil, err
		}
	}
	tlsCert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	// Send the CA along so agents can verify against the pinned CA.
	tlsCert.Certificate = append(tlsCert.Certificate, caCert.Raw)
	return &pki{
		caCertPEM:   pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw}),
		caFP:        CAFingerprint(caCert.Raw),
		certFile:    certPath,
		keyFile:     keyPath,
		caCertPath:  caCertPath,
		certificate: tlsCert,
	}, nil
}

func uniq(in []string) []string {
	var out []string
	for _, s := range in {
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func coversNames(c *x509.Certificate, dns, ips []string) bool {
	for _, d := range dns {
		if !slices.Contains(c.DNSNames, d) {
			return false
		}
	}
	for _, ip := range ips {
		found := false
		for _, have := range c.IPAddresses {
			if have.String() == ip {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func loadPair(certPath, keyPath string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, err
	}
	cb, _ := pem.Decode(certPEM)
	kb, _ := pem.Decode(keyPEM)
	if cb == nil || kb == nil {
		return nil, nil, errors.New("invalid PEM")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, nil, err
	}
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

func writePair(certPath, keyPath string, der []byte, key *ecdsa.PrivateKey) error {
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return err
	}
	return os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 126))
	return n
}

func createCA(certPath, keyPath string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	host, _ := os.Hostname()
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "Remotly CA (" + host + ")", Organization: []string{"Remotly"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	if err := writePair(certPath, keyPath, der, key); err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	return cert, key, err
}

func createLeaf(certPath, keyPath string, ca *x509.Certificate, caKey *ecdsa.PrivateKey, dns, ips []string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: dns[1], Organization: []string{"Remotly"}},
		NotBefore:    time.Now().Add(-time.Hour),
		// Browsers reject leaf certificates valid for more than 398 days.
		NotAfter:    time.Now().AddDate(0, 0, 397),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    dns,
	}
	for _, ip := range ips {
		if p := net.ParseIP(ip); p != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, p)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return err
	}
	return writePair(certPath, keyPath, der, key)
}
