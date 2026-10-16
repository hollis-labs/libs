# ledger

The decision ledger record: one immutable row per decision (rule, classifier, model, human, reflex, policy) and an append-only event log for what happens next. Storage-agnostic: it holds the types, validation, the idempotency key, retention shaping, the `Store` interface, an in-memory store and a conformance suite.

## Status

**Pre-release.** This package is unreleased and has no outside consumers yet. Interfaces and behavior may change without notice, and there are no compatibility guarantees until the first tagged release of its module.

See the module's `CHANGELOG.md` for what exists and what changed.

## Install

```sh
go get github.com/hollis-labs/libs/core/util/ledger
```

Standard library only.

## Usage

```go
store := &ledger.MemStore{}

input, _ := ledger.DefaultRetention().Shape(ledger.StageClassifier,
	json.RawMessage(`{"text":"is this urgent?"}`), "", false, time.Now())
version := ledger.Version{Label: "set-7", Hash: "9f2c"}

rec, _, _ := store.Append(ctx, ledger.Record{
	IdempotencyKey: ledger.Key("msg-1", ledger.StageClassifier, version),
	Stage:          ledger.StageClassifier,
	SubjectRef:     "msg-1",
	Input:          input,
	Version:        version,
	Output:         ledger.Output{Label: "question"},
	Disposition:    ledger.Suggested,
})

// a person corrects it; the original row is untouched
store.AppendEvent(ctx, ledger.Event{DecisionID: rec.DecisionID, Kind: ledger.EventCorrection,
	Correction: &ledger.Correction{Label: "urgent", Actor: "urn:example:user:1"}})
entry, _ := store.Get(ctx, rec.DecisionID)
label, corrected := entry.Label() // "urgent", true
```

## Rows and events

- A `Record` is written once. `Key(subject, stage, version)` is its idempotency key: re-running the same stage over the same subject with the same rule set or model appends nothing, and a new version appends a new row.
- Events are numbered per decision: a **disposition** change, a **correction** (a label and a non-empty actor) or an **outcome** (`success`, `failure`, `unknown`, and a reference).
- A disposition moves one way: `suggested` can become `applied`, `accepted`, `rejected` or `superseded`; every other state can only become `superseded`. A repeated move is refused.
- `Entry.Label()` is the training label: the latest correction, else the original output. `Entry.Current()` and `Entry.Outcome()` read the rest.

## Retention

Metadata, hashes, labels, corrections and outcomes are kept indefinitely. Only the raw input is governed, by `Retention.Shape` per stage:

| Setting | Effect |
|---|---|
| `Mode` | `snapshot` (the input), `pointer` (a ref to it) or `none` (hash only) |
| `RawTTL` | when a snapshot may be dropped; zero never expires, so sensitive stages should not leave it zero |
| `Sample` | fraction of snapshots kept; the rest become a pointer, or a hash only |
| `Redact` | rewrites the snapshot before storage; an error refuses the write |
| `Keep` (per call) | exempts a training row from expiry and sampling |
| `HashKey` | makes the input hash an HMAC, so short inputs cannot be brute-forced from it |

A `Store` that implements `Expirer` drops expired snapshots and keeps every other field.

## Stores

`Store` is `Append` (idempotent on the key, safe under concurrent duplicates), `AppendEvent`, `Get` and `Query`. `ledgertest.Run(t, newStore)` is the suite every implementation must pass.

## Limits

- `MemStore` is for tests and single-process use.
- A correction's actor is a non-empty string. Authenticating it is the service's job.
- The ledger stores what it is given; deciding what a decision's input is, and how long it may be kept, is the caller's retention policy.
