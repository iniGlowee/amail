package keys

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Passphrase protection for secrets at rest (the CA key, key bundles in
// transit): PBKDF2-HMAC-SHA256 with a random salt derives a 256-bit key,
// AES-256-GCM seals the payload. Standard library only.

const (
	sealedVersion = 1
	sealedIter    = 600_000
)

type sealed struct {
	AMailSealed int    `json:"amail_sealed"`
	KDF         string `json:"kdf"`
	Iter        int    `json:"iter"`
	Salt        string `json:"salt"`
	Nonce       string `json:"nonce"`
	CT          string `json:"ct"`
	Hint        string `json:"hint,omitempty"`
}

// IsSealed reports whether data is a passphrase-sealed blob.
func IsSealed(data []byte) bool {
	t := strings.TrimSpace(string(data))
	return strings.HasPrefix(t, "{") && strings.Contains(t, `"amail_sealed"`)
}

func derive(pass string, salt []byte, iter int) ([]byte, error) {
	return pbkdf2.Key(sha256.New, pass, salt, iter, 32)
}

// Seal encrypts plain with a passphrase. hint is stored in the clear.
func Seal(plain []byte, pass, hint string) ([]byte, error) {
	if len(pass) < 8 {
		return nil, errors.New("passphrase must be at least 8 characters")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	key, err := derive(pass, salt, sealedIter)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ct := gcm.Seal(nil, nonce, plain, []byte("amail-sealed-v1"))
	s := sealed{
		AMailSealed: sealedVersion, KDF: "pbkdf2-sha256", Iter: sealedIter,
		Salt: base64.StdEncoding.EncodeToString(salt), Nonce: base64.StdEncoding.EncodeToString(nonce),
		CT: base64.StdEncoding.EncodeToString(ct), Hint: hint,
	}
	js, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(js, '\n'), nil
}

// Open decrypts a sealed blob with the passphrase.
func Open(data []byte, pass string) ([]byte, error) {
	var s sealed
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, errors.New("not a sealed blob")
	}
	if s.AMailSealed != sealedVersion || s.KDF != "pbkdf2-sha256" {
		return nil, fmt.Errorf("unsupported sealed format %d/%s", s.AMailSealed, s.KDF)
	}
	if s.Iter < 100_000 || s.Iter > 10_000_000 {
		return nil, errors.New("sealed blob has an implausible iteration count")
	}
	salt, err := base64.StdEncoding.DecodeString(s.Salt)
	if err != nil {
		return nil, err
	}
	nonce, err := base64.StdEncoding.DecodeString(s.Nonce)
	if err != nil {
		return nil, err
	}
	ct, err := base64.StdEncoding.DecodeString(s.CT)
	if err != nil {
		return nil, err
	}
	key, err := derive(pass, salt, s.Iter)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, ct, []byte("amail-sealed-v1"))
	if err != nil {
		return nil, errors.New("wrong passphrase (or the file was altered)")
	}
	return plain, nil
}

// SealedHint returns the stored hint, if any.
func SealedHint(data []byte) string {
	var s sealed
	if json.Unmarshal(data, &s) == nil {
		return s.Hint
	}
	return ""
}
