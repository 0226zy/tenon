// Package secret implements token hashing and deterministic key derivation.
package secret

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

func Hash(token string) string {
	s := sha256.Sum256([]byte(token))
	return hex.EncodeToString(s[:])
}

func DeriveWSDToken(master []byte, instID string) string {
	m := hmac.New(sha256.New, master)
	_, _ = m.Write([]byte("wsd:" + instID))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func Sign(master []byte, purpose, payload string) string {
	m := hmac.New(sha256.New, master)
	_, _ = m.Write([]byte(purpose + "\x00" + payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// LoadMaster reads a 32-byte master key. Only an explicitly local deployment may
// create a missing key; production fails closed instead of changing identities.
func LoadMaster(path string, allowCreate bool) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		if len(raw) == 32 {
			return raw, nil
		}
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(string(raw))
		if decodeErr == nil && len(decoded) == 32 {
			return decoded, nil
		}
		decoded, decodeErr = base64.StdEncoding.DecodeString(string(raw))
		if decodeErr == nil && len(decoded) == 32 {
			return decoded, nil
		}
		return nil, fmt.Errorf("master secret must contain exactly 32 bytes")
	}
	if !os.IsNotExist(err) || !allowCreate {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("master secret is not configured: %w", err)
		}
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return LoadMaster(path, false)
		}
		return nil, err
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(key); err != nil {
		return nil, err
	}
	if err := file.Sync(); err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	ok = true
	return key, nil
}
