# desk

An agent's front desk: the layer between the owner and the chat agent. It
reads each message and splits it into work (intake), keeps track of that
work by topic (the ledger), and paces what comes back so the owner gets one
composed message instead of a stream of notices (the delivery editor).

The package is host-neutral and has no dependencies and no I/O. The host
runs the work (for Nanite: subtasks and Dispatch), sends the messages, and
persists the ledger. Mode names are plain strings.

```
owner message ─▶ Intake ─▶ Plan ─▶ host runs the work ─▶ Items ─▶ Editor ─▶ Message ─▶ owner
                    ▲                       │                        ▲
                    └──────── Ledger ◀──────┴── results, questions ──┘
```

## Intake

```go
in := desk.Intake{Mode: desk.IntakeSuggest, Planner: desk.Model{Complete: small, Instructions: seats}}
r, err := in.Read(ctx, desk.Request{Message: msg, Mode: mode, Topics: ledger.OpenTopics()})
switch r.Disposition {
case desk.Chat:     // the chat agent handles it as usual
case desk.Suggest:  // show r.Plan.Card(); run it if the owner accepts, record a desk.Correction
case desk.Dispatch: // run r.Plan wave by wave: r.Plan.Waves()
}
```

- `Screen` is the cheap first read: list items, several questions, joiners
  ("and also"), task verbs. Only messages it passes reach the planner.
- `Model` asks a small model for a JSON plan, validates it, and retries once
  with the error. `Rules` is the fallback when there is no model or it fails.
- Work kinds: `answer` (the chat agent), `research`, `analysis`, `council`,
  `delegate` (a change for the owning seat, with acceptance criteria) and
  `question` (only the owner can decide).
- Modes: `off`, `shadow` (plan and record, do nothing), `suggest` (show the
  plan as a card), `auto` (run plans at or above `AutoConfidence`, default
  0.8, and suggest the rest). Start with shadow or suggest.

## Ledger

```go
l := desk.NewLedger(nil)
t, _ := l.Open("", "Orchestration fixes", mode)
_ = l.Attach(t.ID, desk.Work{ID: "w1", Kind: desk.KindResearch, State: desk.WorkRunning, Ref: desk.Ref{Kind: "job", ID: job}})
// later, when the job finishes:
topic, _ := l.Find(desk.Ref{Kind: "job", ID: job})
_ = l.Progress(topic.ID, "w1", desk.WorkDone, summary)
```

A topic holds its work, the questions waiting on the owner (`Ask`,
`Answer`, `HasOpenQuestion`) and related refs. `OpenTopics` feeds intake so a
message can continue a topic. The ledger marshals to JSON for the host to
keep.

## Delivery editor

```go
policy := desk.Layer(desk.DefaultPolicy(), appPolicy, modulePolicy) // later layers win
ed, _ := desk.NewEditor(policy, desk.WithLedger(l))
ed.Submit(desk.Item{ID: id, Topic: topic.ID, Kind: desk.ItemResult, Source: "subtask", Text: summary}, state)
if m, ok := ed.Due(state); ok { send(m.Text) }
if at, ok := ed.Next(state); ok { timer.Reset(time.Until(at)) }
```

Call `Due` when an item is submitted, when the owner's state changes (stops
typing, a turn ends), when a question is answered, and at the time `Next`
returns.

| Action | Released |
| --- | --- |
| `now` | at the next `Due` |
| `merge` | after `MergeWindow` (20s), or with a release on the same topic |
| `hold` | when the topic's open question is answered, or after `HoldMax` (30m) |
| `digest` | at `DigestMax` (10) items or after `DigestEvery` (30m); rides along with any other release unless `Quiet` |
| `file` | never; kept in `Filed` for a done-for-you log |

The default rules, first match wins:

| Rule | Matches | Action |
| --- | --- | --- |
| `focus` | in mode `focus`, results, failures and notices up to normal urgency | digest |
| `urgent` | high urgency | now |
| `preferred-now` | the producer asked for now | now |
| `waiting-on-owner` | results, failures, notices on a topic with an open question | hold |
| `preferred-digest`, `preferred-file` | the producer asked | digest, file |
| `results` | results and failures | merge |
| `progress` | progress | file |
| `notices` | low-urgency notices | digest |
| default | anything else | merge |

Two invariants come before the rules: a critical item and a question to the
owner always go out now. While the owner is busy (typing, or a turn is
running) only those interrupt; everything else waits until the owner is free
or `BusyMax` passes.

A release is one `Message`: items grouped by topic (topics in order of their
first item, questions first within a topic), rendered by `DefaultComposer` or
a host's `WithComposer`.
