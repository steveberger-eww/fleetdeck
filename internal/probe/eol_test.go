package probe

import "testing"

func TestEOL(t *testing.T) {
	for _, c := range [][3]string{
		{"debian", "12", "2028-06-30"},
		{"ubuntu", "24.04", "2029-05-31"},
		{"ubuntu", "24", ""}, // Ubuntu needs the full YY.MM
		{"rocky", "9.4", "2032-05-31"},
		{"debian", "", ""}, // testing/sid have no VERSION_ID
		{"arch", "", ""},
		{"debian", "99", ""},
	} {
		if got := EOL(c[0], c[1]); got != c[2] {
			t.Errorf("EOL(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
	if s := Parse("os_id=ubuntu\nos_version=22.04"); s.OSEOL != "2027-06-01" {
		t.Errorf("Parse osEol = %q", s.OSEOL)
	}
}
