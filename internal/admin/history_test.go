package admin

import (
	"os"
	"testing"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAptHistory(t *testing.T) {
	h := ParseAptHistory(fixture(t, "apt-history.log"))
	eq(t, len(h), 2) // the cut-off entry at the top has no Start-Date
	eq(t, h[0], Transaction{When: "2024-05-02 10:01:02", Source: "apt-get install -y htop", Packages: []Change{
		{"install", "htop", "3.2.2-2"}, {"install", "libnl-3-200", "3.7.0-0.2"}, {"remove", "nano", "7.2-1"}}})
	eq(t, h[1], Transaction{When: "2024-05-01 06:25:13", Source: "unattended-upgrades", Packages: []Change{
		{"upgrade", "libssl3", "3.0.13-1~deb12u1"}, {"upgrade", "openssl", "3.0.13-1~deb12u1"}}})
	eq(t, ParseAptHistory(""), []Transaction{})
}

func TestDnfHistory(t *testing.T) {
	h := ParseDnfHistory(fixture(t, "dnf4-history.txt"))
	eq(t, len(h), 2)
	eq(t, h[0], Transaction{When: "Thu 02 May 2024 10:01:02 AM UTC", Source: "upgrade -y", Packages: []Change{
		{"upgrade", "openssl", "1:3.0.7-27.el9"}, {"install", "kernel", "5.14.0-427.13.1.el9_4"}, {"remove", "nano", "5.6.1-5.el9"}}})
	eq(t, h[1].Packages, []Change{{"install", "htop", "3.3.0-1.el9"}})
	h = ParseDnfHistory(fixture(t, "dnf5-history.txt"))
	eq(t, h, []Transaction{{When: "2024-10-01 10:00:00", Source: "dnf5 upgrade", Packages: []Change{{"upgrade", "curl", "0:8.9.1-2.fc41"}}}})
}
