// Package sec holds the crypto helpers: secrets at rest (AES-256-GCM), password hashing (scrypt), TOTP (RFC 6238).
// Formats are byte-compatible with the earlier Node version.
package sec

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/scrypt"

	"fleetdeck/internal/db"
)

var master []byte

// Init loads the master key (FLEETDECK_SECRET, else DATA_DIR/secret.key, created on first run) and refuses a key
// that doesn't match the stored data. Call after db.Open.
func Init() error {
	if s := os.Getenv("FLEETDECK_SECRET"); s != "" {
		k, err := scrypt.Key([]byte(s), []byte("fleetdeck-master"), 16384, 8, 1, 32) // Node's scryptSync defaults
		if err != nil {
			return err
		}
		master = k
	} else {
		file := filepath.Join(db.DataDir, "secret.key")
		if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(file, []byte(base64.StdEncoding.EncodeToString(Random(32))), 0o600); err != nil {
				return err
			}
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil || len(k) != 32 {
			return fmt.Errorf("%s is not a base64 32-byte key", file)
		}
		master = k
	}
	return checkMasterKey()
}

// Refuse to start with a different master key than the data was encrypted with (e.g. FLEETDECK_SECRET added later):
// otherwise every stored key and token would fail to decrypt one by one at runtime.
func checkMasterKey() error {
	canary := db.Setting("key_check", "")
	if canary == "" { // data from before this check has no canary: verify against any existing secret instead
		if v, err := db.Get[string](`SELECT private_enc v FROM keys UNION ALL SELECT config_enc FROM notifiers
			UNION ALL SELECT token_enc FROM repos WHERE token_enc IS NOT NULL UNION ALL SELECT value FROM settings WHERE key = 'totp_secret' LIMIT 1`); err != nil {
			return err
		} else if v != nil {
			canary = *v
		}
	}
	if canary != "" {
		if _, err := Decrypt(canary); err != nil {
			if os.Getenv("FLEETDECK_SECRET") != "" {
				return errors.New("FLEETDECK_SECRET does not match the key this data was encrypted with. Use the previous secret (or unset it to use data/secret.key)")
			}
			return fmt.Errorf("%s does not match the key this data was encrypted with. Restore the original secret.key or set the FLEETDECK_SECRET that was used", filepath.Join(db.DataDir, "secret.key"))
		}
	}
	if db.Setting("key_check", "") == "" {
		return db.SetSetting("key_check", Encrypt("fleetdeck"))
	}
	return nil
}

// Random returns n random bytes.
func Random(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return b
}

func gcm() cipher.AEAD {
	if master == nil {
		panic("sec.Init was not called")
	}
	block, _ := aes.NewCipher(master)
	g, _ := cipher.NewGCM(block)
	return g
}

// Encrypt returns "v1:" + base64(iv[12] | tag[16] | ciphertext).
func Encrypt(plain string) string {
	iv := Random(12)
	sealed := gcm().Seal(nil, iv, []byte(plain), nil) // ciphertext | tag
	ct, tag := sealed[:len(sealed)-16], sealed[len(sealed)-16:]
	out := append(append(iv, tag...), ct...)
	return "v1:" + base64.StdEncoding.EncodeToString(out)
}

// Decrypt reverses Encrypt; it fails on a wrong key or tampered data.
func Decrypt(blob string) (string, error) {
	if len(blob) < 3 {
		return "", errors.New("invalid encrypted value")
	}
	raw, err := base64.StdEncoding.DecodeString(blob[3:])
	if err != nil || len(raw) < 28 {
		return "", errors.New("invalid encrypted value")
	}
	iv, tag, ct := raw[:12], raw[12:28], raw[28:]
	plain, err := gcm().Open(nil, iv, append(append([]byte{}, ct...), tag...), nil)
	if err != nil {
		return "", errors.New("unable to decrypt (wrong key or tampered data)")
	}
	return string(plain), nil
}

// HashPassword returns "scrypt$N$salt$hash" (base64 parts).
func HashPassword(pw string) string {
	salt := Random(16)
	h, err := scrypt.Key([]byte(pw), salt, 32768, 8, 1, 32)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("scrypt$32768$%s$%s", base64.StdEncoding.EncodeToString(salt), base64.StdEncoding.EncodeToString(h))
}

// VerifyPassword checks pw against a HashPassword result in constant time.
func VerifyPassword(pw, stored string) bool {
	p := strings.Split(stored, "$")
	if len(p) != 4 || p[0] != "scrypt" {
		return false
	}
	n, err1 := strconv.Atoi(p[1])
	salt, err2 := base64.StdEncoding.DecodeString(p[2])
	want, err3 := base64.StdEncoding.DecodeString(p[3])
	if err1 != nil || err2 != nil || err3 != nil || n < 2 || n > 1<<20 || len(want) == 0 {
		return false
	}
	got, err := scrypt.Key([]byte(pw), salt, n, 8, 1, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// Token is 32 random bytes, base64url without padding.
func Token() string { return base64.RawURLEncoding.EncodeToString(Random(32)) }

// SHA256 is the lowercase hex digest of s.
func SHA256(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

const b32 = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"

// Base32 encodes without padding (RFC 4648 alphabet), as authenticator apps expect.
func Base32(buf []byte) string {
	var bits, value uint
	var out strings.Builder
	for _, c := range buf {
		value = value<<8 | uint(c)
		bits += 8
		for bits >= 5 {
			out.WriteByte(b32[(value>>(bits-5))&31])
			bits -= 5
		}
	}
	if bits > 0 {
		out.WriteByte(b32[(value<<(5-bits))&31])
	}
	return out.String()
}

func unbase32(s string) []byte {
	var bits, value uint
	var out []byte
	for _, ch := range strings.ToUpper(strings.TrimRight(s, "=")) {
		i := strings.IndexRune(b32, ch)
		if i < 0 {
			continue
		}
		value = value<<5 | uint(i)
		bits += 5
		if bits >= 8 {
			out = append(out, byte(value>>(bits-8)))
			bits -= 8
		}
	}
	return out
}

// TOTP is the 6-digit code for a base32 secret at a 30-second counter.
func TOTP(secret string, counter int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(counter))
	m := hmac.New(sha1.New, unbase32(secret))
	m.Write(msg[:])
	h := m.Sum(nil)
	o := h[len(h)-1] & 15
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(h[o:])&0x7fffffff)%1_000_000)
}

var sixDigits = regexp.MustCompile(`^[0-9]{6}$`)

// VerifyTOTP returns the matched counter (so callers can refuse reuse). Accepts ±1 step of clock drift.
func VerifyTOTP(secret, code string, now time.Time) (int64, bool) {
	if !sixDigits.MatchString(code) {
		return 0, false
	}
	c := now.UnixMilli() / 30000
	for _, d := range []int64{0, -1, 1} {
		if subtle.ConstantTimeCompare([]byte(TOTP(secret, c+d)), []byte(code)) == 1 {
			return c + d, true
		}
	}
	return 0, false
}

// NewTOTPSecret returns a random 160-bit base32 secret.
func NewTOTPSecret() string { return Base32(Random(20)) }
