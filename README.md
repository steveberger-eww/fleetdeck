<div align="center">

<img src="assets/icon.svg" width="96" height="96" alt="Fleetdeck logo">

# Fleetdeck

**A self-hosted control panel for your Linux servers.**<br>
Monitoring, one-click maintenance and day-to-day administration in one place. Everything runs over SSH, so there's nothing to install on the servers.

![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white)
![Vue](https://img.shields.io/badge/Vue-3-4FC08D?logo=vuedotjs&logoColor=white)
![Postgres](https://img.shields.io/badge/PostgreSQL-18-4169E1?logo=postgresql&logoColor=white)
![Docker](https://img.shields.io/badge/Docker-ready-2496ED?logo=docker&logoColor=white)

[Features](#features) · [Getting started](#getting-started) · [Adding a server](#adding-a-server) · [Monitoring](#monitoring-and-alerts) · [Automation](#scripts-and-automation) · [Security](#security-model) · [How it works](#how-it-works)

<picture>
  <source media="(prefers-color-scheme: light)" srcset="assets/screenshots/overview-light.png">
  <img src="assets/screenshots/overview-dark.png" alt="Fleetdeck fleet overview: online servers, pending security updates, busiest servers and recent jobs">
</picture>

</div>

## Features

Fleetdeck combines the core ideas of [Beszel](https://github.com/henrygd/beszel), [Semaphore](https://github.com/semaphoreui/semaphore) and [Cockpit](https://cockpit-project.org) in one Go server, with multi-tenancy on top.

- **Monitoring.** Per-minute metrics with 30 days of history: CPU, memory, swap, disks and inodes per mount, disk and network I/O, load, temperature, NVIDIA and AMD GPUs, Docker containers, Proxmox guests and S.M.A.R.T. disk health. Add your own metrics from any shell command. Prometheus `/metrics` and CSV export.
- **Alerts.** Rules for all servers, a tag or one server, sent via ntfy, Pushover, Discord, Slack, Telegram, Gotify or a webhook. Endpoint and TLS certificate checks, quiet hours and maintenance windows.
- **Scripts and jobs.** A script library with parameters and encrypted shared variables. Run on servers, tags or everything, watch the output live, re-run failed hosts only. Chains, automatic retries, cron schedules and webhook triggers.
- **Ansible from Git.** Playbooks straight from your repositories, with dry runs and per-host results.
- **Server administration.** Package updates with history and OS end-of-life warnings, systemd services and timers, a fleet-wide journal search, processes, containers, open ports checked against the real firewall, a web terminal, and every authorized SSH key across the fleet.
- **Organizations.** Several tenants on one instance (for example an IT provider and each of its customers), with per-organization roles, invite links and single sign-on.

<table>
  <tr>
    <td width="50%"><img src="assets/screenshots/server.png" alt="Server page with 24 hours of CPU, memory, network, disk I/O and load charts"></td>
    <td width="50%"><img src="assets/screenshots/job.png" alt="Job page with per-server results and live output"></td>
  </tr>
  <tr>
    <td align="center"><sub>Per-server metrics and admin tabs</sub></td>
    <td align="center"><sub>Jobs keep each server's output and exit code</sub></td>
  </tr>
</table>

## Getting started

### Docker

Fleetdeck needs a PostgreSQL database. Point `DATABASE_URL` at it:

```sh
docker build -t fleetdeck .
docker run -d --name fleetdeck -p 127.0.0.1:8080:8080 -v fleetdeck:/data \
  -e DATABASE_URL='postgres://…' -e FLEETDECK_SECRET="$(openssl rand -base64 32)" fleetdeck
docker logs fleetdeck   # shows the setup token
```

On first start, the console prints a **setup token**. Open http://127.0.0.1:8080, enter the token, and create the first account (a super admin) and its organization.

> [!IMPORTANT]
> Fleetdeck listens on localhost by default. To reach it from elsewhere, put it behind a reverse proxy with HTTPS (Caddy, Traefik, nginx) and set `FLEETDECK_TRUST_PROXY=1`, or keep it on a VPN such as Tailscale or WireGuard.

### Build from source

You need Go 1.26+, Node 22+ and PostgreSQL. The UI is a separate build that the server reads from disk (`ui/dist`, or `FLEETDECK_UI_DIR`); it isn't compiled into the binary.

```sh
docker compose up -d   # local Postgres from compose.yaml
export DATABASE_URL='postgres://fleetdeck:fleetdeck@localhost:5432/fleetdeck?sslmode=disable'
npm --prefix ui ci && npm --prefix ui run build
go build -o fleetdeck .
./fleetdeck            # http://127.0.0.1:8080
```

**Working on the UI:** keep `./fleetdeck` running and start `npm --prefix ui run dev`. Vite serves the UI with hot reload on http://localhost:5173 and forwards `/api` (including the terminal WebSocket) to the Go server (`FLEETDECK_DEV_BACKEND`, default `http://127.0.0.1:8080`).

**Tests:** `go test ./...`. Each package works in its own throwaway schema of the compose database (or of `FLEETDECK_TEST_DATABASE_URL`).

### Configuration

| Variable | Default | Description |
|---|---|---|
| `HOST` / `PORT` | `127.0.0.1` / `8080` | Listen address |
| `DATABASE_URL` | – | PostgreSQL connection URL (required) |
| `DATA_DIR` | `./data` | `secret.key`, Git checkouts and temporary files |
| `FLEETDECK_UI_DIR` | `./ui/dist` | The built UI. The Docker image sets it to `/usr/share/fleetdeck/ui`. |
| `FLEETDECK_SECRET` | – | Master key for encrypting SSH keys and tokens. Without it, a random key is written to `DATA_DIR/secret.key`. |
| `FLEETDECK_TRUST_PROXY` | – | `1` trusts `X-Forwarded-*` headers (client IP, HTTPS detection for secure cookies) |
| `FLEETDECK_PUBLIC_URL` | – | Fleetdeck's address, e.g. `https://fleetdeck.example.com`, for single sign-on redirects and invite links. Can also be set under **Settings → Instance → General**. Without it, the address the browser used counts (fine for `localhost`). |
| `FLEETDECK_PASSWORD_LOGIN` | – | `1` allows password sign-in for accounts that turned it off in favour of single sign-on (for when your provider is down) |

### Single sign-on

Any OpenID Connect provider works (authentik, Keycloak, …), at two levels. Both are set up step by step in the app and use the same redirect URI.

- **Instance** (**Settings → Instance → Single sign-on**, super admins): e.g. your company's authentik. It signs in existing accounts (matched by connected identity or email) and can accept invites.
- **Organization** (**Settings → Organization → Single sign-on**, its admins): the organization's own provider, used from `https://…/?org=<short name>`. It signs in that organization's members only and can add new people with a default role. These sessions stay in that organization and can't change the account's password, 2FA or email, so an organization's provider can't take over accounts elsewhere.

### Moving from SQLite

Versions before Postgres kept everything in `DATA_DIR/fleetdeck.db`. When Fleetdeck starts with an empty Postgres database and finds that file, it imports it first (one transaction, row counts checked per table). Stop the old version and start the new one with `DATABASE_URL` set.

> [!CAUTION]
> Keep `DATA_DIR`: its `secret.key` (or your `FLEETDECK_SECRET`) decrypts the imported keys and tokens.

Everything lands in one organization. The former single owner becomes a super admin who signs in as **`admin`** with the old password (and 2FA); change the sign-in email under **Settings → Profile**. Built-in scripts become the shared library.

## Adding a server

**Add server** asks for the address and user and can generate a dedicated ed25519 key. It then shows a one-liner to authorize that key and a **Test connection** button.

- **Supported:** any Linux with `sh` and `/proc`. Package features cover apt (Debian/Ubuntu) and dnf (Fedora/RHEL/Alma/Rocky). Admin features need systemd.
- **Non-root users** need sudo for updates and admin actions; monitoring works without it, with less detail. Either allow passwordless sudo:
  ```
  # /etc/sudoers.d/fleetdeck
  deploy ALL=(ALL) NOPASSWD:ALL
  ```
  or enter the sudo password in the server's Settings. It's stored encrypted, sent only over the SSH session's stdin (never on a command line), tried once per change so it can't trigger a lockout, and passed to Ansible as `ansible_become_password`.
- **Disk health** needs `smartmontools` and root or sudo. The server page offers to install it on physical machines. Checks run every 30 minutes and never wake a sleeping disk.

## Monitoring and alerts

Every server is probed once a minute over its pooled SSH connection. New installs start with three rules: unreachable for 2 minutes, disk above 90 %, and memory above 90 % for 10 minutes.

| | |
|---|---|
| **Disks and inodes** | Rules watch the fullest mount by default, or one mount such as `/data`. If that mount disappears, the rule stays quiet and shows **Mount not found**. |
| **Network traffic** | Per-interface rates, plus 30-day totals next to the Network chart and on the Overview. Time when the probe was down counts as zero. |
| **GPUs** | NVIDIA via `nvidia-smi` (needs the driver), AMD from `/sys/class/drm` (no extra tools). Intel GPUs aren't supported. |
| **Custom metrics** | Graph anything a shell command prints: `ls /var/backups \| wc -l`, hours since the last backup, queue length. Add one from a server's Metrics panel for that server, a tag or all servers. The command must print one number and is stopped after 5 seconds. |
| **Proxmox VE** | On a host with `pvesh`, the Containers tab lists that node's VMs and LXC containers with state, usage and history (read-only). Needs root or passwordless sudo. |
| **Endpoint checks** | An HTTP(S) URL (expected status, optional "page must contain") or a TCP port, checked every minute from the Fleetdeck host. HTTPS checks record the days the certificate has left. Never connects to loopback or link-local addresses such as cloud metadata. |
| **S.M.A.R.T.** | Every check is recorded; **History** charts 30 days of temperature, reallocated and pending sectors (SATA) or wear (NVMe). Rules can watch each of them. |
| **Processes** | "Process not running" fires when no process with a given name (`nginx`, `postgres`) runs, matched against `ps -e`. |
| **OS end of life** | An amber chip in the last six months of security support, red after it ends. Covers Debian (LTS), Ubuntu, RHEL, Alma, Rocky and Fedora. |
| **Update history** | The last 30 apt or dnf transactions, including what unattended-upgrades did. |
| **Prometheus** | Generate a scrape token under **Settings → Organization → General**. `/api/metrics` then serves every server of that organization as `fleetdeck_*` gauges, labelled `server` and `server_id`. Only the token's hash is stored. |
| **CSV export** | The **CSV** button next to a server's chart range downloads every per-minute reading (UTC timestamps, percentages as 0–100). |

> [!WARNING]
> Custom metric commands run with the probe's rights: **as root via `sudo -n`** where available. Anyone who can sign in to Fleetdeck can already run scripts as root, so this adds no new access, but treat the command like a script. Adding or changing one is recorded in the audit log.

> [!NOTE]
> End-of-life dates are built into Fleetdeck (nothing is looked up online), so a release newer than your Fleetdeck version may be missing. Unknown releases show nothing.

## Scripts and automation

![The script library with built-in maintenance scripts and their parameters](assets/screenshots/scripts.png)

- **Parameters** are text, a checkbox or a choice from a list. Shell scripts get them as environment variables (a checkbox is `true`/`false`), playbooks as extra variables (a real boolean). Values are validated on the server.
- **Variables** hold values several scripts need, such as an API token. They're stored encrypted, and a script only receives the ones you tick in it. Secret values can't be viewed again and are masked as `••••••` in job output.
- **Chains** run another script after this one, on success, on failure or always, on the same servers or only those where it succeeded. A chain stops after 10 jobs; cancelled jobs and dry runs don't continue.
- **Retries** (**If it fails** in the script editor) re-run only the failed servers, up to 5 times, 1 to 60 minutes apart. Chains and "job failed" notifications wait for the last attempt. Dry runs and ad-hoc commands never retry.
- **Schedules** run scripts on cron expressions.
- **Webhooks** give one script and a fixed set of servers a secret URL; a `POST` starts the job. Only the URL's hash is stored, and a call while the previous run is still going gets HTTP 409.

### Ansible playbooks from Git

1. **Repositories → Add repository.** Public HTTPS, private SSH with a deploy key from the key store, or HTTPS with an access token.
2. **Scripts → New script → Ansible playbook.** Pick the repository and a playbook. Parameters become extra variables.
3. **Run it** on servers, tags or everything, on demand or on a schedule. Tick **Dry run** for `--check --diff`.

Each run pulls the latest commit (recorded on the job), installs `collections/requirements.yml` and `roles/requirements.yml` if present, builds the inventory from your servers (tags become groups) and enforces Fleetdeck's pinned host keys. The job shows the full log plus per-server tasks and recap.

> [!NOTE]
> Requires `git` and `ansible-playbook` on the Fleetdeck host (the Docker image includes both; elsewhere use `pipx install ansible-core`). Target servers need Python 3.

> [!WARNING]
> Playbooks run on the Fleetdeck host and can execute commands there. Only add repositories you control. Fleetdeck limits the exposure: playbooks get a minimal environment without `FLEETDECK_SECRET`, decrypted keys live in a private temp folder that is wiped after each run, and Git hooks, `file://`/`ext::` transports and submodules are disabled.

## Organizations and roles

People sign in once and switch between their organizations. Roles apply per organization:

| Role | Can do |
|---|---|
| **admin** | Everything in the organization |
| **owner** | Add servers, edit and share the ones they have |
| **operator** | Work on assigned servers: jobs, services, terminal |
| **viewer** | Read-only on assigned servers |

Super admins see every organization on the instance. Invite links are single-use and expire after 7 days.

Anyone can also create one **personal organization** (**Settings → Profile**) for their own servers. Only they see it, super admins included: it takes no invites and no single sign-on of its own, and it's deleted with their account. Super admins see in **Instance → Organizations** that it exists and can delete it.

## Security model

Anyone with access to Fleetdeck has root on every server in it, so the defaults are strict:

- **Sign-in:** scrypt-hashed passwords, optional TOTP, 5-attempt lockout per IP. First-run setup needs the console token.
- **Passkeys:** register them under **Settings → Sign-in & security**. A passkey must verify the user (PIN or biometrics), so it replaces both password and code. A passkey whose signature counter goes backwards (a cloned authenticator) is refused and logged. Passkeys need HTTPS or `localhost` and are tied to the host name; behind a reverse proxy, set `FLEETDECK_TRUST_PROXY=1`.
- **Single sign-on:** OpenID Connect with PKCE, state bound to the browser, nonce and signed ID token checked. Accounts can turn off their password once single sign-on is connected.
- **Tenancy:** every query is scoped to the session's organization; people below admin only see servers assigned to them. Every organization needs at least one admin, the instance at least one super admin.
- **Sessions:** random tokens stored hashed; `HttpOnly`, `SameSite=Strict`, `Secure` behind HTTPS; 7-day idle expiry; revocable in Settings.
- **CSRF and WebSocket hijacking:** cross-origin requests and terminal connections are rejected; API writes accept JSON only.
- **Secrets at rest:** SSH keys, passphrases and tokens are encrypted with AES-256-GCM and decrypted only in memory. Fleetdeck refuses to start if the configured master key doesn't match the one the data was encrypted with.
- **Host keys:** pinned on first connection. A changed key blocks the connection and notifies you until you explicitly accept it.
- **Command injection:** every name passed to a remote command is allowlisted and shell-quoted; scripts are piped over stdin.
- **Browser:** strict CSP (`script-src 'self'`), `frame-ancestors 'none'`, `nosniff`, `no-referrer`.
- **Audit log:** every change, remote action, terminal session, sign-in and alert, with source IP.

## How it works

`main.go` reads the config and starts everything; the rest lives in `internal/`:

| Package | Responsibility |
|---|---|
| `web` | HTTP, authentication, routing and the terminal WebSocket |
| `ssh` | One pooled connection per server, host-key pinning |
| `probe` | Monitoring |
| `checks` | Endpoint and TLS checks |
| `jobs` | Jobs, cron and built-in scripts |
| `alerts` | Alert rules and notifications |
| `admin` | Services, logs, packages, processes, network and Docker |
| `repos` | Git sync and Ansible runs |
| `sec` | Encryption, password hashing and TOTP |
| `db` | PostgreSQL storage and the one-time SQLite import |

The UI in `ui/` is a Vue 3 app built with Vite (Vue Router, CodeMirror for scripts, xterm.js for the terminal, hand-written SVG charts, system fonts). `npm run build` writes `ui/dist`, which the server serves as static files next to the JSON API.

## Out of scope

- **Terraform:** only shell scripts and Ansible playbooks run.
- **Storage/LVM, network configuration, full firewall editing, VMs:** use Cockpit, which Fleetdeck links to (with one-click install).
- **Cancelling a job** stops its output, but a remote command without a terminal may keep running.
