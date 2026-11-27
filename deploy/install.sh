#!/bin/sh
# Installs (or updates) Fleetdeck with Docker Compose and prints where to open it and the setup token.
#   curl -fsSL https://raw.githubusercontent.com/steveberger-eww/fleetdeck/main/deploy/install.sh | sh
# FLEETDECK_DIR: where compose.yaml goes (default ./fleetdeck). Settings: a .env file there, see compose.yaml.
set -eu

REPO=https://raw.githubusercontent.com/steveberger-eww/fleetdeck/main
DIR=${FLEETDECK_DIR:-fleetdeck}

if [ -t 1 ]; then B=$(printf '\033[1m'); D=$(printf '\033[2m'); G=$(printf '\033[32m'); R=$(printf '\033[31m'); C=$(printf '\033[36m'); N=$(printf '\033[0m')
else B='' D='' G='' R='' C='' N=''; fi
step() { printf '%s▸%s %s\n' "$C" "$N" "$*"; }
die() { printf '%s✗ %s%s\n' "$R" "$*" "$N" >&2; exit 1; }

command -v docker >/dev/null 2>&1 || die "Docker isn't installed. Get it from https://docs.docker.com/engine/install/ and run this again."
command -v curl >/dev/null 2>&1 || die "curl isn't installed."

# Without access to the Docker daemon (not in the docker group), go through sudo.
DOCKER=docker
if ! docker info >/dev/null 2>&1; then
  command -v sudo >/dev/null 2>&1 || die "Can't reach the Docker daemon. Is it running, and may this user use it?"
  step "Using sudo for Docker (this user isn't in the docker group)"
  DOCKER="sudo docker"
  $DOCKER info >/dev/null 2>&1 || die "Can't reach the Docker daemon. Is it running?"
fi
$DOCKER compose version >/dev/null 2>&1 || die "Docker Compose v2 is missing (the docker-compose-plugin package)."

mkdir -p "$DIR" && cd "$DIR"
step "Downloading compose.yaml to $(pwd)"
curl -fsSLo compose.yaml.new "$REPO/deploy/compose.yaml" && mv compose.yaml.new compose.yaml

step "Starting Fleetdeck and PostgreSQL (the first start pulls the images)"
$DOCKER compose pull -q
$DOCKER compose up -d --wait

# The address: FLEETDECK_BIND from the environment or .env, as compose sees it.
bind=${FLEETDECK_BIND:-$(sed -n 's/^FLEETDECK_BIND=//p' .env 2>/dev/null | tail -n 1)}
bind=${bind:-127.0.0.1:8080}
case $bind in 0.0.0.0:*|:*) bind="<this server's address>:${bind##*:}" ;; esac
url=${FLEETDECK_PUBLIC_URL:-$(sed -n 's/^FLEETDECK_PUBLIC_URL=//p' .env 2>/dev/null | tail -n 1)}
url=${url:-http://$bind}

# Until the first account exists, every start prints a new token: take the latest. Older log lines can still
# hold one after setup, so ask Fleetdeck first.
token=
if $DOCKER compose exec -T fleetdeck wget -qO- http://127.0.0.1:8080/api/auth/state 2>/dev/null | grep -q '"setup":true'; then
  token=$($DOCKER compose logs --no-log-prefix fleetdeck 2>/dev/null | awk '/setup token:/ { want = 1; next } want && NF { t = $1; want = 0 } END { print t }')
fi

printf '\n%s✓ Fleetdeck is running%s\n\n' "$G$B" "$N"
printf '  Open         %s%s%s\n' "$B" "$url" "$N"
if [ -n "$token" ]; then
  printf '  Setup token  %s%s%s\n\n' "$B$G" "$token" "$N"
  printf '  %sEnter the token to create the first account (a super admin). It changes on every restart until then.%s\n' "$D" "$N"
else
  printf '  %sAlready set up: sign in with your account.%s\n' "$D" "$N"
fi
case $bind in 127.0.0.1:*|localhost:*)
  printf '  %sListening on localhost only. From elsewhere: an SSH tunnel (ssh -L %s:%s <server>), or a reverse proxy with HTTPS.%s\n' "$D" "${bind##*:}" "$bind" "$N" ;;
esac
printf '\n  %sFiles: %s  ·  Update: run this again  ·  Logs: %s compose logs -f%s\n\n' "$D" "$(pwd)" "$DOCKER" "$N"
