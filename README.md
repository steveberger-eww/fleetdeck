<div align="center">

<img src="assets/icon.svg" width="96" height="96" alt="Fleetdeck logo">

# Fleetdeck

**A self-hosted control panel for your Linux servers.**<br>
Monitoring, one-click maintenance and day-to-day administration in one place, all over SSH, with nothing to install on the servers.

![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white)
![Postgres](https://img.shields.io/badge/PostgreSQL-18-4169E1?logo=postgresql&logoColor=white)
![Docker](https://img.shields.io/badge/Docker-ready-2496ED?logo=docker&logoColor=white)

[Features](#features) · [Getting started](#getting-started) · [Adding a server](#adding-a-server) · [Monitoring](#monitoring-and-alerts) · [Ansible](#ansible-playbooks-from-git) · [Security](#security-model) · [How it works](#how-it-works)

</div>

## Features

Fleetdeck combines the core ideas of three projects in a single binary:

| Inspired by | In Fleetdeck |
|---|---|
| **Beszel** | Per-minute metrics (CPU, memory, swap, disk and inodes per mount, disk I/O, network, load, temperature, NVIDIA and AMD GPUs) with 30 days of history, plus custom metrics from any shell command. Prometheus `/metrics` and CSV export. Per-interface traffic and 30-day traffic totals, Docker containers with health and history, Proxmox VMs and LXC containers (read-only), S.M.A.R.T. disk health with 30-day history. Alert rules for all servers, a tag or one server (disk and inode rules can watch one mount, process rules a program name), sent via ntfy, Pushover, Discord, Slack, Telegram, Gotify or a webhook. Quiet hours and maintenance windows. |
| **Semaphore** | Script library with parameters (text, checkbox, choice) and encrypted shared variables. Run on servers, tags or everything. Jobs stream live, keep their output and can be cancelled or re-run (all hosts or failed only). Script chains (run another script on success or failure) and automatic retries of failed servers. Cron schedules, webhook URLs, an encrypted SSH key store, and **Git repositories with Ansible playbooks** (dry runs, per-host results). |
| **Organizations** | Several organizations (tenants) on one instance, e.g. an IT company and each of its customers. People sign in once and switch between their organizations. Roles per organization: **admin** (everything), **owner** (adds servers, edits and shares the ones they have), **operator** (works on assigned servers: jobs, services, terminal), **viewer** (read-only on assigned servers). Invites by link, an instance-wide single sign-on plus each organization's own, and super admins who see every organization. |
| **Cockpit** | Per-server pages: package updates with install history, an OS end-of-life warning, services running replaced code, automatic updates, systemd services and timers, journal (searchable across all servers or a tag at once), processes, containers, a network tab (open ports checked against the real firewall, incl. Docker ports that bypass ufw, one-click [ufw-docker](https://github.com/chaifeng/ufw-docker)), a web terminal and system info. Every authorized SSH key across the fleet, with one-click removal. |

## Getting started

### Build from source

You need Go 1.26 or newer and a PostgreSQL database. The result is a single binary with the UI embedded.

```sh
docker compose up -d   # local Postgres from compose.yaml
export DATABASE_URL='postgres://fleetdeck:fleetdeck@localhost:5432/fleetdeck?sslmode=disable'
go build -o fleetdeck .
./fleetdeck            # http://127.0.0.1:8080
```

Run the tests with `go test ./...`. Each package works in its own throwaway schema of the compose database (or of `FLEETDECK_TEST_DATABASE_URL`).

### Moving from SQLite

Versions before Postgres kept everything in `DATA_DIR/fleetdeck.db`. When Fleetdeck starts with an empty Postgres database and finds that file, it imports it first (one transaction, row counts checked per table), so just stop the old version and start the new one with `DATABASE_URL` set. Keep `DATA_DIR`: its `secret.key` (or your `FLEETDECK_SECRET`) decrypts the imported keys and tokens.

Everything lands in one organization. The former single owner becomes a super admin who signs in as **`admin`** with the old password (and 2FA); change the sign-in email under **Settings → Profile**. Built-in scripts become the shared library.

### Docker

```sh
docker build -t fleetdeck .
docker run -d --name fleetdeck -p 127.0.0.1:8080:8080 -v fleetdeck:/data \
  -e DATABASE_URL='postgres://…' -e FLEETDECK_SECRET="$(openssl rand -base64 32)" fleetdeck
docker logs fleetdeck   # setup token
```

On first start, the console prints a **setup token**. Open the page, enter the token, and create the first account (a super admin) and its organization.

> [!IMPORTANT]
> Fleetdeck listens on localhost by default. To reach it from elsewhere, put it behind a reverse proxy with HTTPS (Caddy, Traefik, nginx) and set `FLEETDECK_TRUST_PROXY=1`, or keep it on a VPN such as Tailscale or WireGuard.

### Configuration

| Variable | Default | Description |
|---|---|---|
| `HOST` / `PORT` | `127.0.0.1` / `8080` | Listen address |
| `DATABASE_URL` | – | PostgreSQL connection URL (required) |
| `DATA_DIR` | `./data` | `secret.key`, Git checkouts and temporary files |
| `FLEETDECK_SECRET` | – | Master key for encrypting SSH keys and tokens. Without it, a random key is written to `DATA_DIR/secret.key`. |
| `FLEETDECK_TRUST_PROXY` | – | `1` trusts `X-Forwarded-*` headers (client IP, HTTPS detection for secure cookies) |
| `FLEETDECK_PUBLIC_URL` | – | Fleetdeck's address, e.g. `https://fleetdeck.example.com`, for single sign-on redirects and invite links. Can also be set under **Settings → Instance → General**. Without it, the address the browser used counts (fine for `localhost`). |
| `FLEETDECK_PASSWORD_LOGIN` | – | `1` allows password sign-in for accounts that turned it off in favour of single sign-on (for when your provider is down) |

Single sign-on (authentik, Keycloak, any OpenID Connect provider) works at two levels, both set up step by step in the app. All providers use the same redirect URI.

- **Instance** (**Settings → Instance → Single sign-on**, super admins): e.g. your company's authentik. It signs in existing accounts (matched by connected identity or email) and can accept invites.
- **Organization** (**Settings → Organization → Single sign-on**, its admins): the organization's own provider, used from `https://…/?org=<short name>`. It signs in that organization's members only and can add new people with a default role. These sessions stay in that organization and can't change the account's password, 2FA or email, so an organization's provider can't take over accounts elsewhere. This is the feature meant for a business license later (`licensed()` in `internal/web/tenancy.go`).

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

Every server is probed once a minute over its pooled SSH connection. New installs start with three rules: unreachable for 2 minutes, disk above 90 % and memory above 90 % for 10 minutes.

- **Disk and inode rules** watch the fullest mount by default, or one mount such as `/data`. If that mount disappears, the rule stays quiet and shows **Mount not found** in the rule list.
- **Traffic totals** next to the Network chart (and on the Overview) add up the last 30 days, or the retention period if it's shorter. Time when the probe was down counts as zero.
- **OS end of life:** the server header shows an amber chip in the last six months of security support, and a red one after it ends. The Overview shows a warning icon. Covers Debian (LTS), Ubuntu (standard support), RHEL, Alma, Rocky and Fedora.
- **GPUs:** NVIDIA cards are read with `nvidia-smi` (needs the NVIDIA driver), AMD cards from `/sys/class/drm` (no extra tools). The Metrics panel gets a GPU chart (busiest GPU's usage and memory) on servers that have one, and System lists every card. Intel GPUs aren't supported.
- **Custom metrics** graph anything a shell command prints: `ls /var/backups | wc -l`, hours since the last backup, queue length. Add one with **Add custom metric** in a server's Metrics panel, for that server, a tag or all servers. The command must print one number; anything else is recorded as missing and the chart says why. Each command is stopped after 5 seconds, and they all run in parallel with the rest of the probe. Pick **Custom metric** in an alert rule to get notified when the value goes above a threshold.

  > [!WARNING]
  > Custom metric commands run with the probe's rights: **as root via `sudo -n`** where available. Anyone who can sign in to Fleetdeck can already run scripts as root, so this adds no new access, but treat the command like a script. Adding or changing one is recorded in the audit log.

- **Proxmox VE:** on a host with `pvesh`, the Containers tab lists that node's VMs and LXC containers (on a cluster, each node shows only its own guests): state, CPU, memory, disk, uptime and history, with a link to the Proxmox web UI. It's read-only; start, stop and consoles stay in Proxmox. `pvesh` needs root, so the SSH user needs root or passwordless sudo.
- **Endpoint checks** live in a server's Network tab: an HTTP(S) URL (expected status, optional "page must contain") or a TCP port, checked every minute from the Fleetdeck host with a 10-second timeout. HTTPS checks record how many days the certificate has left. Alert on them with the **Endpoint check failing** rule (fails for N minutes) and the **TLS certificate days left** rule (fires below N days). Checks never connect to the Fleetdeck host itself (loopback) or to link-local addresses such as cloud metadata; private networks are allowed.
- **Update history** lives in the server's Updates panel: the last 30 apt or dnf transactions, with what was installed, upgraded or removed and whether unattended-upgrades did it.
- **S.M.A.R.T. history and thresholds:** every disk-health check (every 30 minutes) is recorded, and **History** in a server's Disk health panel charts the last 30 days: temperature, and reallocated and pending sectors (SATA) or wear (NVMe). Beyond the existing "disks with S.M.A.R.T. problems" rule, alert rules can watch the worst disk's **reallocated sectors**, the most worn SSD's **wear** and the hottest disk's **temperature**.
- **Process not running:** an alert rule that fires when no process with a given name runs, e.g. `nginx` or `postgres`. Names are matched against `ps -e` (the program name, which the kernel cuts to 15 characters; longer names match on their first 15). If a server's `ps` lists nothing, the rule keeps its last state rather than firing.
- **Prometheus:** under **Settings → Organization → General**, generate a scrape token; the panel shows a ready-to-paste `scrape_configs` entry. `/api/metrics` then serves the latest probe of every server in that organization (paused ones left out) as gauges named `fleetdeck_*`, labelled `server` and `server_id`: up, CPU, memory, swap, load, filesystems per mount, network and disk I/O, temperature, GPU, pending updates, reboot required, failed units, unhealthy containers, custom metrics, endpoint checks and firing alerts. An unreachable server reports only `fleetdeck_up 0`. Only the token's hash is stored; a new token replaces the old one.
- **CSV export:** the **CSV** button next to a server's chart range downloads every per-minute reading for that range (UTC timestamps, percentages as 0-100).

> [!NOTE]
> End-of-life dates are built into Fleetdeck (nothing is looked up online), so a release older than your Fleetdeck version may be missing. Unknown releases show nothing.

## Ansible playbooks from Git

1. **Repositories → Add repository.** Public HTTPS, private SSH with a deploy key from the key store, or HTTPS with an access token.
2. **Scripts → New script → Ansible playbook.** Pick the repository and a playbook. Parameters become extra variables.
3. **Run it** on servers, tags or everything, on demand or on a schedule. Tick **Dry run** for `--check --diff`.

Each run pulls the latest commit (recorded on the job), installs `collections/requirements.yml` and `roles/requirements.yml` if present, builds the inventory from your servers (tags become groups) and enforces Fleetdeck's pinned host keys. The job shows the full log plus per-server tasks and recap.

> [!NOTE]
> Requires `git` and `ansible-playbook` on the Fleetdeck host (the Docker image includes both; elsewhere use `pipx install ansible-core`). Target servers need Python 3.

> [!WARNING]
> Playbooks run on the Fleetdeck host and can execute commands there. Only add repositories you control. Fleetdeck limits the exposure: playbooks get a minimal environment without `FLEETDECK_SECRET`, decrypted keys live in a private temp folder that is wiped after each run, and Git hooks, `file://`/`ext::` transports and submodules are disabled.

## Parameters, variables and webhooks

- **Parameters** are text, a checkbox or a choice from a list, and can be reordered in the script editor. Shell scripts get them as environment variables (a checkbox is `true`/`false`), playbooks as extra variables (a checkbox is a real boolean). Values are checked on the server, so an API call with a choice that isn't in the list gets HTTP 400. Schedules and webhooks use the script's default for any value left empty.
- **Variables** hold values several scripts need, such as an API token. They're stored encrypted, and a script only receives the ones you tick in it (environment variables for shell scripts, extra vars for playbooks). Secret values can't be viewed again and show up as `••••••` in job output.
- **Chains:** a script can name the script to run after it, on success, on failure or always, on the same servers or only those where it succeeded. The next job gets its own parameter defaults and links back to the job that started it. Cancelled jobs and dry runs don't continue, and a chain stops after 10 jobs.
- **Retries:** under **If it fails** in the script editor, a script can retry up to 5 times, 1 to 60 minutes apart. Only the servers that failed run again, with the same parameters, as a new job that links to the attempt it retries. Chains and "job failed" notifications wait for the last attempt (a chain after retries counts every server's latest result). A waiting retry shows its start time on the job page and can be cancelled there. Dry runs and ad-hoc commands never retry, and a retry that's waiting when Fleetdeck restarts is dropped.

- **Webhooks** (on the Schedules page) give one script and a fixed set of servers a secret URL. A `POST` starts the job. Only the URL's hash is stored. If the previous run is still going, the call gets HTTP 409.

## Security model

Anyone with access to Fleetdeck has root on every server in it, so the defaults are strict:

- **Sign-in:** scrypt-hashed password, optional TOTP, 5-attempt lockout per IP. First-run setup needs the console token.
- **Passkeys (optional):** register them under Settings → Sign-in & security, then use **Sign in with a passkey**. A passkey must verify the user (PIN or biometrics), so it replaces both password and code. Failed attempts count toward the same lockout, and a passkey whose signature counter goes backwards (a sign of a cloned authenticator) is refused and logged. Passkeys need HTTPS or `localhost`, and are tied to the host name in the address bar (not an IP address): behind a reverse proxy, set `FLEETDECK_TRUST_PROXY=1` so Fleetdeck sees the real host. Accounts limited to single sign-on can't use them.
- **Single sign-on (optional):** OpenID Connect with PKCE, state bound to the browser, nonce and signed ID token checked. An organization's own provider only reaches that organization (see above). Each account can turn off its password once single sign-on is connected.
- **Organizations:** every query is scoped to the session's organization; people below admin only see servers assigned to them, and viewers can't change anything. Invite links are single-use and expire after 7 days. Every organization needs at least one admin, the instance at least one super admin.
- **Sessions:** random tokens stored hashed; `HttpOnly`, `SameSite=Strict`, `Secure` behind HTTPS cookies; 7-day idle expiry; revocable in Settings.
- **CSRF / WebSocket hijacking:** cross-origin requests and terminal connections are rejected; API writes accept JSON only.
- **Secrets at rest:** SSH keys, passphrases and tokens are encrypted with AES-256-GCM and decrypted only in memory.
- **Host keys:** pinned on first connection. A changed key blocks the connection and notifies you until you explicitly accept it.
- **Command injection:** every name passed to a remote command is allowlisted and shell-quoted; scripts are piped over stdin.
- **Browser:** strict CSP (`script-src 'self'`), `frame-ancestors 'none'`, `nosniff`, `no-referrer`.
- **Audit log:** every change, remote action, terminal session, sign-in and alert, with source IP.
- **Master-key check:** Fleetdeck refuses to start if the configured key doesn't match the one the data was encrypted with.

## How it works

`main.go` reads the config and starts everything; the rest lives in `internal/`:

| Package | Responsibility |
|---|---|
| `web` | HTTP, authentication, routing and the terminal WebSocket |
| `ssh` | One pooled connection per server, host-key pinning |
| `probe` | Monitoring |
| `jobs` | Jobs, cron and built-in scripts |
| `alerts` | Alert rules and notifications |
| `admin` | Services, logs, packages, processes, network and Docker |
| `repos` | Git sync and Ansible runs |
| `sec` | Encryption, password hashing and TOTP |
| `db` | PostgreSQL storage and the one-time SQLite import |

The UI in `public/` uses Preact and htm without a build step. Libraries and fonts are vendored and embedded into the binary.

## Out of scope

- **Terraform:** only shell scripts and Ansible playbooks run.
- **Storage/LVM, network configuration, full firewall editing, VMs:** use Cockpit, which Fleetdeck links to (with one-click install).
- **Cancelling a job** stops its output, but a remote command without a terminal may keep running.
