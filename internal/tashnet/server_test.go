package tashnet

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/tash/tash/internal/config"
)

func freeControlPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

func TestJoinApprovalHeartbeatAndRevocation(t *testing.T) {
	t.Setenv("TASH_HOME", t.TempDir())

	secret, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Tashnet.Role = "main"
	cfg.Tashnet.ControlPort = freeControlPort(t)
	cfg.Tashnet.Devices[cfg.Tashnet.DeviceID] = config.Device{
		ID: cfg.Tashnet.DeviceID, Name: cfg.Tashnet.DeviceName, Token: NewToken(),
	}
	store, err := config.Create(cfg, secret)
	if err != nil {
		t.Fatalf("config.Create: %v", err)
	}

	srv := NewServer(store, "test")
	if err := srv.Start(); err != nil {
		t.Fatalf("server start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Stop(ctx)
	})

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", cfg.Tashnet.ControlPort)
	client, err := NewClient(baseURL, "", "test")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	hello, err := client.Hello(ctx)
	if err != nil {
		t.Fatalf("Hello: %v", err)
	}
	if hello.App != "tash" || hello.MainDeviceID != cfg.Tashnet.DeviceID {
		t.Fatalf("unexpected hello: %+v", hello)
	}

	// A wrong secret must never create a pending request.
	otherSecret, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Join(ctx, otherSecret, "dev_wrong", "intruder"); err == nil {
		t.Fatal("join with the wrong secret was accepted")
	}

	join, err := client.Join(ctx, secret, "dev_laptop", "laptop")
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if join.Status != "pending" || join.PendingID == "" || join.PollToken == "" {
		t.Fatalf("unexpected join response: %+v", join)
	}
	pending := store.Snapshot().Tashnet.Pending
	if len(pending) != 1 {
		t.Fatalf("expected one pending request, got %d", len(pending))
	}
	if _, ok := pending[join.PendingID]; !ok {
		t.Fatal("pending request was not persisted")
	}

	poll, err := client.Poll(ctx, join.PendingID, join.PollToken, "dev_laptop")
	if err != nil {
		t.Fatalf("Poll pending: %v", err)
	}
	if poll.Status != "pending" {
		t.Fatalf("poll status = %q, want pending", poll.Status)
	}

	// This is the same mutation the TASHNET tab performs when the operator
	// approves a device.
	if err := store.Update(func(c *config.Config) error {
		p, ok := c.Tashnet.Pending[join.PendingID]
		if !ok {
			return errors.New("pending request vanished")
		}
		delete(c.Tashnet.Pending, join.PendingID)
		c.Tashnet.Devices[p.DeviceID] = config.Device{
			ID: p.DeviceID, Name: p.Name, OS: p.OS, Arch: p.Arch, IP: p.IP,
			Token: NewToken(), PollHash: p.PollHash, ApprovedAt: time.Now().UTC(), LastSeen: time.Now().UTC(),
		}
		return nil
	}); err != nil {
		t.Fatalf("approve: %v", err)
	}

	poll, err = client.Poll(ctx, join.PendingID, join.PollToken, "dev_laptop")
	if err != nil {
		t.Fatalf("Poll approved: %v", err)
	}
	if poll.Status != "approved" || poll.Token == "" {
		t.Fatalf("unexpected approved poll: %+v", poll)
	}
	if _, err := client.Poll(ctx, join.PendingID, "wrong-poll-token", "dev_laptop"); err == nil {
		t.Fatal("approved device token was returned without the poll token")
	}

	client.Token = poll.Token
	hb, err := client.Heartbeat(ctx)
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if !hb.OK || hb.DeviceID != "dev_laptop" {
		t.Fatalf("unexpected heartbeat: %+v", hb)
	}
	netw, err := client.Network(ctx)
	if err != nil {
		t.Fatalf("Network: %v", err)
	}
	if len(netw.Devices) != 2 {
		t.Fatalf("expected 2 approved devices, got %d", len(netw.Devices))
	}

	// An already-approved device that asks to join again must still wait for
	// explicit approval. Knowing the secret and a device ID is not enough.
	rejoinApproved, err := client.Join(ctx, secret, "dev_laptop", "laptop")
	if err != nil {
		t.Fatalf("rejoin approved device: %v", err)
	}
	if rejoinApproved.Status != "pending" {
		t.Fatalf("approved device rejoin status = %q, want pending", rejoinApproved.Status)
	}

	// Regenerating the secret invalidates every client token and rejects the
	// old key. This mirrors the EXTREME-warning action in the TASHNET tab.
	newSecret, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(c *config.Config) error {
		c.Tashnet.SecretVersion++
		c.Tashnet.Pending = map[string]config.Pending{}
		c.Tashnet.Devices = map[string]config.Device{
			c.Tashnet.DeviceID: {
				ID: c.Tashnet.DeviceID, Name: c.Tashnet.DeviceName,
				Token: NewToken(), ApprovedAt: time.Now().UTC(), LastSeen: time.Now().UTC(),
			},
		}
		return nil
	}); err != nil {
		t.Fatalf("regenerate: %v", err)
	}
	if err := store.SetSecret(newSecret); err != nil {
		t.Fatalf("SetSecret: %v", err)
	}

	if _, err := client.Heartbeat(ctx); !errors.Is(err, ErrRevoked) {
		t.Fatalf("old token heartbeat error = %v, want ErrRevoked", err)
	}
	if _, err := client.Join(ctx, secret, "dev_laptop", "laptop"); err == nil {
		t.Fatal("old secret was accepted after regeneration")
	}
	rejoin, err := client.Join(ctx, newSecret, "dev_laptop", "laptop")
	if err != nil {
		t.Fatalf("join with new secret: %v", err)
	}
	if rejoin.Status != "pending" {
		t.Fatalf("rejoin status = %q, want pending approval", rejoin.Status)
	}
}
