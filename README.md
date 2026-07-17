# rds-bridge

Single-binary local Postgres proxy to an RDS instance reachable only over AWS
SSM, using RDS **IAM authentication** — without changing your app's code.

Your app connects to `127.0.0.1:<listen_port>` with a static credential. The
proxy opens each backend connection to RDS with a freshly minted IAM token, so
there is no token-refresh loop, no PgBouncer, no Docker, no certificate files.

A **target is one RDS instance** (one SSM tunnel) exposed on **one local port**.
The client chooses which database to connect to (via `dbname` / `DB_DATABASE`),
so that single port serves **every database** on the instance — the IAM token is
per user/host, not per database.

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

## SSO login

`start` checks both profiles' credentials first. If an SSO session has expired
and you're on a terminal, it runs `aws sso login` for you before bringing the
tunnel up — grouped by SSO session, so profiles sharing a session trigger a
single browser prompt. With `--detach` the login happens in the foreground
parent, then the proxy detaches with a refreshed token cache. Without a TTY
(cron, scripts) it doesn't open a browser — it fails with the exact
`aws sso login` command to run.

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
rds-bridge start my-rds-dev           # foreground (Ctrl-C to stop)
rds-bridge start my-rds-dev --detach
rds-bridge status my-rds-dev          # process + proxy port reachability
rds-bridge logs my-rds-dev -f
rds-bridge stop my-rds-dev
eval "$(rds-bridge env my-rds-dev)"           # DB_* without DB_DATABASE (app sets its own)
eval "$(rds-bridge env my-rds-dev app-db)"    # or fill DB_DATABASE for convenience
```

The app selects the database on connect (`dbname=…`, `psql -d …`, or
`DB_DATABASE`); the same port reaches any database on the instance. `env`'s
database argument is optional and only fills `DB_DATABASE`.

## Notes

- `local.user` / `local.password` are only the proxy-facing static credential.
- `iam.token_host` may differ from `ssm.remote_host` — the token is signed for
  the real RDS endpoint while the TCP connection goes through the tunnel.
- TLS to RDS uses `InsecureSkipVerify` (mirrors `DB_SSL_REJECT_UNAUTHORIZED=false`).
