# steer

Host-neutral steering for an agent turn: which mode is in force, which slots are
active, which reflexes and routing rules apply, and what a classifier may
suggest. It runs no model and stores nothing. A host supplies what it knows
(the situation, its slots, modes, reflexes and rules) and gets decisions back.

```go
reg, _ := steer.NewRegistry(steer.DefaultPresets()...)
_ = reg.Add(steer.LayerModule, "drafts", steer.Preset{Name: "writing", Slots: []string{"drafts"}})
s, owner, err := steer.New(reg, steer.NewFocusSelector(steer.Slot{Name: "general"}), steer.NewVocabulary(),
	steer.WithCooldowns(myPersistedCooldowns))

// The owner picks a mode in the host's UI. Keep `owner` away from the model.
_ = owner.SetMode("writing")

turn, err := s.Prepare(ctx, steer.Input{Sit: sit, Slots: slots, Reflexes: reflexes, Classified: cls})
// turn.Fired.Halt: answer with this; run no routing or model.
// turn.Instructions, turn.Selection, turn.Tools: what the model is given.
// turn.Fired.Actions: host actions to carry out.
// turn.Suggestions: offer these to the owner; owner.Accept(v, view, slots) applies one.
// turn.Decision.TraceFields(): the prompt-free trace fields.
```

The zero `Steer` is not usable; build one with `New`.

## Pipeline

1. **Mode.** A deliberate mode wins. Otherwise a classified preset, if the
   policy lets the `preset` facet act on its own. Otherwise the registry's
   default. The default is not a mode: `Situation.Mode` stays empty for it.
2. **Slots.** The `Selector` picks from the situation; the preset adds its
   slots; a classified slot may lead if the policy allows (never for an empty
   message).
3. **Reflexes.** Those enabled everywhere plus those the preset lists, against
   the trimmed message. Event and focus triggers run when the host calls
   `Reflexes().OnEvent` and `OnFocus`.
4. **Rules.** `ActiveRules` switches on the routing rules a preset names;
   `MessageSubject` builds the subject they match.

**Order with routing.** Call `Prepare` before any routing tool runs, and route
only when `Fired.Halt` is empty: a halt answers the message and nothing else
should act on it.

**Selection reasons** (`Selection.Why`): `focus`, `pinned`, `kept` (the last
primary slot stayed), `default` (the fallback slot is primary), `mode`,
`preset`, `classified`.

## Modes and owner control

`New` returns an `*OwnerControls`: the only way to set the mode
(`SetMode`) or accept a suggestion (`Accept`). It is a capability the host
holds, not an identity a caller states, and it cannot be decoded from data.
`Accept` takes only a `suggest` verdict for a preset in the registry or a slot
the host offers; accepting a slot needs a selector that implements `Pinner`.
`Steer.Modes().State()` reads the mode and when it was set.

## Presets and layers

| Layer | Who | Wins over |
| --- | --- | --- |
| `LayerDefault` | `DefaultPresets()`: general, focus, triage, research | nothing |
| `LayerModule` | a module, named as the source | defaults |
| `LayerApp` | the app | modules and defaults |

A preset of the same name in a higher layer replaces the lower one whole
(app > module > default); between modules, the first registered wins. The
default preset cannot be removed while it is the default. `Registry.Check`
reports presets that name slots, reflexes or rules the host lacks.

## Classifier policy

The classifier runs in the host. Per facet (`slot`, `preset`, `act`,
`affect`), the gate is `shadow` (record only), `suggest` (offer to the owner
above the threshold) or `auto` (apply above the threshold, which must be at
least `AutoFloor`, 0.5). An unknown gate is treated as shadow, and a
confidence that is NaN or outside 0 to 1 counts as abstained. The default
suggests `slot` at 0.7 and `preset` at 0.8, and shadows the rest. Set the
policy with `WithPolicy` or `SetPolicy`; both validate it.

**`act` and `affect` are data only in this version:** recorded in the trace,
never suggested or applied.

## Reflexes

Instances are configuration (`ParseReflexes` reads a JSON array). Trigger kinds:
`text` (`{"pattern": "<regexp>"}`), `predicate` (a libs agent/core reflexes
predicate), `event` (`{"topic": "..."}`, prefix match at a dot) and `focus`
(`{"view": "..."}`). Predicates see `attrs`: `focused.view`,
`focused.module`, `selection`, `mode`, `layout`, `source`, and each host
signal as `signal.<key>` (`Situation.Signals["mail.unread"]` is
`signal.mail.unread`), so a signal can never stand in for a built-in
attribute. A `StateFill` (`WithStateFill`) sets typed state such as unread
mail, prefix tokens or scope tier; `WithAgentID` names the agent.

Action kinds come from a `Vocabulary`: `remind` and `halt` are built in; a host
registers its own (`call_tool`, `run_command`, `dispatch_to_agent`) and
carries them out. A reflex whose kind is not in the vocabulary is skipped and
reported, never returned as an action, and so is an invalid reflex handed to
`Prepare`. Cooldowns live in a `CooldownStore` (`WithCooldowns`), whose calls
take a context and may fail; a store error keeps the reflex from firing and is
reported. `MemoryCooldowns` has `Snapshot` and `Restore` so a host can persist
it and not re-fire after a restart. `NewReflexes` needs a vocabulary and takes
no policy.

## Trace privacy

`Decision.TraceFields` fills the libs agent/trace record's reserved fields
with no prompt content: the view and module in front, the open views, the mode
and its source, the layout, the turn source, the slots, the selector's reason,
classifier labels and confidences for the known facets only, the deliberate
mode when its preset is gone, and the reflexes that fired. Message text,
selections, window titles and host signals stay out.
