package classify

import (
	"context"
	"strings"
	"unicode"
)

// The affect facet's labels.
const (
	AffectNeutral    = "neutral"
	AffectFrustrated = "frustrated"
	AffectUrgent     = "urgent"
	AffectPositive   = "positive"
)

// AffectFacet is a facet spec for Affect's labels, to layer into a pack.
func AffectFacet() FacetSpec {
	return FacetSpec{Name: "affect", About: "The tone of the text.", MinP: 0.5, MinMargin: 0.1, Labels: []LabelSpec{
		{Name: AffectNeutral, About: "No particular feeling.", Examples: []string{"open the notes", "what changed in the last commit?"}},
		{Name: AffectFrustrated, About: "Annoyed that something is wrong or keeps failing.", Examples: []string{"why is this STILL broken", "ugh, wrong file again"}},
		{Name: AffectUrgent, About: "Needs it done now.", Examples: []string{"prod is down, fix it asap", "need this right away"}},
		{Name: AffectPositive, About: "Pleased or grateful.", Examples: []string{"thanks, that's perfect", "great work"}},
	}}
}

// Affect is a lexicon-and-heuristics judge for the affect facet, in the manner
// of VADER: cue words vote for frustrated, urgent or positive; a negation in the
// three words before a positive cue turns it to frustration; exclamation marks
// and words in capitals intensify whatever was found; neutral holds a constant
// vote of 0.5, so text with no cue stays neutral and one cue outweighs it. It runs in microseconds.
type Affect struct {
	name  string
	facet string
	cues  map[string][]cue // first word -> cues starting with it
}

type cue struct {
	words  []string
	label  string
	weight float64
}

var affectCues = map[string][]string{
	AffectFrustrated: {"broken", "still broken", "again", "wrong", "annoying", "annoyed", "frustrating", "frustrated",
		"ugh", "argh", "wtf", "useless", "stupid", "hate", "doesn't work", "does not work", "not working", "won't work",
		"keeps failing", "keeps breaking", "fails", "failing", "failed", "stuck", "ridiculous", "terrible", "awful",
		"sick of", "fed up", "come on", "seriously", "for the third time", "how many times"},
	AffectUrgent: {"asap", "urgent", "urgently", "immediately", "right away", "right now", "hurry", "critical",
		"emergency", "blocker", "blocking", "outage", "is down", "prod", "production", "deadline", "now please"},
	AffectPositive: {"thanks", "thank you", "great", "awesome", "perfect", "nice", "love it", "excellent",
		"good job", "well done", "amazing", "brilliant", "that worked", "works now", "nailed it"},
}

var negations = map[string]bool{"not": true, "no": true, "never": true, "isn't": true, "wasn't": true,
	"don't": true, "doesn't": true, "didn't": true, "hardly": true, "nothing": true}

// NewAffect returns the judge for facet (usually "affect").
func NewAffect(name, facet string) *Affect {
	a := &Affect{name: name, facet: facet, cues: map[string][]cue{}}
	for label, phrases := range affectCues {
		for _, p := range phrases {
			ws := Tokens(p)
			a.cues[ws[0]] = append(a.cues[ws[0]], cue{words: ws, label: label, weight: 1})
		}
	}
	return a
}

// Name implements Judge.
func (a *Affect) Name() string { return a.name }

// Phase implements Judge.
func (a *Affect) Phase() Phase { return Keystroke }

// Judge implements Judge.
func (a *Affect) Judge(_ context.Context, in Input) ([]Vote, error) {
	if strings.TrimSpace(in.Text) == "" {
		return nil, nil
	}
	words := Tokens(in.Text)
	sums := map[string]float64{}
	var found []string
	for i := 0; i < len(words); i++ {
		for _, c := range a.cues[words[i]] {
			if i+len(c.words) > len(words) || !equalWords(words[i:i+len(c.words)], c.words) {
				continue
			}
			label := c.label
			if label == AffectPositive && negatedBefore(words, i) {
				label = AffectFrustrated
			}
			sums[label] += c.weight
			found = append(found, strings.Join(c.words, " "))
		}
	}
	intensity := 1.0
	if n := strings.Count(in.Text, "!"); n > 0 {
		intensity += 0.3 * float64(min(n, 3))
	}
	if strings.Contains(in.Text, "?!") || strings.Contains(in.Text, "!?") {
		sums[AffectFrustrated] += 0.5
	}
	if caps := shouted(in.Text); caps > 0 {
		intensity += 0.4 * float64(min(caps, 3))
		if len(sums) == 0 {
			sums[AffectFrustrated] += 0.5 // shouting with no other cue reads as frustration
		}
	}
	out := []Vote{{Facet: a.facet, Label: AffectNeutral, Weight: 0.5, Evidence: "baseline"}}
	for label, s := range sums {
		out = append(out, Vote{Facet: a.facet, Label: label, Weight: s * intensity,
			Evidence: strings.Join(found, ", ")})
	}
	return out, nil
}

func equalWords(a, b []string) bool {
	for i := range b {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func negatedBefore(words []string, i int) bool {
	for j := max(0, i-3); j < i; j++ {
		if negations[words[j]] {
			return true
		}
	}
	return false
}

// shouted counts words of three or more letters written all in capitals.
func shouted(text string) int {
	n := 0
	for _, w := range strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) }) {
		if len([]rune(w)) < 3 {
			continue
		}
		upper := true
		for _, r := range w {
			if !unicode.IsUpper(r) {
				upper = false
				break
			}
		}
		if upper {
			n++
		}
	}
	return n
}
