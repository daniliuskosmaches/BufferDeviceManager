package clipsync

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"math/big"
	"time"
)

// Identity is ephemeral: restarting the companion revokes all old pairing links.
func Identity() (tls.Certificate, string, string, error) {
	var zero tls.Certificate
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return zero, "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return zero, "", "", err
	}
	template := x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "BufferSync"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(30 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return zero, "", "", err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return zero, "", "", err
	}
	pin := sha256.Sum256(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, hex.EncodeToString(secret), hex.EncodeToString(pin[:]), nil
}
