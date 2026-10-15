package manifest

import (
	"fmt"
	"strings"
)

// CompareVersions compares strict SemVer 2.0.0 values; build metadata is ignored.
// Numeric components are compared without integer conversion or overflow.
func CompareVersions(a, b string) (int, error) {
	if !ValidVersion(a) || !ValidVersion(b) {
		return 0, fmt.Errorf("versions must be SemVer without a leading v")
	}
	ac, ap := versionParts(a)
	bc, bp := versionParts(b)
	for i := range ac {
		if c := numericCompare(ac[i], bc[i]); c != 0 {
			return c, nil
		}
	}
	if ap == bp {
		return 0, nil
	}
	if ap == "" {
		return 1, nil
	}
	if bp == "" {
		return -1, nil
	}
	aa, bb := strings.Split(ap, "."), strings.Split(bp, ".")
	for i := 0; i < len(aa) && i < len(bb); i++ {
		an, bn := numeric(aa[i]), numeric(bb[i])
		c := 0
		switch {
		case an && bn:
			c = numericCompare(aa[i], bb[i])
		case an:
			c = -1
		case bn:
			c = 1
		default:
			c = strings.Compare(aa[i], bb[i])
		}
		if c != 0 {
			return c, nil
		}
	}
	if len(aa) < len(bb) {
		return -1, nil
	}
	if len(aa) > len(bb) {
		return 1, nil
	}
	return 0, nil
}
func versionParts(v string) ([]string, string) {
	base := strings.SplitN(v, "+", 2)[0]
	core, pre, _ := strings.Cut(base, "-")
	return strings.Split(core, "."), pre
}
func numeric(v string) bool { return strings.Trim(v, "0123456789") == "" }
func numericCompare(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}

// Validate checks normalized inclusive bounds. A missing side is unbounded.
func (r HostRange) Validate() error {
	if r.Min == "" && r.Max == "" {
		return fmt.Errorf("min or max is required")
	}
	if r.Min != "" && !ValidVersion(r.Min) {
		return fmt.Errorf("min must be SemVer")
	}
	if r.Max != "" && !ValidVersion(r.Max) {
		return fmt.Errorf("max must be SemVer")
	}
	if r.Min != "" && r.Max != "" {
		c, _ := CompareVersions(r.Min, r.Max)
		if c > 0 {
			return fmt.Errorf("min must not exceed max")
		}
	}
	return nil
}

// Contains applies strict version and prerelease checks even for 0.x/dev plugins.
func (r HostRange) Contains(version string, allowPrerelease bool) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if !ValidVersion(version) {
		return fmt.Errorf("actual version is absent or invalid")
	}
	_, pre := versionParts(version)
	if pre != "" && !allowPrerelease {
		return fmt.Errorf("prerelease version requires explicit host policy")
	}
	if r.Min != "" {
		c, _ := CompareVersions(version, r.Min)
		if c < 0 {
			return fmt.Errorf("version %s is below min %s", version, r.Min)
		}
	}
	if r.Max != "" {
		c, _ := CompareVersions(version, r.Max)
		if c > 0 {
			return fmt.Errorf("version %s exceeds max %s", version, r.Max)
		}
	}
	return nil
}

// Compatibility is host-supplied, resolved without executing plugin code.
// Hosts contains public contract versions, never application binary versions.
// Engines contains runtime versions; binary denotes a host's native-runner contract.
// Only a host may opt into prerelease versions. There is no local/dev bypass.
type Compatibility struct {
	Hosts           map[string]string
	Engines         map[string]string
	AllowPrerelease bool
}

// CheckCompatibility must run before spawn; every declared requirement is checked.
func (m Manifest) CheckCompatibility(c Compatibility) error {
	if err := m.Validate(); err != nil {
		return err
	}
	for _, group := range []struct {
		name     string
		ranges   map[string]HostRange
		versions map[string]string
	}{{"hosts", m.Hosts, c.Hosts}, {"server.engines", m.Server.Engines, c.Engines}} {
		for _, name := range sortedKeys(group.ranges) {
			if err := group.ranges[name].Contains(group.versions[name], c.AllowPrerelease); err != nil {
				return fmt.Errorf("%s.%s: %w", group.name, name, err)
			}
		}
	}
	if m.Server.Runtime == "node" {
		order, _ := CompareVersions(c.Engines["node"], "22.0.0")
		if order < 0 {
			return fmt.Errorf("server.engines.node: actual Node version must be >=22.0.0")
		}
	}
	return nil
}
