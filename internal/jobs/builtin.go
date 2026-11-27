package jobs

func pkg(apt, dnf string) string {
	return `export DEBIAN_FRONTEND=noninteractive
if command -v apt-get >/dev/null 2>&1; then ` + apt + `
elif command -v dnf >/dev/null 2>&1; then ` + dnf + `
else echo "No apt-get or dnf on this host"; exit 1; fi`
}

// BuiltinScript is a script seeded into the library on first start.
type BuiltinScript struct {
	ID, Name, Description, Body string
	Params                      Params
}

var Builtin = []BuiltinScript{
	{ID: "update", Name: "Update packages", Description: "Refresh package lists and install all available upgrades.",
		Body: pkg("apt-get update && apt-get -y -o Dpkg::Options::=--force-confold upgrade", "dnf -y upgrade")},
	{ID: "upgrade-packages", Name: "Upgrade selected packages", Description: "Upgrade only the listed packages.",
		Params: Params{{Name: "PACKAGES", Label: "Packages (space separated)", Default: "", Required: true}},
		Body:   pkg("apt-get update && apt-get install -y --only-upgrade -o Dpkg::Options::=--force-confold $PACKAGES", "dnf -y upgrade $PACKAGES")},
	{ID: "autoremove", Name: "Clean up packages", Description: "Remove unused dependencies and clear the package cache.",
		Body: pkg("apt-get -y autoremove && apt-get clean", "dnf -y autoremove && dnf clean all")},
	{ID: "unattended", Name: "Enable automatic security updates", Description: "unattended-upgrades on Debian/Ubuntu, dnf-automatic on Fedora/RHEL.",
		Body: pkg(`apt-get update && apt-get install -y unattended-upgrades && printf 'APT::Periodic::Update-Package-Lists "1";\nAPT::Periodic::Unattended-Upgrade "1";\n' > /etc/apt/apt.conf.d/20auto-upgrades && echo enabled`,
			`dnf -y install dnf-automatic && sed -i 's/^apply_updates.*/apply_updates = yes/; s/^upgrade_type.*/upgrade_type = security/' /etc/dnf/automatic.conf && systemctl enable --now dnf-automatic.timer && echo enabled`)},
	{ID: "unattended-off", Name: "Disable automatic updates", Description: "Turns off unattended-upgrades (Debian/Ubuntu) or dnf-automatic (Fedora/RHEL). Packages stay installed.",
		Body: pkg(`grep -l 'Unattended-Upgrade' /etc/apt/apt.conf.d/* 2>/dev/null | xargs -r sed -i 's/\(Unattended-Upgrade\) "[^"]*"/\1 "0"/' && echo disabled`,
			`for t in dnf-automatic.timer dnf-automatic-install.timer dnf5-automatic.timer; do systemctl disable --now "$t" 2>/dev/null; done; echo disabled`)},
	{ID: "smartmontools", Name: "Install smartmontools", Description: "Disk health (S.M.A.R.T.) checks for physical disks.",
		Body: pkg("apt-get update && apt-get install -y --no-install-recommends smartmontools", "dnf -y install smartmontools") + "\nsmartctl --scan"},
	// Rules from https://github.com/chaifeng/ufw-docker (conntrack variant), appended once to after.rules.
	{ID: "ufw-docker", Name: "Make ufw control Docker ports (ufw-docker)", Description: `Docker-published ports stop bypassing ufw. Afterwards only container ports allowed with "ufw route allow" are reachable from the internet; private networks (10/8, 172.16/12, 192.168/16) still are.`,
		Params: Params{{Name: "KEEP_OPEN", Label: "Container ports to keep open, e.g. 80/tcp 443/tcp (the port inside the container, not the host port)", Default: "", Required: false}},
		Body: `set -e
command -v ufw >/dev/null 2>&1 || { echo "ufw is not installed"; exit 1; }
ufw status | grep -q '^Status: active' || { echo "ufw is not active: turn it on first (and allow SSH!)"; exit 1; }
for p in $KEEP_OPEN; do case "$p" in [0-9]*/tcp|[0-9]*/udp) ;; *) echo "invalid port: $p (use 80/tcp)"; exit 1;; esac; done
f=/etc/ufw/after.rules
if grep -q '^# BEGIN UFW AND DOCKER' "$f"; then
  echo "ufw-docker rules are already in $f"
else
  cp -p "$f" "$f.before-ufw-docker"
  cat >> "$f" <<'EOF'
# BEGIN UFW AND DOCKER
*filter
:ufw-user-forward - [0:0]
:ufw-docker-logging-deny - [0:0]
:DOCKER-USER - [0:0]
-A DOCKER-USER -j ufw-user-forward

-A DOCKER-USER -m conntrack --ctstate RELATED,ESTABLISHED -j RETURN
-A DOCKER-USER -m conntrack --ctstate INVALID -j DROP
-A DOCKER-USER -i docker0 -o docker0 -j ACCEPT

-A DOCKER-USER -j RETURN -s 10.0.0.0/8
-A DOCKER-USER -j RETURN -s 172.16.0.0/12
-A DOCKER-USER -j RETURN -s 192.168.0.0/16

-A DOCKER-USER -j ufw-docker-logging-deny -m conntrack --ctstate NEW -d 10.0.0.0/8
-A DOCKER-USER -j ufw-docker-logging-deny -m conntrack --ctstate NEW -d 172.16.0.0/12
-A DOCKER-USER -j ufw-docker-logging-deny -m conntrack --ctstate NEW -d 192.168.0.0/16

-A DOCKER-USER -j RETURN

-A ufw-docker-logging-deny -m limit --limit 3/min --limit-burst 10 -j LOG --log-prefix "[UFW DOCKER BLOCK] "
-A ufw-docker-logging-deny -j DROP

COMMIT
# END UFW AND DOCKER
EOF
  echo "Added the ufw-docker rules to $f (previous version: $f.before-ufw-docker)"
fi
for p in $KEEP_OPEN; do ufw route allow proto "$(echo "$p" | cut -d/ -f2)" from any to any port "$(echo "$p" | cut -d/ -f1)"; done
ufw reload
if iptables -S DOCKER-USER 2>/dev/null | grep -q ufw-user-forward; then
  echo "Active: Docker traffic now goes through ufw."
else
  echo "The rules are written but not active yet. Reboot the server to apply them."
fi`},
	{ID: "cockpit", Name: "Install Cockpit", Description: "Install Cockpit and enable its web UI on port 9090.",
		Body: pkg("apt-get update && apt-get install -y cockpit", "dnf -y install cockpit") + "\nsystemctl enable --now cockpit.socket && echo \"Cockpit: https://$(hostname -f 2>/dev/null || hostname):9090\""},
	{ID: "restart-services", Name: "Restart services", Description: "Restart several services, e.g. the ones still running old code after updates.",
		Params: Params{{Name: "SERVICES", Label: "Services (space separated)", Default: "", Required: true}},
		Body:   "rc=0\nfor s in $SERVICES; do echo \"== $s\"; systemctl restart -- \"$s\" && echo restarted || rc=1; done\nexit $rc"},
	{ID: "restart-service", Name: "Restart a service", Description: "systemctl restart, then show its status.",
		Params: Params{{Name: "SERVICE", Label: "Service", Default: "", Required: true}},
		Body:   `systemctl restart -- "$SERVICE" && systemctl status --no-pager -- "$SERVICE"`},
	{ID: "disk-usage", Name: "Find large directories", Description: "Largest directories on a filesystem.",
		Params: Params{{Name: "PATH_TO_SCAN", Label: "Path", Default: "/", Required: true}},
		Body:   `du -xh --max-depth=2 -- "$PATH_TO_SCAN" 2>/dev/null | sort -rh | head -n 30`},
	{ID: "docker-prune", Name: "Docker cleanup", Description: "Remove stopped containers, dangling images, unused networks and build cache.",
		Body: "docker system prune -f && docker system df"},
	// Compose labels name the project, its directory and files; the Containers tab fills them in.
	{ID: "compose-update", Name: "Pull and recreate a Compose stack", Description: "docker compose pull, then up -d: containers whose image changed are recreated.",
		Params: Params{{Name: "PROJECT", Label: "Compose project", Default: "", Required: true},
			{Name: "DIR", Label: "Project directory", Default: "", Required: true},
			{Name: "FILES", Label: "Compose files, comma separated (empty: the directory's default)", Default: "", Required: false}},
		Body: `set -e
cd -- "$DIR"
if docker compose version >/dev/null 2>&1; then dc="docker compose"
elif command -v docker-compose >/dev/null 2>&1; then dc=docker-compose
else echo "Docker Compose is not installed"; exit 1; fi
set -- -p "$PROJECT"
IFS=,; for f in $FILES; do set -- "$@" -f "$f"; done; unset IFS
$dc "$@" pull
$dc "$@" up -d
$dc "$@" ps`},
	{ID: "authorize-key", Name: "Authorize SSH public key", Description: "Append a public key to a user's authorized_keys (e.g. for key rotation).",
		Params: Params{{Name: "PUBLIC_KEY", Label: "Public key", Default: "", Required: true}, {Name: "TARGET_USER", Label: "User", Default: "root", Required: true}},
		Body: `set -e
home=$(getent passwd "$TARGET_USER" | cut -d: -f6)
[ -n "$home" ] || { echo "no such user: $TARGET_USER"; exit 1; }
mkdir -p "$home/.ssh" && touch "$home/.ssh/authorized_keys"
grep -qxF "$PUBLIC_KEY" "$home/.ssh/authorized_keys" && echo "already present" || { echo "$PUBLIC_KEY" >> "$home/.ssh/authorized_keys"; echo added; }
chown -R "$TARGET_USER": "$home/.ssh" && chmod 700 "$home/.ssh" && chmod 600 "$home/.ssh/authorized_keys"`},
	{ID: "reboot", Name: "Reboot", Description: "Reboot the server (in 2 seconds, so the job can report back).",
		Body: `echo "Rebooting $(hostname)…"; nohup sh -c "sleep 2; systemctl reboot" >/dev/null 2>&1 &`},
	{ID: "shutdown", Name: "Shut down", Description: "Power the server off.",
		Body: `echo "Shutting down $(hostname)…"; nohup sh -c "sleep 2; systemctl poweroff" >/dev/null 2>&1 &`},
}
