package conformance

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type fixtureManifestEntry struct {
	count  int
	digest string
}

func TestEmbeddedFixtureManifest(t *testing.T) {
	want := readFixtureManifest(t)
	wantSets := []FixtureSet{
		CompensationFixtures,
		ControlFlowFixtures,
		ExecutorMetadataFixtures,
		GraphValidationFixtures,
		MemoizationFixtures,
		SchedulerFixtures,
		SourceMapFixtures,
		ValueFixtures,
		VerificationFixtures,
		WaitFixtures,
	}
	if len(want) != len(wantSets) {
		t.Fatalf("fixture manifest has %d sets, want %d", len(want), len(wantSets))
	}
	for _, set := range wantSets {
		entry, ok := want[set]
		if !ok {
			t.Fatalf("fixture manifest is missing stable set %q", set)
		}
		paths, err := fs.Glob(embeddedFixtureFiles, "testdata/fixtures/"+string(set)+"/*.json")
		if err != nil {
			t.Fatalf("glob %s fixtures: %v", set, err)
		}
		sort.Strings(paths)
		hash := sha256.New()
		for _, fixturePath := range paths {
			data, err := embeddedFixtureFiles.ReadFile(fixturePath)
			if err != nil {
				t.Fatalf("read %s: %v", fixturePath, err)
			}
			rel := strings.TrimPrefix(fixturePath, "testdata/fixtures/")
			hash.Write([]byte(rel))
			hash.Write([]byte{0})
			hash.Write(data)
			hash.Write([]byte{0})
		}
		gotDigest := fmt.Sprintf("sha256:%x", hash.Sum(nil))
		if len(paths) != entry.count || gotDigest != entry.digest {
			t.Fatalf("fixture set %s changed: count=%d digest=%s, want count=%d digest=%s; review compatibility and update fixtures.manifest intentionally", set, len(paths), gotDigest, entry.count, entry.digest)
		}
	}
}

func readFixtureManifest(t *testing.T) map[FixtureSet]fixtureManifestEntry {
	t.Helper()
	file, err := os.Open("fixtures.manifest")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	entries := make(map[FixtureSet]fixtureManifestEntry)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			t.Fatalf("invalid fixture manifest line %q", line)
		}
		count, err := strconv.Atoi(fields[1])
		if err != nil || count < 1 || !strings.HasPrefix(fields[2], "sha256:") {
			t.Fatalf("invalid fixture manifest line %q", line)
		}
		set := FixtureSet(fields[0])
		if _, duplicate := entries[set]; duplicate {
			t.Fatalf("duplicate fixture manifest set %q", set)
		}
		entries[set] = fixtureManifestEntry{count: count, digest: fields[2]}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return entries
}
