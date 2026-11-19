# classify

Classifies short text (a chat message, a command line, a field's value) along several independent facets. Cheap judges (rules, context priors, an n-gram model) run as the owner types; dearer ones (embeddings, model calls) run on submit or later. Every answer can abstain, and every result maps to decision-ledger rows, so corrections become training labels.

## Status

**Pre-release.** This package is unreleased and has no outside consumers yet. Interfaces and behavior may change without notice, and there are no compatibility guarantees until the first tagged release of its module.

## Install

```sh
go get github.com/hollis-labs/libs/core/util/classify
```

## Model

- **Facet**: one question with a closed label set ("act": command, question, task…). A `Result` has one `FacetResult` per facet, each with probabilities, the top label, the margin, the votes, and an answer or an abstention.
- **Judge**: casts `Vote`s (facet, label, weight in log-odds, evidence). The pack's rules are one judge; `Bayes` and `Centroid` are others; anything implementing `Judge` can be added (an LLM call at `Async`).
- **Phase**: `Keystroke` < `Submit` < `Async`. A judge declares the cheapest phase it may run in.
- **Pack**: facets, labels with examples, rules and thresholds, as strict JSON. `Layer` merges packs, later over earlier, by facet, label and rule id. The examples seed `Bayes` and `Centroid` and are the closed label set a model judge is given.

## Pack rules

A rule votes for a label when its `when` conditions on the context hold (`eq`, `not_eq`, `in`, `prefix`, `exists`, `absent`) and its text test matches: one of `pattern` (a Go regular expression over the lower-cased, whitespace-collapsed text), `prefix`, or `words` (whole words or phrases, optionally `at_start`). A rule with no text test is a prior on the context alone. `weight` is log-odds (default 1); `decisive` settles the facet.

```json
{"name": "composer", "version": "1", "facets": [{
  "name": "act", "min_p": 0.5, "min_margin": 0.1,
  "labels": [{"name": "command", "examples": ["/open notes"]},
             {"name": "question", "examples": ["what is open?"]},
             {"name": "approval", "min_p": 0.8, "examples": ["yes go ahead"]}],
  "rules": [{"id": "slash", "label": "command", "prefix": "/", "decisive": true},
            {"id": "qmark", "label": "question", "pattern": "\\?$", "weight": 2},
            {"id": "review", "label": "approval", "when": [{"field": "mode", "op": "eq", "value": "review"}]}]
}]}
```

## Evaluation

`Evaluate` reports, per facet, accuracy, precision on what was answered, coverage, accuracy of the top label by probability band, the confusion matrix, and the majority-label floor. Hold evaluation examples out of training.
