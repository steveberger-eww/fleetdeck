// Package probe: Beszel-style monitoring without an agent: one POSIX sh script per minute over the pooled SSH connection.

package probe

// PROBE is the per-minute probe script.
const PROBE = `
export LC_ALL=C
echo "hostname=$(hostname 2>/dev/null || cat /etc/hostname)"
. /etc/os-release 2>/dev/null
echo "os=$PRETTY_NAME"
echo "os_id=$ID"
echo "os_version=$VERSION_ID"
echo "kernel=$(uname -r)"
echo "arch=$(uname -m)"
echo "cpu_model=$(grep -m1 -E '^(model name|Model)' /proc/cpuinfo | cut -d: -f2- | sed 's/^ *//')"
echo "cores=$(grep -c ^processor /proc/cpuinfo)"
echo "virt=$(systemd-detect-virt 2>/dev/null)"
echo "ips=$(hostname -I 2>/dev/null)"
echo "uptime=$(cut -d' ' -f1 /proc/uptime)"
echo "load=$(cut -d' ' -f1-3 /proc/loadavg)"
echo "procs=$(ls -d /proc/[0-9]* | wc -l)"
dk=''
# docker stats samples for ~2s: run it in the background, alongside the CPU sample below.
if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
  dk=$(mktemp)
  T=''; command -v timeout >/dev/null 2>&1 && T='timeout 15' # a stuck daemon must not stall the whole probe
  $T docker stats --no-stream --no-trunc --format '{{json .}}' > "$dk" 2>/dev/null </dev/null &
fi
echo "cpu1=$(head -1 /proc/stat)"
sleep 1
echo "cpu2=$(head -1 /proc/stat)"
awk '/^MemTotal:/{t=$2} /^MemAvailable:/{a=$2} /^SwapTotal:/{st=$2} /^SwapFree:/{sf=$2} END{printf "mem=%.0f %.0f %.0f %.0f\n", t*1024, a*1024, st*1024, sf*1024}' /proc/meminfo
df -PTk -x tmpfs -x devtmpfs -x squashfs -x overlay -x efivarfs 2>/dev/null | awk 'NR>1 && $3>0 {printf "fs=%s %s %.0f %.0f %s\n", $1, $2, $3*1024, $4*1024, $7}'
df -Pi -x tmpfs -x devtmpfs -x squashfs -x overlay -x efivarfs 2>/dev/null | awk 'NR>1 && $2>0 {printf "ino=%s %s %s\n", $3, $2, $6}'
awk 'NR>2{sub(/^ +/,""); split($0,a,":"); split(a[2],f," "); if(a[1]!="lo" && a[1]!~/^veth/) printf "if=%s %.0f %.0f\n", a[1], f[1], f[9]; if(a[1]!~/^(lo|veth|docker|br-|virbr|cni|flannel|cali|tailscale|wg)/){r+=f[1]; t+=f[9]}} END{printf "net=%.0f %.0f\n", r, t}' /proc/net/dev
awk '$3~/^(sd[a-z]+|vd[a-z]+|xvd[a-z]+|hd[a-z]+|nvme[0-9]+n[0-9]+|mmcblk[0-9]+)$/{r+=$6; w+=$10} END{printf "dio=%.0f %.0f\n", r*512, w*512}' /proc/diskstats
t=$(cat /sys/class/thermal/thermal_zone*/temp /sys/class/hwmon/hwmon*/temp*_input 2>/dev/null | sort -n | tail -1)
[ -n "$t" ] && echo "temp=$t"
if command -v apt-get >/dev/null 2>&1; then
  echo "pkg=apt"
  u=$(apt-get -s -o Debug::NoLocking=1 upgrade 2>/dev/null | grep '^Inst ')
  echo "updates=$(printf '%s' "$u" | grep -c .)"
  echo "security=$(printf '%s' "$u" | grep -ci securi)"
  a=off
  dpkg -s unattended-upgrades 2>/dev/null | grep -q '^Status: install ok installed' && apt-config dump 2>/dev/null | grep -qE 'APT::Periodic::Unattended-Upgrade "[1-9]' && a=on
  echo "auto_updates=$a"
elif command -v dnf >/dev/null 2>&1; then
  echo "pkg=dnf"
  echo "updates=$(dnf -q --cacheonly check-update 2>/dev/null | grep -cE '^[A-Za-z0-9_.+-]+\.[A-Za-z0-9_]+ ')"
  echo "security=$(dnf -q --cacheonly updateinfo list --security 2>/dev/null | grep -c .)"
  a=off
  systemctl is-enabled -q dnf-automatic-install.timer 2>/dev/null && a=on
  systemctl is-enabled -q dnf-automatic.timer dnf5-automatic.timer 2>/dev/null && grep -qE '^apply_updates *= *(yes|true|1)' /etc/dnf/automatic.conf 2>/dev/null && a=on
  echo "auto_updates=$a"
fi
r=0
[ -f /var/run/reboot-required ] && r=1
command -v needs-restarting >/dev/null 2>&1 && ! needs-restarting -r >/dev/null 2>&1 && r=1
echo "reboot=$r"
echo "failed=$(systemctl --failed --no-legend --plain 2>/dev/null | grep -c .)"
echo "cockpit=$(systemctl is-active cockpit.socket 2>/dev/null)"
if [ -n "$dk" ]; then
  echo "docker=$(docker ps -q | wc -l) $(docker ps -aq | wc -l)"
  docker ps -a --no-trunc --format '{{json .}}' 2>/dev/null | sed 's/^/ctr=/'
  wait
  sed 's/^/cst=/' "$dk"
  rm -f "$dk"
fi
# GPUs: NVIDIA via nvidia-smi (bounded: a wedged driver can hang it), AMD from sysfs (no tools needed). Intel is out of scope.
if command -v nvidia-smi >/dev/null 2>&1; then
  T=''; command -v timeout >/dev/null 2>&1 && T='timeout 5'
  $T nvidia-smi --query-gpu=utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw,name --format=csv,noheader,nounits 2>/dev/null | sed 's/^/nv=/'
fi
for c in /sys/class/drm/card*; do
  case ${c##*/} in *-*) continue ;; esac # connectors like card0-DP-1
  d=$c/device
  [ -r "$d/gpu_busy_percent" ] || continue
  h=$(ls -d "$d"/hwmon/hwmon* 2>/dev/null | head -1)
  echo "amd=${c##*/},$(cat "$d/gpu_busy_percent" 2>/dev/null),$(cat "$d/mem_info_vram_used" 2>/dev/null),$(cat "$d/mem_info_vram_total" 2>/dev/null),$(cat "$h/temp1_input" 2>/dev/null),$(cat "$h/power1_average" 2>/dev/null)"
done
# Proxmox VE: this node's VMs and LXC containers (pvesh needs root; on a cluster it lists every node's guests).
if command -v pvesh >/dev/null 2>&1; then
  T=''; command -v timeout >/dev/null 2>&1 && T='timeout 10'
  echo "pve_node=$(uname -n | cut -d. -f1)"
  echo "pve=$($T pvesh get /cluster/resources --type vm --output-format json 2>/dev/null | tr -d '\n')"
fi
echo "probe_user=$(id -un)"
true
`

// Disk health, appended to the probe every 30 minutes (smartctl is slow-ish). -n standby: never spin up a sleeping disk.
const SMART = `
if ! command -v smartctl >/dev/null 2>&1; then echo "smart=none"
elif [ "$(id -u)" != 0 ]; then echo "smart=noroot"
else
  echo "smart=ok"
  smartctl --scan 2>/dev/null | while read -r dev _ type _; do
    echo "smartdev=$dev $(smartctl -j -n standby -H -A -i -d "$type" "$dev" 2>/dev/null | tr -d '
')"
  done
fi
`
