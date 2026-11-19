// Package classify labels short text (a chat message, a command line, a field's
// value) along several independent facets, with cheap judges first and dearer
// ones only when a caller asks for them.
//
// # Facets
//
// A classification is not one class. Each facet ("act", "slot", "affect") has
// its own closed label set, its own thresholds, and its own answer, which may be
// an abstention. A Result holds one FacetResult per facet.
//
// # Judges and votes
//
// A Judge reads an Input (the text and a flat map of context fields such as the
// view in front or the mode) and casts votes: a facet, a label, a weight in
// log-odds units, and the evidence. A judge never decides; it is a labelling
// function. The engine adds the votes per facet, turns the sums into
// probabilities, and abstains when the top label is not probable enough or not
// far enough ahead of the next. A vote marked Decisive settles its facet (a
// leading slash is a command, whatever else is said).
//
// Every judge declares the cheapest Phase it may run in: Keystroke (as the
// owner types: rules, priors, the n-gram model), Submit (when the text is sent:
// embeddings) or Async (later: a model call). Classify runs the judges whose
// phase is at or below the input's phase. A later phase gives a new Result for
// the same input; the ledger keeps both.
//
// # Packs
//
// A Pack is the declarative part: facets, their labels with examples, rules
// over the text and the context, and thresholds. Packs layer (built-in, then
// user, then project, then module), a later pack overriding an earlier one by
// facet, label and rule id. The same examples seed the n-gram model and the
// centroid judge and are what an LLM judge is given as its closed label set, so
// one file describes a facet to every tier. A compiled pack is fingerprinted;
// a Result records which content produced it.
//
// # Learning and evaluation
//
// Bayes is a hashed n-gram naive Bayes model that learns one example at a time
// and serialises to JSON. Centroid scores the text against each label's
// examples through an Embedder, which lives outside this package (a local ONNX
// model needs cgo and is its own module). Evaluate measures an engine on
// labelled examples: accuracy, coverage, accuracy by confidence band and the
// majority-label floor any judge has to beat.
//
// Result.LedgerRecords turns a result into one ledger row per facet, so
// decisions, corrections and training labels share the ledger's store.
//
// The package is standard library plus the ledger package.
package classify
