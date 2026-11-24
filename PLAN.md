# Fleetdeck v2 plan (implemented)

## 1. Feature comparison

| Area | Beszel | Semaphore | Cockpit | Fleetdeck v1 | **v2 target** |
|---|---|---|---|---|---|
| Add hosts in UI | ✅ | ✅ inventories | ✅ host switcher | ✅ | ✅ + **edit**, **tags**, notes, pause |
| Agentless | ❌ agent | ✅ SSH | ✅ SSH + bridge pkg | ✅ | ✅ |
| Key store | per-hub key | ✅ encrypted | – | file per server | ✅ **named keys, encrypted at rest, generate ed25519 in-app** |
| Host key pinning | – | – | ✅ prompt | accept-new | ✅ **pin on first use, block + alert on change, accept in UI** |
| CPU/mem/disk/net | ✅ | – | ✅ live | ✅ | ✅ + **swap, load, disk I/O, temperature, all mounts** |
| Metric history | ✅ 1h…30d | – | live only | 3h in RAM | ✅ **SQLite, 30 days, 1h/6h/24h/7d/30d** |
| Docker containers | ✅ stats | – | podman | – | ✅ **list, stats, start/stop/restart, logs** |
| Alerts + notifications | ✅ | ✅ (job results) | – | – | ✅ **rules (down/cpu/mem/disk/swap/temp/updates/reboot/failed units) → webhook, ntfy, Discord, Slack, Telegram, Gotify** |
| Run task on hosts | – | ✅ | – | ✅ fixed buttons | ✅ **script library with parameters, run on servers or tags** |
| Job history + logs | – | ✅ | – | – | ✅ **persisted, live streaming per host, cancel, re-run** |
| Schedules | – | ✅ cron | – | – | ✅ **cron schedules for scripts** |
| Package updates | – | via playbook | ✅ list + apply | count only | ✅ **list packages, upgrade all/selected** |
| Services | – | – | ✅ | failed count | ✅ **list, filter, start/stop/restart/enable/disable** |
| Logs (journal) | – | – | ✅ | – | ✅ **journal viewer: unit, priority, time range, search** |
| Processes | – | – | ✅ (top) | – | ✅ **top by CPU/mem, kill** |
| Web terminal | – | – | ✅ | – | ✅ **xterm.js over SSH, resizable** |
| Auth | ✅ users, OAuth | ✅ users, TOTP | PAM | basic auth | ✅ **login page, scrypt, TOTP 2FA, sessions, setup token** |
| Audit log | – | ✅ events | journal | console | ✅ **every action with actor + IP** |
| Multi-user / RBAC | ✅ | ✅ | PAM | – | ✅ **added later: organizations (tenants), roles admin/owner/operator/viewer, per-server assignment, invites, per-organization SSO, super admins** |
| Ansible / Terraform / git repos | – | ✅ | – | – | ✅ **added later: Git repos (HTTPS, SSH key, token), Ansible playbooks with dry run, per-host results, schedules**. Terraform still skipped. |
| Storage/LVM/network/firewall config, VMs | – | – | ✅ | – | ❌ stays in Cockpit (deep link + one-click install) |

## 2. Architecture

- **Backend:** Go (originally Node 22; ported to Go as a single static binary). PostgreSQL (pgx) stores everything; tables carry a tenant_id for multi-tenancy. Uses **x/crypto/ssh** for SSH: one pooled connection per server; keys stay in memory, so nothing decrypted touches disk; PTYs can be resized; host-key callback for pinning. Uses **coder/websocket** for the terminal websocket.
- **Frontend:** **Preact + htm**, no build step, vendored in `public/` and embedded in the binary. **xterm.js** for the terminal. Hand-written SVG charts. The TOTP QR code is rendered server-side as SVG (**rsc.io/qr**).
- **Probe:** one script piped over stdin, run with `sudo -n` when available, so it works with any remote login shell.
- **Files:**
  - `main.go`, `internal/web`: HTTP, routing, auth
  - `internal/db`
  - `internal/sec`: crypto, sessions, TOTP
  - `internal/ssh`
  - `internal/probe`
  - `internal/jobs`: jobs, cron, schedules
  - `internal/alerts`
  - `internal/admin`: services, logs, packages, processes, docker
  - `public/`: the UI

## 3. Security

1. **Mandatory login.** First run prints a one-time setup token to the console, so whoever reaches the page first can't claim the instance. Passwords are hashed with scrypt. Optional TOTP 2FA. Login is rate-limited per IP.
2. **Sessions:** random 256-bit token, stored hashed. Cookie is `HttpOnly`, `SameSite=Strict`, and `Secure` behind HTTPS. 7-day idle expiry. Log out of all sessions from Settings.
3. **CSRF:** strict same-site cookies, JSON-only mutations, Origin check. The websocket upgrade also checks Origin and the session.
4. **Secrets at rest:** private keys and notifier tokens use AES-256-GCM. The master key comes from `FLEETDECK_SECRET`, or from `data/secret.key` (0600) when that isn't set. Keys are decrypted only in memory.
5. **Host keys:** pinned on first use (shown as a SHA256 fingerprint). A mismatch blocks the connection and raises an alert; accepting the new key is an explicit, audited action.
6. **Input validation:** host, user, unit, container and package names are allowlisted. Script parameters are shell-quoted. All remote output is rendered as text (no HTML).
7. **Headers:** strict CSP (`script-src 'self'`, no inline script, no eval), `frame-ancestors 'none'`, nosniff, `no-referrer`.
8. **Audit log** for every mutation and every remote command.
9. **Least privilege:** non-root SSH users need `NOPASSWD` sudo. The README shows how to restrict it.

## 4. UI / UX

- **Shell:** sidebar with Overview, Jobs, Scripts, Schedules, Alerts, Keys, Audit, Settings, plus a server list with status dots. The top bar has search. The sidebar collapses to a drawer on mobile. Dark-first with a light theme. Toasts, confirm dialogs (type the server name to reboot or shut down), empty states, loading states.
- **Overview:** fleet stats (up/down, updates pending, alerts firing, jobs running), server table with status, CPU/mem/disk bars, sparklines, tags and updates. Search, tag filter, multi-select, bulk "Run script…".
- **Server page tabs:**
  - Overview: charts with range picker, mounts, system info
  - Updates
  - Services
  - Logs
  - Processes
  - Containers
  - Terminal
  - Jobs
  - Settings: edit, key, host key, pause, delete
- **Jobs:** live per-host output with status chips, cancel, re-run.
- **Scripts:** editor with parameter definitions; a run dialog with target picker (servers + tags) and parameter form.
- **Schedules:** cron with presets and a "next runs" preview.
- **Alerts:** rules, notification channels with "send test", firing list and history.
- **Keys:** generate or paste, copy public key, shows "used by N servers".
- **Settings:** password, 2FA with QR code, sessions, data retention.

## 5. Order of work

1. Backend core: db, sec, ssh, probe, metrics
2. Jobs, schedules, admin endpoints, alerts
3. Auth and security hardening
4. UI rewrite
5. Tests (parsers, cron, crypto, TOTP) and an end-to-end run against a throwaway VM
