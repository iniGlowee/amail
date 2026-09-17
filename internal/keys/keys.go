// Package keys manages the network key material.
//
// The network operator (Austin Armas) runs a tiny certificate authority.
// Every node gets a "key bundle" (<id>.amailkey): the network CA
// certificate plus a node certificate/private key signed by that CA. Nodes
// authenticate each other with mutual TLS against the CA, so only bundled
// nodes can join, and every byte on the wire is encrypted in transit.
//
// This is a tunnel, not end-to-end secrecy: every node on the network is
// trusted and a relaying node can read what it holds. See docs/DESIGN.md.
package keys

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iniGlowee/amail/internal/config"
)

// BundleVersion is the .amailkey format version.
const BundleVersion = 1

// DefaultValidDays is how long an issued node certificate lasts (3 years).
// Rotate with `amail ca issue <id>` again and revoke the old serial.
const DefaultValidDays = 1095

// File names inside <home>/keys and <home>/ca.
const (
	KeysDir   = "keys"
	CADirName = "ca"
	CACert    = "ca.crt"
	CAKey     = "ca.key"
	NodeCert  = "node.crt"
	NodeKey   = "node.key"
	IssuedLog = "issued.log"
)

// Bundle is the contents of an .amailkey file handed to a node.
type Bundle struct {
	AMailKey int    `json:"amail_key"`
	Network  string `json:"network"`
	NodeID   string `json:"node_id"`
	Issued   string `json:"issued"`
	IssuedBy string `json:"issued_by,omitempty"`
	Expires  string `json:"expires"`
	CACert   string `json:"ca_cert"`
	NodeCert string `json:"node_cert"`
	NodeKey  string `json:"node_key"`
}

// Material is the loaded key material of a node.
type Material struct {
	Cert     tls.Certificate
	Pool     *x509.CertPool
	NodeID   string
	Network  string
	NotAfter time.Time
}

// CADir returns the CA directory under an AMail home.
func CADir(home string) string { return filepath.Join(home, CADirName) }

func serial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}

func pemBlock(typ string, der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}))
}

func marshalKey(k *ecdsa.PrivateKey) (string, error) {
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return "", err
	}
	return pemBlock("EC PRIVATE KEY", der), nil
}

func parseCert(pemText string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, errors.New("no PEM certificate found")
	}
	return x509.ParseCertificate(block.Bytes)
}

// InitCA creates a new network certificate authority in dir.
func InitCA(dir, network string) error {
	network = strings.TrimSpace(network)
	if network == "" {
		return errors.New("network name is required")
	}
	if _, err := os.Stat(filepath.Join(dir, CAKey)); err == nil {
		return fmt.Errorf("a CA already exists in %s", dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	sn, err := serial()
	if err != nil {
		return err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          sn,
		Subject:               pkix.Name{CommonName: "AMail CA " + network, Organization: []string{network}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(20, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return err
	}
	keyPEM, err := marshalKey(priv)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, CAKey), []byte(keyPEM), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, CACert), []byte(pemBlock("CERTIFICATE", der)), 0o644); err != nil {
		return err
	}
	return nil
}

// CAInfo describes an existing CA.
type CAInfo struct {
	Network  string
	Subject  string
	NotAfter time.Time
	Dir      string
}

// CAPassEnv is the environment variable that unlocks a sealed CA key.
const CAPassEnv = "AMAIL_CA_PASS"

// CASealed reports whether the CA key in dir is passphrase-protected.
func CASealed(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, CAKey))
	return err == nil && IsSealed(b)
}

// ProtectCA seals the CA private key with a passphrase (in place).
func ProtectCA(dir, pass string) error {
	p := filepath.Join(dir, CAKey)
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	if IsSealed(b) {
		return errors.New("the CA key is already protected")
	}
	sealedKey, err := Seal(b, pass, "AMail CA key")
	if err != nil {
		return err
	}
	return os.WriteFile(p, sealedKey, 0o600)
}

// UnprotectCA removes the passphrase from the CA key (in place).
func UnprotectCA(dir, pass string) error {
	p := filepath.Join(dir, CAKey)
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	if !IsSealed(b) {
		return errors.New("the CA key is not protected")
	}
	plain, err := Open(b, pass)
	if err != nil {
		return err
	}
	return os.WriteFile(p, plain, 0o600)
}

