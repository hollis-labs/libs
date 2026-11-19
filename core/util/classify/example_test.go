package classify_test

import (
	"context"
	"fmt"

	"github.com/hollis-labs/libs/core/util/classify"
)

func Example() {
	p, _ := classify.ParsePack([]byte(`{
	  "name": "composer", "version": "1",
	  "facets": [{
	    "name": "act", "min_p": 0.5,
	    "labels": [
	      {"name": "command", "examples": ["/open notes"]},
	      {"name": "question", "examples": ["what is open?"]},
	      {"name": "task", "examples": ["fix the failing test"]}
	    ],
	    "rules": [
	      {"id": "slash", "label": "command", "prefix": "/", "decisive": true},
	      {"id": "qmark", "label": "question", "pattern": "\\?$", "weight": 2},
	      {"id": "verb", "label": "task", "words": ["fix", "add"], "at_start": true, "weight": 1.5}
	    ]
	  }]}`))
	c, _ := classify.Compile(p)
	model := classify.NewBayes("bayes:act", "act", classify.BayesOptions{})
	model.LearnAll(c.Examples("act"))
	e, _ := classify.NewEngine(c, model)

	for _, text := range []string{"/layout focus", "why is the build red?", "fix the build", "hmm"} {
		r := e.Classify(context.Background(), classify.Input{Text: text, Phase: classify.Keystroke})
		fr := r.Facets["act"]
		fmt.Printf("%-24q label=%-9q top=%s\n", text, fr.Label, fr.Top)
	}
	// Output:
	// "/layout focus"          label="command" top=command
	// "why is the build red?"  label="question" top=question
	// "fix the build"          label="task"    top=task
	// "hmm"                    label=""        top=command
}
