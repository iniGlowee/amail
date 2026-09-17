package keys

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSealOpen(t *testing.T) {
	plain := []byte("-----BEGIN EC PRIVATE KEY-----\nsecret\n-----END EC PRIVATE KEY-----\n")
	sealedKey, err := Seal(plain, "correct horse battery", "test")
	if err != nil {
		t.Fatal(err)
	}
	if !IsSealed(sealedKey) || strings.Contains(string(sealedKey), "secret") {
		t.Fatal("not sealed")
	}
	if SealedHint(sealedKey) != "test" {
		t.Fatal("hint")
	}
	got, err := Open(sealedKey, "correct horse battery")
	if err != nil || string(got) != string(plain) {
		t.Fatalf("open: %v", err)
	}
	if _, err := Open(sealedKey, "wrong passphrase!"); err == nil {
		t.Fatal("wrong passphrase accepted")
	}
	sealedKey[len(sealedKey)/2] ^= 0x01
	if _, err := Open(sealedKey, "correct horse battery"); err == nil {
		t.Fatal("tampered blob accepted")
	}
	if _, err := Seal(plain, "short", ""); err == nil {
		t.Fatal("short passphrase accepted")
	}
}

func TestCAProtectAndSealedBundle(t *testing.T) {
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca")
	if err := InitCA(ca, "sealnet"); err != nil {
		t.Fatal(err)
	}
	if err := ProtectCA(ca, "operator-passphrase"); err != nil {
		t.Fatal(err)
	}
	if !CASealed(ca) {
		t.Fatal("CA not sealed")
	}
	if _, err := Issue(ca, "alpha", 30, "t"); err == nil {
		t.Fatal("issue without passphrase should fail")
	}
	t.Setenv(CAPassEnv, "operator-passphrase")
	b, err := Issue(ca, "alpha", 30, "t")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "alpha.amailkey")
	if err := WriteBundle(b, p, "bundle-pass-123"); err != nil {
		t.Fatal(err)
	}
	if !BundleSealed(p) {
		t.Fatal("bundle not sealed")
	}
	if _, err := ReadBundle(p, ""); err == nil {
		t.Fatal("sealed bundle opened without passphrase")
	}
	rb, err := ReadBundle(p, "bundle-pass-123")
	if err != nil || rb.NodeID != "alpha" {
		t.Fatalf("read bundle: %v", err)
	}
	if err := UnprotectCA(ca, "operator-passphrase"); err != nil || CASealed(ca) {
		t.Fatalf("unprotect: %v", err)
	}
}

func TestSignatures(t *testing.T) {
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca")
	if err := InitCA(ca, "signet"); err != nil {
		t.Fatal(err)
	}
	mk := func(id string) *Material {
		b, err := Issue(ca, id, 30, "t")
		if err != nil {
			t.Fatal(err)
		}
		home := filepath.Join(dir, id)
		if err := Install(home, b); err != nil {
			t.Fatal(err)
		}
		m, err := Load(home)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	alice, bob := mk("alice"), mk("bob")
	data := Canonical("alice", "bob", "docs/x.pdf", 1234, "ABCD")
	sig, err := alice.Sign(data)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := bob.VerifyOriginCert(alice.CertPEM(), "alice", func(string) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifySig(cert, data, sig); err != nil {
		t.Fatal(err)
	}
	// wrong content / wrong signer / wrong id / revoked
	if err := VerifySig(cert, Canonical("alice", "bob", "docs/x.pdf", 1235, "abcd"), sig); err == nil {
		t.Fatal("altered size accepted")
	}
	bobSig, _ := bob.Sign(data)
	if err := VerifySig(cert, data, bobSig); err == nil {
		t.Fatal("signature by another key accepted")
	}
	if _, err := bob.VerifyOriginCert(alice.CertPEM(), "bob", nil); err == nil {
		t.Fatal("cert for the wrong id accepted")
	}
	if _, err := bob.VerifyOriginCert(alice.CertPEM(), "alice", func(s string) bool { return s == Serial(alice.Cert.Leaf) }); err == nil {
		t.Fatal("revoked cert accepted")
	}
	// a certificate from another CA is refused
	other := filepath.Join(dir, "otherca")
	_ = InitCA(other, "othernet")
	ob, _ := Issue(other, "alice", 30, "t")
	if _, err := bob.VerifyOriginCert(ob.NodeCert, "alice", nil); err == nil {
		t.Fatal("foreign CA cert accepted")
	}
}
