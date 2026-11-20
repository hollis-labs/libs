# steer

Host-neutral steering for an agent turn: which mode is in force, which slots are
active, which reflexes and routing rules apply, and what a classifier may
suggest. It runs no model and stores nothing. A host supplies what it knows
(the situation, its slots, modes, reflexes and rules) and gets decisions back.

```go
reg, _ := steer.NewRegistry(steer.DefaultPresets()...)
_ = reg.Add(steer.LayerModule, "drafts", steer.Preset{Name: "writing", Slots: []string{"drafts"}})
s := steer.New(reg, steer.NewFocusSelector(steer.Slot{Name: "general"}), steer.NewVocabulary())

// The owner picks a mode in the host's UI. The model cannot.
_ = s.Modes.Set(steer.Actor{Kind: steer.ActorOwner}, "writing")

turn, err := s.Prepare(ctx, steer.Input{Sit: sit, Slots: slots, Reflexes: reflexes, Classified: cls})
// turn.Instructions, turn.Selection, turn.Tools: what the model is given.
// turn.Fired.Halt: answer with this instead of calling the model.
// turn.Fired.Actions: host actions to carry out.
// turn.Suggestions: offer these to the owner; turn.Decision.TraceFields() for the trace.
```

## Pipeline

1. **Mode.** A deliberate mode (set only by the owner) wins. Otherwise a
   classified preset, if the policy lets the `preset` facet act on its own.
   Otherwise the registry's default.
2. **Slots.** The `Selector` picks from the situation; the preset adds its
   slots; a classified slot may lead if the policy allows.
3. **Reflexes.** Those enabled everywhere plus those the preset lists. Text and
   predicate triggers run here; event and focus triggers run when the host calls
   `OnEvent` and `OnFocus`.
4. **Rules.** `ActiveRules` switches on the routing rules a preset names;
   `MessageSubject` builds the subject they match.

## Presets and layers

| Layer | Who | Wins over |
| --- | --- | --- |
| `LayerDefault` | `DefaultPresets()`: general, focus, triage, research | nothing |
| `LayerModule` | a module, named as the source | defaults |
| `LayerApp` | the app | modules and defaults |

A preset of the same name in a higher layer replaces the lower one whole.

## Classifier policy

The classifier runs in the host. Per facet (`slot`, `preset`, `act`, `affect`),
the policy's gate is `shadow` (record only), `suggest` (offer to the owner
above the threshold) or `auto` (apply above the threshold). The default suggests
`slot` at 0.7 and `preset` at 0.8, and shadows the rest. Turn a facet to `auto`
only after replay shows enough precision. `act` and `affect` are recorded, never
applied, in this version.

## Reflexes

Instances are configuration (`ParseReflexes` reads a JSON array). Trigger kinds:
`text` (`{"pattern": "<regexp>"}`), `predicate` (a libs agent/core reflexes
predicate; the situation is in `attrs`: `focused.view`, `focused.module`,
`selection`, `mode`, `layout`, `source`), `event` (`{"topic": "..."}`, prefix
match at a dot) and `focus` (`{"view": "..."}`). Action kinds come from a
`Vocabulary`: `remind` and `halt` are built in; a host registers its own
(`call_tool`, `run_command`, `dispatch_to_agent`) and carries them out.
