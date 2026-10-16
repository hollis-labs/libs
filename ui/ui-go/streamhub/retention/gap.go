package retention

import "sort"

// GapFrame is the single informational frame a reader gets, once and before
// the data, when it starts below a class watermark of a [TrimPerRow] store
// (D7 rev 2.3). Reason is always "retention". The reader's cursor is NOT
// advanced by it: cursors are never reused and resuming from the cursor of a
// removed row is valid.
type GapFrame struct {
	Reason string
	// Class and ThroughCursor name the watermark that explains the gap: every
	// row of Class at or below ThroughCursor was removed by retention.
	Class         string
	ThroughCursor uint64
}

// ReadGap returns the frame for a read that starts after cursor `after`, or
// false when the read starts at or beyond every watermark. With several
// classes below the reader the frame names the one with the highest
// watermark (ties go to the class name that sorts first), so one frame says
// how far retention has reached.
func ReadGap(watermarks map[string]uint64, after uint64) (GapFrame, bool) {
	classes := make([]string, 0, len(watermarks))
	for c := range watermarks {
		classes = append(classes, c)
	}
	sort.Strings(classes)
	var best GapFrame
	found := false
	for _, c := range classes {
		w := watermarks[c]
		if w > after && (!found || w > best.ThroughCursor) {
			best, found = GapFrame{Reason: "retention", Class: c, ThroughCursor: w}, true
		}
	}
	return best, found
}
