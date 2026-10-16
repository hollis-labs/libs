package retention

import "sort"

// ClassGap says that every row of Class at or below ThroughCursor was removed
// by retention.
type ClassGap struct {
	Class         string
	ThroughCursor uint64
}

// GapFrame is the single informational frame a reader gets, once and before
// the data, when it starts below the watermark of at least one class of a
// [TrimPerRow] store (D7 rev 2.3.1). Reason is always "retention". Classes lists
// EVERY class whose watermark is above the reader's cursor, sorted by class
// name. The reader's cursor is NOT advanced by the frame: cursors are never
// reused and resuming from the cursor of a removed row is valid.
type GapFrame struct {
	Reason  string
	Classes []ClassGap
}

// ReadGap returns the frame for a read that starts after cursor `after`, or
// false when the read starts at or beyond every watermark.
func ReadGap(watermarks map[string]uint64, after uint64) (GapFrame, bool) {
	var classes []ClassGap
	for class, w := range watermarks {
		if w > after {
			classes = append(classes, ClassGap{Class: class, ThroughCursor: w})
		}
	}
	if len(classes) == 0 {
		return GapFrame{}, false
	}
	sort.Slice(classes, func(i, j int) bool { return classes[i].Class < classes[j].Class })
	return GapFrame{Reason: "retention", Classes: classes}, true
}
