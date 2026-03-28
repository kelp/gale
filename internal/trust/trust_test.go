package trust

import (
	"encoding/base64"
	"testing"
)

// --- Behavior 1: Generate ed25519 keypair ---

func TestGenerateKeyPairReturnsNonNil(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kp == nil {
		t.Fatal("expected non-nil KeyPair")
	}
}

func TestGenerateKeyPairPublicKeyNotEmpty(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kp.PublicKey == "" {
		t.Error("PublicKey is empty")
	}
}

func TestGenerateKeyPairPrivateKeyNotEmpty(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if kp.PrivateKey == "" {
		t.Error("PrivateKey is empty")
	}
}

func TestGenerateKeyPairPublicKeyIsValidBase64(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, err = base64.StdEncoding.DecodeString(kp.PublicKey)
	if err != nil {
		t.Errorf("PublicKey is not valid base64: %v", err)
	}
}

func TestGenerateKeyPairPrivateKeyIsValidBase64(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, err = base64.StdEncoding.DecodeString(kp.PrivateKey)
	if err != nil {
		t.Errorf("PrivateKey is not valid base64: %v", err)
	}
}

func TestGenerateKeyPairPublicKeyLength(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(kp.PublicKey)
	if err != nil {
		t.Fatalf("PublicKey is not valid base64: %v", err)
	}
	// ed25519 public keys are 32 bytes.
	if len(raw) != 32 {
		t.Errorf("PublicKey decoded length = %d, want 32", len(raw))
	}
}

func TestGenerateKeyPairPrivateKeyLength(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(kp.PrivateKey)
	if err != nil {
		t.Fatalf("PrivateKey is not valid base64: %v", err)
	}
	// ed25519 private keys are 64 bytes.
	if len(raw) != 64 {
		t.Errorf("PrivateKey decoded length = %d, want 64", len(raw))
	}
}

func TestGenerateKeyPairUniqueness(t *testing.T) {
	kp1, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("first GenerateKeyPair: %v", err)
	}
	kp2, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("second GenerateKeyPair: %v", err)
	}
	if kp1.PublicKey == kp2.PublicKey {
		t.Error("two generated keypairs have identical PublicKey")
	}
	if kp1.PrivateKey == kp2.PrivateKey {
		t.Error("two generated keypairs have identical PrivateKey")
	}
}

// --- Behavior 2: Sign data ---

func TestSignReturnsNonEmptySignature(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	sig, err := Sign([]byte("hello world"), kp.PrivateKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig == "" {
		t.Error("signature is empty")
	}
}

func TestSignReturnsValidBase64(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	sig, err := Sign([]byte("hello world"), kp.PrivateKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, err = base64.StdEncoding.DecodeString(sig)
	if err != nil {
		t.Errorf("signature is not valid base64: %v", err)
	}
}

func TestSignSignatureLength(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	sig, err := Sign([]byte("hello world"), kp.PrivateKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		t.Fatalf("signature is not valid base64: %v", err)
	}
	// ed25519 signatures are 64 bytes.
	if len(raw) != 64 {
		t.Errorf("signature decoded length = %d, want 64", len(raw))
	}
}

func TestSignDeterministicForSameKeyAndData(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	data := []byte("deterministic signing")
	sig1, err := Sign(data, kp.PrivateKey)
	if err != nil {
		t.Fatalf("first Sign: %v", err)
	}
	sig2, err := Sign(data, kp.PrivateKey)
	if err != nil {
		t.Fatalf("second Sign: %v", err)
	}
	if sig1 != sig2 {
		t.Error("same key and data produced different signatures")
	}
}

func TestSignErrorOnInvalidPrivateKey(t *testing.T) {
	_, err := Sign([]byte("hello"), "not-valid-base64!!!")
	if err == nil {
		t.Error("expected error for invalid private key")
	}
}

// --- Behavior 3: Verify signature ---

func TestVerifyValidSignature(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	data := []byte("hello world")
	sig, err := Sign(data, kp.PrivateKey)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	ok, err := Verify(data, sig, kp.PublicKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("Verify = false, want true")
	}
}

// --- Behavior 4: Reject invalid signature ---

func TestVerifyWrongPublicKey(t *testing.T) {
	kp1, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair 1: %v", err)
	}
	kp2, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair 2: %v", err)
	}

	data := []byte("hello world")
	sig, err := Sign(data, kp1.PrivateKey)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	ok, err := Verify(data, sig, kp2.PublicKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("Verify = true with wrong public key, want false")
	}
}

func TestVerifyTamperedData(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	sig, err := Sign([]byte("original data"), kp.PrivateKey)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	ok, err := Verify([]byte("tampered data"), sig, kp.PublicKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("Verify = true with tampered data, want false")
	}
}

func TestVerifyCorruptedSignature(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	data := []byte("hello world")

	// A corrupted signature: not valid base64.
	ok, err := Verify(data, "not-valid-base64!!!", kp.PublicKey)
	// Either err != nil or ok == false is acceptable.
	if ok {
		t.Error("Verify = true with corrupted signature, want false")
	}
	_ = err
}

func TestVerifyWrongLengthSignature(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	data := []byte("hello world")
	// Valid base64 but wrong length for an ed25519 signature.
	shortSig := base64.StdEncoding.EncodeToString([]byte("short"))

	ok, err := Verify(data, shortSig, kp.PublicKey)
	// Either err != nil or ok == false is acceptable.
	if ok {
		t.Error("Verify = true with wrong-length signature, want false")
	}
	_ = err
}

func TestVerifyInvalidPublicKey(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	data := []byte("hello world")
	sig, err := Sign(data, kp.PrivateKey)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	ok, err := Verify(data, sig, "not-valid-base64!!!")
	// Either err != nil or ok == false is acceptable.
	if ok {
		t.Error("Verify = true with invalid public key, want false")
	}
	_ = err
}

// --- Behavior 5: Round-trip ---

func TestRoundTrip(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	data := []byte("round-trip test payload")
	sig, err := Sign(data, kp.PrivateKey)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	ok, err := Verify(data, sig, kp.PublicKey)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !ok {
		t.Error("round-trip failed: Verify returned false")
	}
}

func TestRoundTripEmptyData(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	data := []byte{}
	sig, err := Sign(data, kp.PrivateKey)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	ok, err := Verify(data, sig, kp.PublicKey)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !ok {
		t.Error("round-trip with empty data failed")
	}
}

func TestRoundTripLargeData(t *testing.T) {
	kp, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("GenerateKeyPair: %v", err)
	}

	data := make([]byte, 1024*1024) // 1 MiB of zeros
	sig, err := Sign(data, kp.PrivateKey)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	ok, err := Verify(data, sig, kp.PublicKey)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !ok {
		t.Error("round-trip with large data failed")
	}
}

// --- Behavior 6: Embedded public key ---

func TestRecipePublicKeyNotEmpty(t *testing.T) {
	key := RecipePublicKey()
	if key == "" {
		t.Fatal("RecipePublicKey() returned empty string")
	}
}

func TestRecipePublicKeyIsValidBase64(t *testing.T) {
	key := RecipePublicKey()
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		t.Fatalf("RecipePublicKey is not valid base64: %v", err)
	}
	if len(raw) != 32 {
		t.Errorf("decoded length = %d, want 32", len(raw))
	}
}
