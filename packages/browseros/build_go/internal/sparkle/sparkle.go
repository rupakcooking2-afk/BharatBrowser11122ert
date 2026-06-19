// Package sparkle ports build/common/sparkle.py: Ed25519 signing compatible
// with the Sparkle auto-update framework, using stdlib crypto/ed25519.
package sparkle

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"

	"github.com/browseros-ai/BrowserOS/packages/browseros/build_go/internal/envx"
)

// ParsePrivateKey accepts Sparkle key material in any of its shapes: raw
// 32-byte seed, raw 64-byte (seed + public key), or base64 of either.
func ParsePrivateKey(keyData string) (ed25519.PrivateKey, error) {
	keyBytes, err := base64.StdEncoding.DecodeString(keyData)
	if err != nil {
		keyBytes = []byte(keyData)
	}
	switch len(keyBytes) {
	case 64:
		return ed25519.NewKeyFromSeed(keyBytes[:32]), nil
	case 32:
		return ed25519.NewKeyFromSeed(keyBytes), nil
	}
	return nil, fmt.Errorf("invalid Sparkle key length: %d bytes (expected 32 or 64)", len(keyBytes))
}

// SignFile signs a file with the given key, returning the base64 signature
// and the file length (the appcast needs both).
func SignFile(path string, key ed25519.PrivateKey) (string, int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0, err
	}
	signature := ed25519.Sign(key, data)
	return base64.StdEncoding.EncodeToString(signature), int64(len(data)), nil
}

// SignFileWithEnv signs using SPARKLE_PRIVATE_KEY from the environment
// (common/sparkle.py sparkle_sign_file).
func SignFileWithEnv(path string) (string, int64, error) {
	keyData := envx.SparklePrivateKey()
	if keyData == "" {
		return "", 0, fmt.Errorf("SPARKLE_PRIVATE_KEY not set")
	}
	key, err := ParsePrivateKey(keyData)
	if err != nil {
		return "", 0, err
	}
	return SignFile(path, key)
}
