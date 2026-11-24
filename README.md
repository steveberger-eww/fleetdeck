<div align="center">

<img src="assets/icon.svg" width="96" height="96" alt="Fleetdeck logo">

# Fleetdeck

**A self-hosted control panel for your Linux servers.**<br>
Monitoring, one-click maintenance and day-to-day administration in one place, all over SSH, with nothing to install on the servers.

![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white)
![SQLite](https://img.shields.io/badge/SQLite-embedded-003B57?logo=sqlite&logoColor=white)
![Docker](https://img.shields.io/badge/Docker-ready-2496ED?logo=docker&logoColor=white)

[Features](#features) · [Getting started](#getting-started) · [Adding a server](#adding-a-server) · [Ansible](#ansible-playbooks-from-git) · [Security](#security-model) · [How it works](#how-it-works)

</div>

## Features

Fleetdeck combines the core ideas of three projects in a single binary:

| Inspired by | In Fleetdeck |
|---|---|
| **Beszel** | Per-minute metrics (CPU, memory, swap, disk, disk I/O, network, load, temperature) with 30 days of history. Per-interface traffic, Docker containers with health and history, S.M.A.R.T. disk health. Alert rules for all servers, a tag or one server (disk and inode rules can watch one mount), sent via ntfy, Pushover, Discord, Slack, Telegram, Gotify or a webhook. Quiet hours and maintenance windows. |
| **Semaphore** | Script library with parameters and encrypted shared variables. Run on servers, tags or everything. Jobs stream live, keep their output and can be cancelled or re-run (all hosts or failed only). Cron schedules, webhook URLs, an encrypted SSH key store, and **Git repositories with Ansible playbooks** (dry runs, per-host results). |
| **Cockpit** | Per-server pages: package updates, services running replaced code, automatic updates, systemd services and timers, journal, processes, containers, a network tab (open ports checked against the real firewall, incl. Docker ports that bypass ufw, one-click [ufw-docker](https://github.com/chaifeng/ufw-docker)), a web terminal and system info. Every authorized SSH key across the fleet, with one-click removal. |

## Getting started

### Build from source

You need Go 1.26 or newer. The result is a single binary with the UI embedded.

```sh
go build -o fleetdeck .
./fleetdeck        # http://127.0.0.1:8080
```

Run the tests with `go test ./...`.

### Docker

```sh
docker build -t fleetdeck .
docker run -d --name fleetdeck -p 127.0.0.1:8080:8080 -v fleetdeck:/data \
  -e FLEETDECK_SECRET="$(openssl rand -base64 32)" fleetdeck
docker logs fleetdeck   # setup token
```

On first start, the console prints a **setup token**. Open the page, enter the token and choose your password.

> [!IMPORTANT]
> Fleetdeck listens on localhost by default. To reach it from elsewhere, put it behind a reverse proxy with HTTPS (Caddy, Traefik, nginx) and set `FLEETDECK_TRUST_PROXY=1`, or keep it on a VPN such as Tailscale or WireGuard.

### Configuration

| Variable | Default | Description |
|---|---|---|
| `HOST` / `PORT` | `127.0.0.1` / `8080` | Listen address |
| `DATA_DIR` | `./data` | SQLite database and `secret.key` |
| `FLEETDECK_SECRET` | – | Master key for encrypting SSH keys and tokens. Without it, a random key is written to `DATA_DIR/secret.key`. |
| `FLEETDECK_TRUST_PROXY` | – | `1` trusts `X-Forwarded-*` headers (client IP, HTTPS detection for secure cookies) |
| `FLEETDECK_PASSWORD_LOGIN` | – | `1` allows password sign-in even when it's turned off in favour of single sign-on (for when your provider is down) |

Single sign-on (authentik, Keycloak, any OpenID Connect provider) is set up in the app under **Settings → Single sign-on**, step by step.

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

## Ansible playbooks from Git

1. **Repositories → Add repository.** Public HTTPS, private SSH with a deploy key from the key store, or HTTPS with an access token.
2. **Scripts → New script → Ansible playbook.** Pick the repository and a playbook. Parameters become extra variables.
3. **Run it** on servers, tags or everything, on demand or on a schedule. Tick **Dry run** for `--check --diff`.

Each run pulls the latest commit (recorded on the job), installs `collections/requirements.yml` and `roles/requirements.yml` if present, builds the inventory from your servers (tags become groups) and enforces Fleetdeck's pinned host keys. The job shows the full log plus per-server tasks and recap.

> [!NOTE]
> Requires `git` and `ansible-playbook` on the Fleetdeck host (the Docker image includes both; elsewhere use `pipx install ansible-core`). Target servers need Python 3.

> [!WARNING]
> Playbooks run on the Fleetdeck host and can execute commands there. Only add repositories you control. Fleetdeck limits the exposure: playbooks get a minimal environment without `FLEETDECK_SECRET`, decrypted keys live in a private temp folder that is wiped after each run, and Git hooks, `file://`/`ext::` transports and submodules are disabled.

## Variables and webhooks

- **Variables** hold values several scripts need, such as an API token. They're stored encrypted, and a script only receives the ones you tick in it (environment variables for shell scripts, extra vars for playbooks). Secret values can't be viewed again and show up as `••••••` in job output.
- **Webhooks** (on the Schedules page) give one script and a fixed set of servers a secret URL. A `POST` starts the job. Only the URL's hash is stored. If the previous run is still going, the call gets HTTP 409.

## Security model

Anyone with access to Fleetdeck has root on every server in it, so the defaults are strict:

- **Sign-in:** scrypt-hashed password, optional TOTP, 5-attempt lockout per IP. First-run setup needs the console token.
- **Single sign-on (optional):** OpenID Connect with PKCE, state bound to the browser, nonce and signed ID token checked. Only the one connected account (its `sub`) gets in. Password sign-in can then be turned off.
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
| `db` | SQLite storage (pure Go, no cgo) |

The UI in `public/` uses Preact and htm without a build step. Libraries and fonts are vendored and embedded into the binary.

## Out of scope

- **Multiple users or roles:** it's a single-owner panel.
- **Terraform:** only shell scripts and Ansible playbooks run.
- **Storage/LVM, network configuration, full firewall editing, VMs:** use Cockpit, which Fleetdeck links to (with one-click install).
- **Cancelling a job** stops its output, but a remote command without a terminal may keep running.