func loadCA(dir string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certPEM, err := os.ReadFile(filepath.Join(dir, CACert))
	if err != nil {
		return nil, nil, fmt.Errorf("no CA in %s (run: amail ca init): %w", dir, err)
	}
	cert, err := parseCert(string(certPEM))
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, CAKey))
	if err != nil {
		return nil, nil, err
	}
	if IsSealed(keyPEM) {
		pass := os.Getenv(CAPassEnv)
		if pass == "" {
			return nil, nil, fmt.Errorf("the CA key is passphrase-protected: set %s", CAPassEnv)
		}
		keyPEM, err = Open(keyPEM, pass)
		if err != nil {
			return nil, nil, fmt.Errorf("CA key: %w", err)
		}
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, nil, errors.New("bad CA key file")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

// LoadCAInfo reads the CA certificate in dir.
func LoadCAInfo(dir string) (*CAInfo, error) {
	cert, _, err := loadCA(dir)
	if err != nil {
		return nil, err
	}
	return &CAInfo{Network: networkOf(cert), Subject: cert.Subject.CommonName, NotAfter: cert.NotAfter, Dir: dir}, nil
}

func networkOf(cert *x509.Certificate) string {
	if len(cert.Subject.Organization) > 0 {
		return cert.Subject.Organization[0]
	}
	return ""
}

// Issue creates a key bundle for nodeID signed by the CA in dir.
func Issue(dir, nodeID string, validDays int, issuedBy string) (*Bundle, error) {
	if err := config.ValidID(nodeID); err != nil {
		return nil, err
	}
	if validDays <= 0 {
		validDays = DefaultValidDays
	}
	caCert, caKey, err := loadCA(dir)
	if err != nil {
		return nil, err
	}
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	sn, err := serial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	network := networkOf(caCert)
	tmpl := &x509.Certificate{
		SerialNumber:          sn,
		Subject:               pkix.Name{CommonName: nodeID, Organization: []string{network}},
		DNSNames:              []string{nodeID},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(0, 0, validDays),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &priv.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	keyPEM, err := marshalKey(priv)
	if err != nil {
		return nil, err
	}
	b := &Bundle{
		AMailKey: BundleVersion,
		Network:  network,
		NodeID:   nodeID,
		Issued:   now.UTC().Format(time.RFC3339),
		IssuedBy: issuedBy,
		Expires:  tmpl.NotAfter.UTC().Format(time.RFC3339),
		CACert:   pemBlock("CERTIFICATE", caCert.Raw),
		NodeCert: pemBlock("CERTIFICATE", der),
		NodeKey:  keyPEM,
	}
	logLine := fmt.Sprintf("%s\t%s\tserial=%s\texpires=%s\n", b.Issued, nodeID, sn.Text(16), b.Expires)
	f, err := os.OpenFile(filepath.Join(dir, IssuedLog), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		_, _ = f.WriteString(logLine)
		_ = f.Close()
	}
	return b, nil
}

// KeyPassEnv is the environment variable that unlocks a sealed key bundle.
const KeyPassEnv = "AMAIL_KEY_PASS"

// WriteBundle saves a bundle as an .amailkey file. With a non-empty pass the
// file is sealed so it can travel over untrusted channels.
func WriteBundle(b *Bundle, path, pass string) error {
	js, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	js = append(js, '\n')
	if pass != "" {
		js, err = Seal(js, pass, "AMail key bundle for "+b.NodeID)
		if err != nil {
			return err
		}
	}
	return os.WriteFile(path, js, 0o600)
}

// BundleSealed reports whether an .amailkey file needs a passphrase.
func BundleSealed(path string) bool {
	b, err := os.ReadFile(path)
	return err == nil && IsSealed(b)
}

// ReadBundle loads an .amailkey file, unsealing it with pass if needed.
func ReadBundle(path, pass string) (*Bundle, error) {
	js, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if IsSealed(js) {
		if pass == "" {
			return nil, fmt.Errorf("%s is passphrase-protected: set %s (hint: %s)", path, KeyPassEnv, SealedHint(js))
		}
		js, err = Open(js, pass)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	b := &Bundle{}
	if err := json.Unmarshal(js, b); err != nil {
		return nil, fmt.Errorf("%s is not an AMail key bundle: %w", path, err)
	}
	if b.AMailKey != BundleVersion {
		return nil, fmt.Errorf("%s: unsupported bundle version %d", path, b.AMailKey)
	}
	if err := b.check(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return b, nil
}

// check verifies the bundle is internally consistent.
func (b *Bundle) check() error {
	ca, err := parseCert(b.CACert)
	if err != nil {
		return fmt.Errorf("ca_cert: %w", err)
	}
	node, err := parseCert(b.NodeCert)
	if err != nil {
		return fmt.Errorf("node_cert: %w", err)
	}
	if node.Subject.CommonName != b.NodeID {
		return fmt.Errorf("node_cert is for %q, bundle says %q", node.Subject.CommonName, b.NodeID)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	if _, err := node.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		return fmt.Errorf("node_cert is not signed by ca_cert: %w", err)
	}
	if _, err := tls.X509KeyPair([]byte(b.NodeCert), []byte(b.NodeKey)); err != nil {
		return fmt.Errorf("node_key does not match node_cert: %w", err)
	}
	return nil
}

// Install writes a bundle's material into <home>/keys.
func Install(home string, b *Bundle) error {
	if err := b.check(); err != nil {
		return err
	}
	dir := filepath.Join(home, KeysDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, CACert), []byte(b.CACert), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, NodeCert), []byte(b.NodeCert), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, NodeKey), []byte(b.NodeKey), 0o600)
}

// Installed reports whether a node key is present in home.
func Installed(home string) bool {
	_, err := os.Stat(filepath.Join(home, KeysDir, NodeKey))
	return err == nil
}

// Load reads the installed node key material from home.
func Load(home string) (*Material, error) {
	dir := filepath.Join(home, KeysDir)
	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, NodeCert), filepath.Join(dir, NodeKey))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no network key installed in %s (request one from the operator, then: amail join <file>.amailkey)", home)
		}
		return nil, err
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, err
	}
	cert.Leaf = leaf
	caPEM, err := os.ReadFile(filepath.Join(dir, CACert))
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("keys/ca.crt contains no certificate")
	}
	if warn := keyPermWarning(filepath.Join(dir, NodeKey)); warn != "" {
		fmt.Fprintln(os.Stderr, "WARNING:", warn)
	}
	return &Material{
		Cert:     cert,
		Pool:     pool,
		NodeID:   leaf.Subject.CommonName,
		Network:  networkOf(leaf),
		NotAfter: leaf.NotAfter,
	}, nil
}

