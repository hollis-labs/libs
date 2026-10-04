package credentials

import (
	"strings"
	"testing"
)

func TestRelativePathLengthBoundary(t *testing.T) {
	components := make([]string, 17)
	for n := range components {
		components[n] = strings.Repeat("a", 240)
	}
	atLimit := strings.Join(components, "/")
	if len(atLimit) != 4096 {
		t.Fatal(len(atLimit))
	}
	if err := ValidateRelPath(atLimit); err != nil {
		t.Fatal("valid boundary refused", err)
	}
	if err := ValidateRelPath("a" + atLimit); err == nil {
		t.Fatal("oversized relative path accepted")
	}
}
func TestRelativePathRejectsDeleteControl(t *testing.T) {
	if ValidateRelPath("resource\x7fname") == nil {
		t.Fatal("delete control accepted")
	}
}
