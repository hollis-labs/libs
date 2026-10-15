// Package timing probes the timing of a recorded stream: when its frames
// arrived, not what they said. The questions come from the audit's spike
// scripts (stream-timing, live-vs-phased, cap-test): is the text really
// streamed or does it arrive in one burst, how long was the connection silent,
// how many distinct moments carried content.
//
// Profile summarizes a []conformance.TimedFrame into a Report; Classify names
// its Shape. Playback replays the frames at their recorded offsets, on
// whatever clock the caller has (a testing/synctest bubble makes it
// instant), so a decoder or a hub can be exercised against a real cadence.
//
// The thresholds are deliberately blunt and documented on Options: this is a
// smoke check that a provider is streaming rather than phasing, and that a
// silent gap is long enough to trip an idle watchdog, not a benchmark.
package timing