// RevokedFunc reports whether a certificate serial (lower-case hex) is revoked.
type RevokedFunc func(serial string) bool

// tlsBase holds the settings shared by both directions: TLS 1.3 only (so
// only AEAD suites with forward secrecy), modern curves, no session
// resumption (every connection is a fresh, fully authenticated handshake).
func (m *Material) tlsBase() *tls.Config {
	return &tls.Config{
		Certificates:           []tls.Certificate{m.Cert},
		MinVersion:             tls.VersionTLS13,
		CurvePreferences:       []tls.CurveID{tls.X25519, tls.CurveP256},
		SessionTicketsDisabled: true,
		ClientSessionCache:     nil,
	}
}

// verifyChain parses the presented chain, verifies it against the network
// CA and applies the id and revocation checks.
func (m *Material) verifyChain(raw [][]byte, expectID string, revoked RevokedFunc) error {
	if len(raw) == 0 {
		return errors.New("peer sent no certificate")
	}
	certs := make([]*x509.Certificate, 0, len(raw))
	for _, r := range raw {
		c, err := x509.ParseCertificate(r)
		if err != nil {
			return err
		}
		certs = append(certs, c)
	}
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	if _, err := certs[0].Verify(x509.VerifyOptions{Roots: m.Pool, Intermediates: inter, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		return fmt.Errorf("peer certificate is not from this network: %w", err)
	}
	if expectID != "" && certs[0].Subject.CommonName != expectID {
		return fmt.Errorf("peer is %q, expected %q", certs[0].Subject.CommonName, expectID)
	}
	if revoked != nil && revoked(Serial(certs[0])) {
		return fmt.Errorf("peer certificate %s (%s) is revoked", Serial(certs[0]), certs[0].Subject.CommonName)
	}
	return nil
}

// ServerTLS returns the TLS config for accepting connections: the peer must
// present a certificate signed by the network CA that is not revoked.
func (m *Material) ServerTLS(revoked RevokedFunc) *tls.Config {
	c := m.tlsBase()
	c.ClientAuth = tls.RequireAndVerifyClientCert
	c.ClientCAs = m.Pool
	c.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error {
		return m.verifyChain(raw, "", revoked)
	}
	return c
}

// ClientTLS returns the TLS config for dialing a peer. The peer's certificate
// must chain to the network CA, not be revoked and, when expectID is set, be
// issued to that node id. Peers are addressed by IP as often as by name, so
// standard host name verification is replaced by this check.
func (m *Material) ClientTLS(expectID string, revoked RevokedFunc) *tls.Config {
	c := m.tlsBase()
	c.InsecureSkipVerify = true // replaced by VerifyPeerCertificate below
	c.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error {
		return m.verifyChain(raw, expectID, revoked)
	}
	return c
}

// PeerID returns the node id (certificate CN) of the other side.
func PeerID(cs tls.ConnectionState) string {
	if len(cs.PeerCertificates) == 0 {
		return ""
	}
	return cs.PeerCertificates[0].Subject.CommonName
}
