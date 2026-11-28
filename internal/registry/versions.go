package registry

import (
	"cmp"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Steps, smallest change first. How a container compares with what its registry offers.
const (
	Current = "current"
	Build   = "build" // same version, newer image (rebuilt on a patched base image)
	Patch   = "patch"
	Minor   = "minor"
	Major   = "major"
)

var stepRank = map[string]int{"": 0, Current: 0, Build: 1, Patch: 2, Minor: 3, Major: 4}

// Bigger returns the larger of two steps.
func Bigger(a, b string) string {
	if stepRank[b] > stepRank[a] {
		return b
	}
	return a
}

// Compared is the newest tag of the running tag's kind.
type Compared struct {
	Step   string // "" = nothing newer
	Newest string
	Safe   string // newest tag with the running major version, when Newest is a major step away and Safe is newer
	Behind int    // major versions between the running one and Newest
}

type version struct {
	tag    string
	nums   []int
	flavor string // the suffix without its numbers: "-alpine3.19" and "-alpine3.20" are both "-alpine."
	build  []int  // the suffix's numbers, compared after nums (-ls250 < -ls251)
}

var (
	verRe = regexp.MustCompile(`^v?(\d{1,9}(?:\.\d{1,9}){0,3})([-_+.].*)?$`)
	numRe = regexp.MustCompile(`\d+`)
)

func parseVersion(tag string) (version, bool) {
	m := verRe.FindStringSubmatch(tag)
	if m == nil {
		return version{}, false
	}
	v := version{tag: tag, flavor: numRe.ReplaceAllString(m[2], "")}
	for _, n := range strings.Split(m[1], ".") {
		i, _ := strconv.Atoi(n)
		v.nums = append(v.nums, i)
	}
	for _, n := range numRe.FindAllString(m[2], 8) {
		i, _ := strconv.Atoi(n)
		v.build = append(v.build, i)
	}
	return v, true
}

// IsVersion reports whether a tag names a version (16, 1.25.3, v2.0-alpine) rather than a channel (latest, stable).
func IsVersion(tag string) bool { _, ok := parseVersion(tag); return ok }

func (a version) cmp(b version) int {
	return cmp.Or(slices.Compare(a.nums, b.nums), slices.Compare(a.build, b.build))
}

// Compare looks for tags newer than cur of the same kind: as many version parts, the same suffix apart from its
// numbers (so -rc1, -beta and other flavors never stand in for a release). A "v" prefix is ignored.
// ponytail: calendar versions (2024.1 → 2025.1) count as major steps.
func Compare(cur string, tags []string) Compared {
	c, ok := parseVersion(cur)
	if !ok {
		return Compared{}
	}
	digits := len(strconv.Itoa(c.nums[0]))
	var newest, safe *version
	majors := map[int]bool{}
	for _, t := range tags {
		v, ok := parseVersion(t)
		// Same shape only; a much longer major (20231012 next to 16) is a date or build number, not a version.
		if !ok || len(v.nums) != len(c.nums) || v.flavor != c.flavor || len(strconv.Itoa(v.nums[0])) > digits+1 || v.cmp(c) <= 0 {
			continue
		}
		if newest == nil || v.cmp(*newest) > 0 {
			newest = &v
		}
		if v.nums[0] == c.nums[0] && (safe == nil || v.cmp(*safe) > 0) {
			safe = &v
		}
		if v.nums[0] > c.nums[0] {
			majors[v.nums[0]] = true
		}
	}
	if newest == nil {
		return Compared{}
	}
	r := Compared{Newest: newest.tag, Behind: len(majors)}
	i := 0 // the first version part that differs
	for i < len(c.nums) && c.nums[i] == newest.nums[i] {
		i++
	}
	switch {
	case i == len(c.nums):
		r.Step = Build
	case i == 0:
		r.Step = Major
		if safe != nil {
			r.Safe = safe.tag
		}
	case i == 1:
		r.Step = Minor
	default:
		r.Step = Patch
	}
	return r
}
