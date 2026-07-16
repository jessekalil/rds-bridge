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

Runs natively on **Linux, macOS and Windows** (CMD/PowerShell) — no WSL required.

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
  child (and the session-manager-plugin it spawns) is isolated — a process
  group on Unix, a Job Object on Windows — so it is killed cleanly on shutdown,
  with no orphans and without needing administrator rights.

---

# Linux / macOS

## Requirements

- `aws` CLI v2 + `session-manager-plugin` on `PATH` (for the SSM tunnel)
- AWS profiles with permission to start the SSM session and to generate the RDS
  IAM token (these can be two different profiles)

## Install

```bash
make install          # go install into $GOBIN
# or
make build            # ./bin/rds-bridge
# or cross-compile
make release          # dist/rds-bridge-<os>-<arch>[.exe]
```

Without `make`, the Go toolchain works too:

```bash
go install github.com/jessekalil/rds-bridge@latest
# or
go build -o bin/rds-bridge .
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

---

# Windows (CMD / PowerShell)

For users on native Windows **without WSL**. No Go toolchain required — you run
a prebuilt `rds-bridge.exe`.

> Admin rights are **not** needed. `start --detach`, `stop`, `logs` and `status`
> all work as a normal user.

## First-time setup

Do these once. The examples use `C:\Users\<you>\rds-bridge\` as the install
folder — replace `<you>` with your Windows username.

**1. Install the AWS tooling.** Install **AWS CLI v2 for Windows** and the
**Session Manager plugin for Windows**, and make sure both are on `PATH`
(`aws.exe` / `session-manager-plugin.exe`).

**2. Configure AWS SSO (required before first use).** Log in so the tool can
open the SSM session and mint the RDS IAM token:

```bat
aws configure sso
```

Use the same profile names that appear in `rds-bridge.yaml`. Re-run
`aws configure sso` whenever the session expires.

**3. Download the binary.** Grab `rds-bridge-windows-amd64.exe` from the
project's Releases, rename it to `rds-bridge.exe`, and put it in
`C:\Users\<you>\rds-bridge\`. Optionally add that folder to your `PATH`.

**4. Copy the config into the same folder.** Copy `rds-bridge.example.yaml`
into `C:\Users\<you>\rds-bridge\`, rename it to `rds-bridge.yaml`, and edit it:

```bat
cd C:\Users\<you>\rds-bridge
copy rds-bridge.example.yaml rds-bridge.yaml
```

`rds-bridge` auto-discovers `rds-bridge.yaml` in the current folder, so running
from `C:\Users\<you>\rds-bridge\` needs no `--config` flag. State (pid/log) is
written to `%USERPROFILE%\.local\state\rds-bridge\`.

> Config discovery order: `--config` → `%RDS_BRIDGE_CONFIG%` →
> `rds-bridge.yaml` in the current folder → `%USERPROFILE%\.config\rds-bridge\config.yaml`.

## Build from source (maintainers)

End users don't need this. To produce the Windows binary from Linux/macOS or WSL:

```bash
make release          # -> dist/rds-bridge-windows-amd64.exe
```

Or natively on Windows with the Go toolchain (PowerShell):

```powershell
.\build.ps1           # -> bin\rds-bridge.exe
```

## Usage (CMD)

```bat
cd C:\Users\<you>\rds-bridge

rds-bridge list                          :: configured targets
rds-bridge list plataforma-dev           :: that target's databases + ports
rds-bridge start plataforma-dev          :: foreground (Ctrl+C to stop)
rds-bridge start plataforma-dev --detach :: background
rds-bridge status plataforma-dev
rds-bridge logs plataforma-dev -f
rds-bridge stop plataforma-dev

:: load DB_* into the current CMD session
rds-bridge env plataforma-dev --shell cmd > env.bat && call env.bat
```

In **PowerShell**, load the variables like this instead:

```powershell
rds-bridge env plataforma-dev --shell powershell | Invoke-Expression
```

`env` emits the right syntax per shell via `--shell posix|powershell|cmd`
(default: `powershell` on Windows). It takes a database name, which can be
omitted only when the target has a single database.

---

## Notes

- `local.user` / `local.password` are only the proxy-facing static credential
  (safe to share across users); they are **not** your AWS secret.
- `iam.token_host` may differ from `ssm.remote_host` — the token is signed for
  the real RDS endpoint while the TCP connection goes through the tunnel.
- TLS to RDS uses `InsecureSkipVerify` (mirrors `DB_SSL_REJECT_UNAUTHORIZED=false`).
