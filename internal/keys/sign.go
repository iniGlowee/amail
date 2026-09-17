package keys

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Origin signatures: the node that first sends a file signs a canonical
// description of it (who, to whom, which name, how many bytes, SHA-256 of
// the content) with its own private key. Relays pass the signature and the
// origin's certificate along; the final receiver checks that the
// certificate chains to the network CA, names the claimed origin, is not
// revoked, and that the signature and the content hash match. A relay can
// therefore neither forge a sender nor alter a file unnoticed.

// Canonical returns the byte string that is signed for a file.
func Canonical(origin, to, name string, size int64, sha256hex string) []byte {
	return []byte(strings.Join([]string{"amail-sig-v1", origin, to, name, strconv.FormatInt(size, 10), strings.ToLower(sha256hex)}, "\n"))
}

// Sign signs data with this node's key and returns the base64 ASN.1 signature.
func (m *Material) Sign(data []byte) (string, error) {
	signer, ok := m.Cert.PrivateKey.(crypto.Signer)
	if !ok {
		return "", errors.New("node key cannot sign")
	}
	digest := sha256.Sum256(data)
	sig, err := signer.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// CertPEM returns this node's certificate as PEM for the origin_cert field.
func (m *Material) CertPEM() string {
	if m.Cert.Leaf == nil {
		return ""
	}
	return pemBlock("CERTIFICATE", m.Cert.Leaf.Raw)
}

// Serial returns a certificate's serial number as lower-case hex, the form
// used in issued.log and revocation lists.
func Serial(c *x509.Certificate) string {
	if c == nil || c.SerialNumber == nil {
		return ""
	}
	return strings.ToLower(c.SerialNumber.Text(16))
}

// VerifyOriginCert parses a PEM certificate and checks it chains to the
// network CA, is issued to wantID and is not revoked.
func (m *Material) VerifyOriginCert(pemText, wantID string, revoked func(serial string) bool) (*x509.Certificate, error) {
	c, err := parseCert(pemText)
	if err != nil {
		return nil, fmt.Errorf("origin certificate: %w", err)
	}
	if _, err := c.Verify(x509.VerifyOptions{Roots: m.Pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		return nil, fmt.Errorf("origin certificate is not from this network: %w", err)
	}
	if c.Subject.CommonName != wantID {
		return nil, fmt.Errorf("origin certificate is for %q, not %q", c.Subject.CommonName, wantID)
	}
	if revoked != nil && revoked(Serial(c)) {
		return nil, fmt.Errorf("origin certificate %s is revoked", Serial(c))
	}
	return c, nil
}

// VerifySig checks a base64 ASN.1 ECDSA signature over data against cert.
func VerifySig(cert *x509.Certificate, data []byte, sigB64 string) error {
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return errors.New("origin certificate has no ECDSA key")
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return errors.New("signature is not valid base64")
	}
	digest := sha256.Sum256(data)
	if !ecdsa.VerifyASN1(pub, digest[:], sig) {
		return errors.New("signature does not verify")
	}
	return nil
}
