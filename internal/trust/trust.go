package trust

import (
	"crypto/ed25519"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"fmt"
	"strings"
)

//go:embed pubkey.txt
var recipePublicKey string

// RecipePublicKey returns the embedded base64-encoded ed25519 public key
// used to verify recipe signatures.
func RecipePublicKey() string {
	return strings.TrimSpace(recipePublicKey)
}

// KeyPair represents an ed25519 keypair.
type KeyPair struct {
	PublicKey  string // base64-encoded
	PrivateKey string // base64-encoded
}

// GenerateKeyPair generates a new ed25519 keypair.
func GenerateKeyPair() (*KeyPair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate keypair: %w", err)
	}

	return &KeyPair{
		PublicKey:  base64.StdEncoding.EncodeToString(pub),
		PrivateKey: base64.StdEncoding.EncodeToString(priv),
	}, nil
}

// Sign signs data with the given base64-encoded private key.
func Sign(data []byte, privateKey string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(privateKey)
	if err != nil {
		return "", fmt.Errorf("decode private key: %w", err)
	}

	if len(raw) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("invalid private key length: %d", len(raw))
	}

	sig := ed25519.Sign(ed25519.PrivateKey(raw), data)
	return base64.StdEncoding.EncodeToString(sig), nil
}

// Verify checks a signature against data using a base64-encoded
// public key.
func Verify(data []byte, signature, publicKey string) (bool, error) {
	pubRaw, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil {
		return false, fmt.Errorf("decode public key: %w", err)
	}

	if len(pubRaw) != ed25519.PublicKeySize {
		return false, fmt.Errorf("invalid public key length: %d", len(pubRaw))
	}

	sigRaw, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return false, nil //nolint:nilerr // malformed signature is not an error, just invalid
	}

	if len(sigRaw) != ed25519.SignatureSize {
		return false, nil
	}

	return ed25519.Verify(ed25519.PublicKey(pubRaw), data, sigRaw), nil
}
