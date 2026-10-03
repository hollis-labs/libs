package localdaemon

// Matcher decides whether a process command line is the expected program.
// *regexp.Regexp satisfies it.
type Matcher interface {
	MatchString(string) bool
}
