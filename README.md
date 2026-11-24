<img src="assets/icon.svg" width="64" height="64" alt="">

# Fleetdeck

A self-hosted control panel for your Linux servers. It covers monitoring, one-click maintenance and day-to-day administration in one place. Everything goes over SSH, so nothing is installed on the servers.

It combines the core ideas of three projects:

| Inspired by | In Fleetdeck |
|---|---|
| **Beszel** | Metrics every minute (CPU, memory, swap, disk, disk I/O, network, load, temperature) with 30 days of history and 1h–30d charts. Per-interface traffic. Docker containers with health status and CPU/memory history. S.M.A.R.T. disk health. Alert rules for all servers, a tag or one server, with notifications via ntfy, Pushover, Discord, Slack, Telegram, Gotify or a webhook. Quiet hours and one-time maintenance windows. |
| **Semaphore** | Script library with parameters. Encrypted shared variables (tokens, passwords) that scripts opt into, masked in job output. Run scripts on servers, tags or everything. Jobs keep their output, stream live, and can be cancelled or re-run (all hosts or failed only). Cron schedules and webhook URLs. Encrypted SSH key store. **Git repositories and Ansible playbooks**, with dry runs and per-host results. |
| **Cockpit** | Per-server pages: pending packages (upgrade all or selected), services still running replaced code after an update, automatic updates on/off, systemd services and timers (start/stop/restart/enable, run a timer now), journal viewer, processes (stop/kill), containers (start/stop/logs), a web terminal and system info. Every authorized SSH key across the fleet, with one-click removal. Deep links to Cockpit for storage, network and firewall, with one-click install. |

## Run it

Requires Node 22.5 or newer.

```sh
npm install
npm start          # http://127.0.0.1:8080
npm test
```

On first start, the console prints a **setup token**. Open the page, enter the token and choose your password.

### Docker

```sh
docker build -t fleetdeck .
docker run -d --name fleetdeck -p 127.0.0.1:8080:8080 -v fleetdeck:/data \
  -e FLEETDECK_SECRET="$(openssl rand -base64 32)" fleetdeck
docker logs fleetdeck   # setup token
```

To reach it from anywhere other than localhost, put it behind a reverse proxy with HTTPS (Caddy, Traefik, nginx) and set `FLEETDECK_TRUST_PROXY=1`, or keep it on a VPN such as Tailscale or WireGuard.

| Env | Default | |
|---|---|---|
| `HOST` / `PORT` | `127.0.0.1` / `8080` | Listen address |
| `DATA_DIR` | `./data` | SQLite database and `secret.key` |
| `FLEETDECK_SECRET` | – | Master key for encrypting SSH keys and notifier tokens. Without it, a random key is written to `DATA_DIR/secret.key`. |
| `FLEETDECK_TRUST_PROXY` | – | `1` = trust `X-Forwarded-*` headers (client IP, HTTPS detection for secure cookies) |

## Adding a server

**Add server** asks for the address and user, and can generate a dedicated ed25519 key for the server. It then shows the one-liner to authorize that key, plus a **Test connection** button.

- **Non-root users** need sudo for updates and admin actions. Monitoring works without it, with less detail. Two options:
  - **Passwordless sudo** (simplest):
    ```
    # /etc/sudoers.d/fleetdeck
    deploy ALL=(ALL) NOPASSWD:ALL
    ```
  - **Sudo password:** enter it under the server's Settings. Fleetdeck handles it carefully:
    - It's stored encrypted and never shown again.
    - It's sent only over the SSH session's input, never in a command line, so it doesn't show up in `ps` on the server.
    - It's given to `sudo -S` for a single attempt. If the server rejects it, Fleetdeck stops using it until you change it, so repeated checks can't trigger an account lockout.
    - Ansible runs get it as `ansible_become_password`.
- **Disk health** needs `smartmontools` and root (or sudo). The server page offers to install it on physical machines. Fleetdeck checks every 30 minutes and never wakes a sleeping disk.
- Supported: any Linux with `sh` and `/proc`. Package features cover apt (Debian/Ubuntu) and dnf (Fedora/RHEL/Alma/Rocky). Admin features need systemd.

## Ansible playbooks from Git

1. **Repositories → Add repository.** Accepts public HTTPS repos, private repos over SSH with a deploy key from the key store, or HTTPS with an access token (stored encrypted).
2. **Scripts → New script → Ansible playbook.** Pick the repository and a playbook (Fleetdeck lists the `.yml` files it finds). Parameters become extra variables (`{{ name }}`).
3. **Run it** on servers, tags or everything, on demand or on a schedule. Tick **Dry run** for `--check --diff`.

