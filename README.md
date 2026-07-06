# rds-bridge

Single-binary local Postgres proxy to an RDS instance reachable only over AWS
SSM, using RDS **IAM authentication** — without changing your app's code.

Your app connects to `127.0.0.1:<listen_port>` with a static credential. The
proxy opens each backend connection to RDS with a freshly minted IAM token, so
there is no token-refresh loop, no PgBouncer, no Docker, no certificate files.

A **target is one RDS instance** (one SSM tunnel) and can expose **several
databases** on the instance, each on its own local port — the single tunnel is
shared (the IAM token is per user/host, not per database).

```
app ──▶ rds-bridge proxy ──▶ aws ssm tunnel ──▶ RDS
        (static cred)        (supervised)       (IAM token per connection)
```

## How it works

- **Frontend**: speaks the Postgres wire protocol to your app, validates the
  static credential, and supports SSL with an in-memory self-signed cert
  (`sslmode=allow`/`require` both work).
- **Backend**: uses `pgconn` to connect to RDS through the SSM tunnel — TLS and
  auth are negotiated by the driver — then hijacks the raw connection and pipes
  bytes (session pooling, 1 client : 1 backend).
- **Token**: `BuildAuthToken` signs an IAM token offline per connection. Tokens
  only matter at connect time, so nothing needs refreshing.
- **Tunnel**: supervises `aws ssm start-session`, restarting it on exit. The
  child runs in its own process group and is killed cleanly on shutdown.

## Requirements

- `aws` CLI v2 + `session-manager-plugin` (for the SSM tunnel)
- AWS profiles with permission to start the SSM session and to generate the RDS
  IAM token (these can be two different profiles)

## Install

```bash
make install          # go install into $GOBIN
# or
make build            # ./bin/rds-bridge
# or cross-compile
make release          # dist/rds-bridge-<os>-<arch>
```

## Configure

Copy the example and edit:

```bash
cp rds-bridge.example.yaml rds-bridge.yaml
```

Config is discovered via `--config`, then `$RDS_BRIDGE_CONFIG`,
`./rds-bridge.yaml`, then `~/.config/rds-bridge/config.yaml`.

## Usage

```bash
rds-bridge list                       # show configured targets (instances)
rds-bridge list plataforma-dev        # show that target's databases + ports
rds-bridge start plataforma-dev       # foreground: one tunnel, all DB listeners
rds-bridge start plataforma-dev --detach
rds-bridge status plataforma-dev      # one line per database
rds-bridge logs plataforma-dev -f
rds-bridge stop plataforma-dev
eval "$(rds-bridge env plataforma-dev avalia-online-dev)"   # export DB_* for one database
```

`env` takes a database name; it can be omitted only when the target has a single
database.

## Notes

- `local.user` / `local.password` are only the proxy-facing static credential.
- `iam.token_host` may differ from `ssm.remote_host` — the token is signed for
  the real RDS endpoint while the TCP connection goes through the tunnel.
- TLS to RDS uses `InsecureSkipVerify` (mirrors `DB_SSL_REJECT_UNAUTHORIZED=false`).
