package tashnet

import (
	"strings"
	"testing"
	"time"

	"github.com/tash/tash/internal/config"
)

func TestGenerateAndNormalizeSecret(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	if !strings.HasPrefix(secret, SecretPrefix+"-") {
		t.Fatalf("secret missing prefix: %s", secret)
	}
	groups := strings.Split(strings.TrimPrefix(secret, SecretPrefix+"-"), "-")
	if len(groups) != 8 {
		t.Fatalf("expected 8 groups, got %d: %s", len(groups), secret)
	}
	for _, g := range groups {
		if len(g) != 4 {
			t.Fatalf("expected 4-char group, got %q in %s", g, secret)
		}
	}

	messy := " " + strings.ToLower(strings.ReplaceAll(secret, "-", " ")) + "\n"
	normalized, err := NormalizeSecret(messy)
	if err != nil {
		t.Fatalf("NormalizeSecret: %v", err)
	}
	if normalized != secret {
		t.Fatalf("normalize mismatch: got %s want %s", normalized, secret)
	}

	noPrefix := strings.TrimPrefix(secret, SecretPrefix+"-")
	normalized, err = NormalizeSecret(strings.ReplaceAll(noPrefix, "-", "_"))
	if err != nil {
		t.Fatalf("NormalizeSecret without prefix: %v", err)
	}
	if normalized != secret {
		t.Fatalf("normalize without prefix mismatch: got %s want %s", normalized, secret)
	}

	if _, err := NormalizeSecret("TASH-0000-0000"); err == nil {
		t.Fatal("expected invalid short secret to fail")
	}
	if _, err := NormalizeSecret(""); err == nil {
		t.Fatal("expected empty secret to fail")
	}
}

func TestJoinProof(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	proof := JoinProof(secret, "dev_test", now)
	if err := VerifyJoinProof(secret, "dev_test", now, proof); err != nil {
		t.Fatalf("valid proof rejected: %v", err)
	}
	if err := VerifyJoinProof(secret, "dev_other", now, proof); err == nil {
		t.Fatal("proof accepted for a different device")
	}
	if err := VerifyJoinProof(secret, "dev_test", now-600, proof); err == nil {
		t.Fatal("stale proof accepted")
	}
	other, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyJoinProof(other, "dev_test", now, proof); err == nil {
		t.Fatal("proof accepted with a different secret")
	}
}

func TestTransferRoundTrip(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	devices := map[string]config.Device{
		"dev_old":    {ID: "dev_old", Name: "old-main", Token: "old-token", ApprovedAt: time.Now().UTC()},
		"dev_client": {ID: "dev_client", Name: "laptop", Token: "client-token", ApprovedAt: time.Now().UTC()},
	}
	payload := TransferPayload{
		Version: 1, Network: "Tashnet", Secret: secret, SecretVersion: 3,
		ControlPort: 8787, FromURL: "http://100.64.0.1:8787",
		FromDeviceID: "dev_old", FromDeviceName: "old-main",
		Nonce: "nonce-123", ExpiresAt: time.Now().UTC().Add(5 * time.Minute), Devices: devices,
	}
	code, err := encodeTransfer(payload)
	if err != nil {
		t.Fatalf("encodeTransfer: %v", err)
	}
	if !strings.HasPrefix(code, transferPrefix) {
		t.Fatalf("missing transfer prefix: %s", code)
	}
	parsed, err := ParseTransfer(code)
	if err != nil {
		t.Fatalf("ParseTransfer: %v", err)
	}
	if parsed.Secret != secret || parsed.Nonce != payload.Nonce || len(parsed.Devices) != 2 {
		t.Fatalf("round trip mismatch: %+v", parsed)
	}
	claim, err := BuildClaim(parsed, "dev_new", "new-main")
	if err != nil {
		t.Fatalf("BuildClaim: %v", err)
	}
	if claim.Tashnet.Role != "main" || claim.Tashnet.MainDeviceID != "dev_new" {
		t.Fatalf("unexpected claim role: %+v", claim.Tashnet)
	}
	if len(claim.Tashnet.Devices) != 3 {
		t.Fatalf("expected old devices plus the new main, got %d", len(claim.Tashnet.Devices))
	}
	if claim.Tashnet.Devices["dev_client"].Token != "client-token" {
		t.Fatal("client token was not preserved")
	}
	if claim.Tashnet.Devices["dev_new"].Token == "" {
		t.Fatal("new main has no token")
	}
}

func TestTransferExpired(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	payload := TransferPayload{
		Version: 1, Network: "Tashnet", Secret: secret, SecretVersion: 1,
		ControlPort: 8787, FromURL: "http://100.64.0.1:8787",
		FromDeviceID: "dev_old", FromDeviceName: "old",
		Nonce: "nonce", ExpiresAt: time.Now().UTC().Add(-time.Minute),
		Devices: map[string]config.Device{"dev_old": {ID: "dev_old", Name: "old"}},
	}
	code, err := encodeTransfer(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseTransfer(code); err == nil {
		t.Fatal("expired transfer code accepted")
	}
	if _, err := ParseTransfer("not-a-code"); err == nil {
		t.Fatal("invalid transfer code accepted")
	}
}
