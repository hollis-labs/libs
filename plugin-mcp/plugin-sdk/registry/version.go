package registry

import (
	"regexp"
	"strconv"
	"strings"
)

var semver = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

type version struct {
	numbers [3]uint64
	pre     []string
}

func parseVersion(s string) (version, bool) {
	m := semver.FindStringSubmatch(s)
	if m == nil {
		return version{}, false
	}
	v := version{}
	for i := range 3 {
		n, err := strconv.ParseUint(m[i+1], 10, 64)
		if err != nil || n > MaxRevision {
			return v, false
		}
		v.numbers[i] = n
	}
	if m[4] != "" {
		v.pre = strings.Split(m[4], ".")
		for _, p := range v.pre {
			if numeric(p) && len(p) > 1 && p[0] == '0' {
				return v, false
			}
		}
	}
	return v, true
}
func numeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
func compare(a, b version) int {
	for i := range 3 {
		if a.numbers[i] < b.numbers[i] {
			return -1
		}
		if a.numbers[i] > b.numbers[i] {
			return 1
		}
	}
	if len(a.pre) == 0 && len(b.pre) == 0 {
		return 0
	}
	if len(a.pre) == 0 {
		return 1
	}
	if len(b.pre) == 0 {
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		x, y := a.pre[i], b.pre[i]
		if x == y {
			continue
		}
		xn, yn := numeric(x), numeric(y)
		if xn && yn {
			if len(x) != len(y) {
				if len(x) < len(y) {
					return -1
				}
				return 1
			}
		} else if xn != yn {
			if xn {
				return -1
			}
			return 1
		}
		if x < y {
			return -1
		}
		return 1
	}
	if len(a.pre) < len(b.pre) {
		return -1
	}
	if len(a.pre) > len(b.pre) {
		return 1
	}
	return 0
}
func validBounds(min, max string) bool {
	if min == "" && max == "" {
		return false
	}
	a, aok := parseVersion(min)
	b, bok := parseVersion(max)
	if min != "" && !aok || max != "" && !bok {
		return false
	}
	return min == "" || max == "" || compare(a, b) <= 0
}
