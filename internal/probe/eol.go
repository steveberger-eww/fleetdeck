package probe

import "strings"

// eolDates is when each release stops getting security updates (Debian: LTS end, Ubuntu: standard support,
// RHEL and clones: maintenance end). Built in, so nothing is looked up online. Source: endoflife.date.
// Update it with each Fleetdeck release.
var eolDates = map[string]string{
	"debian/9": "2022-07-01", "debian/10": "2024-06-30", "debian/11": "2026-08-31", "debian/12": "2028-06-30", "debian/13": "2030-06-30",
	"ubuntu/18.04": "2023-05-31", "ubuntu/20.04": "2025-05-29", "ubuntu/22.04": "2027-06-01", "ubuntu/24.04": "2029-05-31",
	"ubuntu/24.10": "2025-07-10", "ubuntu/25.04": "2026-01-17", "ubuntu/25.10": "2026-07-01", "ubuntu/26.04": "2031-05-29", "ubuntu/26.10": "2027-07-15",
	"rhel/7": "2024-06-30", "rhel/8": "2029-05-31", "rhel/9": "2032-05-31", "rhel/10": "2035-05-31",
	"fedora/40": "2025-05-13", "fedora/41": "2025-12-15", "fedora/42": "2026-05-27", "fedora/43": "2026-12-01", "fedora/44": "2027-06-02", "fedora/45": "2027-12-08",
}

// EOL is the end-of-life date (YYYY-MM-DD) for an /etc/os-release ID and VERSION_ID, or "" when unknown.
func EOL(id, version string) string {
	switch id {
	case "almalinux", "rocky":
		id = "rhel"
	}
	if id != "ubuntu" { // Ubuntu releases are YY.MM; the others go by major version (rhel 9.4 -> 9)
		version, _, _ = strings.Cut(version, ".")
	}
	return eolDates[id+"/"+version]
}
