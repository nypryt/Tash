// Package tashnet implements the Tash mesh control plane: secret-key
// onboarding, main-device approvals, heartbeats, and role transfer.
package tashnet

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const (
	// SecretPrefix makes keys recognisable in logs and group chats.
	SecretPrefix = "TASH"
	// ProofContext binds a join proof to its purpose and device.
	ProofContext = "tash-join-v1"
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateSecret creates a 160-bit network key formatted as
// TASH-XXXX-XXXX-XXXX-XXXX-XXXX-XXXX-XXXX.
func GenerateSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return formatSecret(b32.EncodeToString(b)), nil
}

// NormalizeSecret accepts pasted keys with spaces, lowercase, or a missing
// TASH prefix and returns the canonical form.
func NormalizeSecret(raw string) (string, error) {
	s := strings.ToUpper(strings.TrimSpace(raw))
	s = strings.NewReplacer(" ", "", "\t", "", "\n", "", "\r", "", "_", "").Replace(s)
	s = strings.TrimPrefix(s, SecretPrefix+"-")
	s = strings.TrimPrefix(s, SecretPrefix)
	s = strings.ReplaceAll(s, "-", "")
	if s == "" {
		return "", fmt.Errorf("secret key is empty")
	}
	b, err := b32.DecodeString(s)
	if err != nil || len(b) != 20 {
		return "", fmt.Errorf("invalid secret key (expected 8 groups of 4 characters)")
	}
	return formatSecret(b32.EncodeToString(b)), nil
}

// MaskSecret reveals only the first and last group for shoulder-safe display.
func MaskSecret(secret string) string {
	n, err := NormalizeSecret(secret)
	if err != nil {
		return "TASH-****-****-****-****-****-****-****"
	}
	parts := strings.Split(strings.TrimPrefix(n, SecretPrefix+"-"), "-")
	if len(parts) < 8 {
		return n
	}
	return fmt.Sprintf("%s-%s-%s-****-****-****-****-%s-%s", SecretPrefix, parts[0], parts[1], parts[6], parts[7])
}

func formatSecret(encoded string) string {
	parts := make([]string, 0, 8)
	for len(encoded) > 4 {
		parts = append(parts, encoded[:4])
		encoded = encoded[4:]
	}
	if encoded != "" {
		parts = append(parts, encoded)
	}
	return SecretPrefix + "-" + strings.Join(parts, "-")
}

// JoinProof returns a timestamp-bound HMAC proving knowledge of the network
// key without transmitting the key itself.
func JoinProof(secret, deviceID string, unixTime int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%s|%s|%d", ProofContext, deviceID, unixTime)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// VerifyJoinProof validates a join proof and clock skew.
func VerifyJoinProof(secret, deviceID string, unixTime int64, proof string) error {
	if secret == "" {
		return fmt.Errorf("no network secret configured")
	}
	skew := time.Since(time.Unix(unixTime, 0))
	if skew < 0 {
		skew = -skew
	}
	if skew > 3*time.Minute {
		return fmt.Errorf("join proof expired; check both devices' clocks")
	}
	want := JoinProof(secret, deviceID, unixTime)
	if subtle.ConstantTimeCompare([]byte(want), []byte(proof)) != 1 {
		return fmt.Errorf("secret key does not match this Tashnet")
	}
	return nil
}

// TransferProof authenticates a handover request to the old main device.
func TransferProof(secret, nonce, newURL, newDeviceID string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "tash-transfer-v1|%s|%s|%s", nonce, newURL, newDeviceID)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// VerifyTransferProof checks a handover proof.
func VerifyTransferProof(secret, nonce, newURL, newDeviceID, proof string) bool {
	want := TransferProof(secret, nonce, newURL, newDeviceID)
	return subtle.ConstantTimeCompare([]byte(want), []byte(proof)) == 1
}

// NewToken returns an opaque 256-bit bearer token.
func NewToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// HashToken hashes a pairing poll token before persisting it.
func HashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

// NewDeviceID returns a short opaque device identifier.
func NewDeviceID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("dev_%d", time.Now().UnixNano())
	}
	return "dev_" + hex.EncodeToString(b)
}

// NewPendingID returns a pairing request identifier.
func NewPendingID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("pair_%d", time.Now().UnixNano())
	}
	return "pair_" + hex.EncodeToString(b)
}

// NewNonce returns a transfer nonce.
func NewNonce() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("n_%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// NormalizeURL converts host:port or a URL into a clean http base URL.
func NormalizeURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("main device address is empty")
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	s = strings.TrimRight(s, "/")
	if strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://") {
		return s, nil
	}
	return "", fmt.Errorf("unsupported address %q", raw)
}

// ContainsSecret is a cheap UI helper.
func ContainsSecret(s string) bool { return strings.Contains(strings.ToUpper(s), SecretPrefix+"-") }