On every run, Fleetdeck:

- Pulls the latest commit of the branch and records it on the job.
- Installs `collections/requirements.yml` and `roles/requirements.yml` if present.
- Builds the inventory from your servers. Hosts are named after the servers, and tags become groups.
- Writes `known_hosts` from Fleetdeck's pinned host keys and turns on strict host-key checking.

The job shows the full `ansible-playbook` log, plus one entry per server with its tasks and its recap.

**Requirements:** `git` and `ansible-playbook` on the Fleetdeck host. The Docker image includes both; elsewhere, use `pipx install ansible-core`. Target servers need Python 3.

**Trust:** Playbooks run on the Fleetdeck host and can run commands there too (local tasks, lookups, plugins). Only add repositories you control. Fleetdeck reduces the exposure:

- Playbooks get a minimal environment, so `FLEETDECK_SECRET` is not passed.
- Decrypted keys live in a private temp folder that is deleted after the run and wiped on startup.
- Git hooks, `file://` and `ext::` transports, and submodules are disabled.

Runs from the same repository go one at a time.

## Variables and webhooks

- **Variables** hold values that several scripts need, such as an API token or a domain. They're stored encrypted. A script only receives the variables you tick in it: as environment variables for shell scripts and as extra vars for playbooks. Secret values can't be viewed again, are never stored with the job, and show up as `••••••` in job output.
- **Webhooks** (on the Schedules page) give one script and a fixed set of servers a secret URL. A `POST` to it starts the job; the request body is ignored. Only the URL's hash is stored, so it's shown once (you can generate a new one). If the previous run is still going, the call gets HTTP 409 and nothing starts. Failed webhook jobs notify like scheduled ones.

## Security model

Anyone with access to Fleetdeck has root on every server in it, so the defaults are strict:

- **Sign-in:** a password hashed with scrypt, optional TOTP two-factor login, and a 5-attempt lockout per IP. First-run setup needs the console token.
- **Sessions:** a random token stored hashed in the database. The cookie is `HttpOnly`, `SameSite=Strict`, and `Secure` behind HTTPS. Sessions expire after 7 idle days. You can see and revoke them in Settings.
- **CSRF / WebSocket hijacking:** cross-origin requests and terminal connections are rejected. API writes accept JSON only.
- **Secrets at rest:** SSH private keys, passphrases and notifier tokens are encrypted with AES-256-GCM. Keys are decrypted only in memory and never written to disk or sent to the browser.
- **Host keys:** pinned on first connection. If one changes, Fleetdeck refuses to connect, sends a notification and shows both fingerprints. Accepting the new key is an explicit, logged action.
- **Command injection:** every name passed to a remote command (unit, container, PID, signal) is checked against an allowlist and shell-quoted. Scripts are piped over stdin, never spliced into a shell string.
- **Browser:** strict CSP (`script-src 'self'`, no inline script or eval), `frame-ancestors 'none'`, `nosniff`, `no-referrer`.
- **Audit log:** every change, remote action, terminal session, sign-in and alert, with the source IP.
- **Webhook URLs** contain a 256-bit token. Each one can only start its own script on its own servers. There are no general API tokens.
- **Master-key check:** Fleetdeck refuses to start if `FLEETDECK_SECRET` or `secret.key` doesn't match the key the data was encrypted with, instead of failing on each secret later.

## How it works

`server.js` handles HTTP, authentication and routing. The rest lives in `lib/`:

- `ssh.js`: one pooled ssh2 connection per server, plus host-key pinning
- `probe.js`: monitoring
- `jobs.js`: jobs, cron and the built-in scripts
- `alerts.js`: alert rules and notifications
- `admin.js`: services, logs, packages, processes and Docker
- `repos.js`: Git sync and Ansible runs
- `sec.js`: encryption, password hashing and TOTP
- `db.js`: storage in `node:sqlite`

The UI in `public/` uses Preact and htm without a build step.

## Not included

- **Multiple users or roles:** it's a single-owner panel.
- **Terraform:** only shell scripts and Ansible playbooks run.
- **Storage/LVM, network and firewall editing, VMs:** use Cockpit, which Fleetdeck links to.
- **Cancelling a job** stops its output, but a remote command without a terminal may keep running.
