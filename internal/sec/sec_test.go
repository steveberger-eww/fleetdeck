package sec

import (
	"encoding/base64"
	"fmt"
	"os"
	"testing"
	"time"

	"fleetdeck/internal/db"
)

func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "fleetdeck-test-")
	if err := db.OpenTest(dir); err != nil {
		panic(err)
	}
	if err := Init(); err != nil {
		panic(err)
	}
	code := m.Run()
	db.DropTest()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestSecretsAtRest(t *testing.T) {
	blob := Encrypt("private key")
	if blob == Encrypt("private key") {
		t.Fatal("IV not random")
	}
	if p, err := Decrypt(blob); err != nil || p != "private key" {
		t.Fatalf("round trip: %q %v", p, err)
	}
	raw, _ := base64.StdEncoding.DecodeString(blob[3:])
	raw[len(raw)-1] ^= 1
	if _, err := Decrypt("v1:" + base64.StdEncoding.EncodeToString(raw)); err == nil {
		t.Fatal("GCM must detect tampering")
	}
}

// Ciphertext produced by the Node version (FLEETDECK_SECRET unset, key below) must still decrypt.
func TestNodeCompatibleCiphertext(t *testing.T) {
	old := master
	defer func() { master = old }()
	master, _ = base64.StdEncoding.DecodeString("AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=")
	if p, err := Decrypt(nodeBlob); err != nil || p != "hello from node" {
		t.Fatalf("got %q %v", p, err)
	}
}

func TestPasswords(t *testing.T) {
	h := HashPassword("correct horse")
	if !VerifyPassword("correct horse", h) || VerifyPassword("wrong", h) {
		t.Fatal("verify")
	}
	if !VerifyPassword("correct horse", nodeHash) {
		t.Fatal("Node scrypt hash must verify")
	}
}

func TestTOTPMatchesRFC6238(t *testing.T) {
	secret := Base32([]byte("12345678901234567890"))
	if secret != "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" {
		t.Fatal(secret)
	}
	if c := TOTP(secret, 1); c != "287082" { // T=59s
		t.Fatal(c)
	}
	at := time.UnixMilli(59_000)
	if c, ok := VerifyTOTP(secret, "287082", at); !ok || c != 1 {
		t.Fatal("valid code rejected")
	}
	for _, junk := range []string{"000000", "ééé", "12 345", "", "2870821"} {
		if _, ok := VerifyTOTP(secret, junk, at); ok {
			t.Fatalf("%q accepted", junk)
		}
	}
}

// FLEETDECK_SECRET must derive the same master key as Node's scryptSync(secret, 'fleetdeck-master', 32).
func TestEnvSecretDerivationMatchesNode(t *testing.T) {
	old := master
	defer func() { master = old; os.Unsetenv("FLEETDECK_SECRET"); db.SetSetting("key_check", nil) }()
	db.SetSetting("key_check", nil)
	os.Setenv("FLEETDECK_SECRET", "s3cret")
	if err := Init(); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", master); got != "59fc0f00c2f5d92e75a7c3ff64c00b683f7501dd6c9f8e5ed99e4b54e5d12461" {
		t.Fatal(got)
	}
}
