# Tashnet protocol

Tashnet is Tash's small mesh control plane. It is deliberately simpler than a
general-purpose overlay network: Tailscale/WireGuard moves packets, and Tashnet
decides which devices are allowed into the Tash dashboard network.

The main device runs an HTTP control server on port **8787** while
`tash dashboard` is open. Clients store a bearer token; the network Secret Key
lives only on the main device.

## Roles

- **main** — owns the Secret Key, runs the control server, approves joins, and
  can hand the role to another device.
- **client** — stores the main URL and an approved random token. It sends
  heartbeats and reacts to revocation.

The first device configured becomes main. The role can be moved later with a
one-time transfer code.

## Secret key

```text
TASH-XXXX-XXXX-XXXX-XXXX-XXXX-XXXX-XXXX
```

The key is 160 bits of `crypto/rand` data encoded with base32. Tash accepts
pasted keys with spaces, lowercase letters, or a missing `TASH` prefix and
normalizes them. The canonical form is uppercase and dash-separated.

The key is stored in `secret.key` beside `config.json` with mode `0600`. Client
configs contain no secret.

## Joining

1. The client normalizes the Secret Key and sends `POST /v1/join` with:

   ```json
   {
     "device_id": "dev_…",
     "device_name": "laptop",
     "os": "linux",
     "arch": "amd64",
     "timestamp": 1770000000,
     "proof": "base64url HMAC-SHA256"
   }
   ```

2. The proof is `HMAC-SHA256(secret, "tash-join-v1|" + device_id + "|" + timestamp)`.
   The secret itself is never sent.
3. The main verifies the proof, rejects stale timestamps, rate-limits attempts
   by IP, and creates a pending request with a one-time `pending_id` and
   `poll_token`. The poll token is stored only as a hash.
4. A human opens the main device's **TASHNET** tab and presses `a` to approve or
   `d` to deny. The main device must be running `tash dashboard`.
5. The client polls `POST /v1/poll` every two seconds. On approval it receives
   the bearer token and main URL. On denial it returns to the address step.
   The poll token is stored as a hash on the pending request and copied to the
   approved device, so a device ID alone can never retrieve the bearer token.

Every join request, including one from a previously approved device, becomes a
pending request until the main device approves it. Re-requesting replaces that
device's older pending request.

Pending requests expire after 20 minutes. At most 32 can be queued.

## Authenticated calls

Clients send `Authorization: Bearer <token>` to:

- `POST /v1/heartbeat` — update `last_seen`, return main name and secret version.
- `GET /v1/network` — list approved devices and pending requests.
- `GET /v1/whoami` — identify the caller.

The main verifies the token with constant-time comparison. Tokens are random
32-byte values encoded for transport.

If a token is missing or revoked, the client shows an uplink error and instructs
the operator to run `tash join` again.

## Discovery

Clients can find a main device on the local tailnet by probing the control port.
The TUI uses this when the address field is empty. Discovery is best-effort:
unrooted Termux cannot read the Tailscale CLI, so paste the main URL instead.

The advertised URL is the host's Tailscale IPv4 address when available,
otherwise a local address. Use `-url` when the automatic choice is wrong.

## Secret regeneration

`tash secret regen` and the TASHNET tab's `g` action:

1. Require the operator to type `REGENERATE`.
2. Generate a new Secret Key.
3. Increment `secret_version`.
4. Replace the approved device map with only the main device, with a fresh token.
5. Clear pending requests and any transfer nonce.
6. Write the new key to `secret.key`.

Every client heartbeat then fails. Clients must rejoin with the new key and be
approved again. The running main control server reads the updated store, so it
starts enforcing the new key immediately.

## Main-role transfer

On the old main:

```sh
tash main transfer
```

This creates a 10-minute one-time code with prefix `TASHXL1-`. It is gzip +
base64url JSON containing:

- the Secret Key and secret version,
- the control port,
- the old main's advertised URL, device ID, and name,
- a nonce stored on the old main,
- every approved device and token.

On the new device:

```sh
tash main claim -url http://<new-ip>:8787 '<code>'
```

The new device installs the main configuration and secret, then calls
`POST /v1/transfer/complete` on the old main with a proof derived from the
secret, nonce, new URL, and new device ID. The old main verifies the nonce and
proof, then rewrites itself as a client of the new main.

If the old main is unreachable, the claim still succeeds but both devices may
believe they are main. Tash prints a split-brain warning. Resolve it by stopping
the old main's dashboard and rejoining it as a client.

Transfer codes are bearer secrets. Anyone who obtains one can claim main. Do not
paste them into chat logs, tickets, or shell history on shared machines.

## Trust boundary and limitations

- The control API is HTTP, not HTTPS. It relies on Tailscale/WireGuard or a
  trusted LAN for confidentiality and authenticity.
- Approval is manual by design. A stolen Secret Key is not enough to silently
  join, but it is enough to spam join requests (rate-limited).
- There is no certificate pinning or TOFU store. The main URL is trusted as
  configured.
- Revocation is eventual: a client learns about it on its next heartbeat.
- The protocol is versioned by URL (`/v1/…`) and by the `version` field in the
  transfer payload. Unknown versions are rejected.
- Never expose ports 8787 or 8788 directly to the public internet. Use Tailscale
  or a firewall/VPN.
