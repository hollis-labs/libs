// Package steer decides how an agent approaches a turn before the model sees it: which mode
// is in force, which slots are active, which reflexes and routing rules apply, and what a
// classifier may suggest. It is host-neutral. A host (a desktop app, a headless runner)
// supplies the Situation and the slots, modes, reflexes and rules it knows; this package
// returns decisions. It runs no model, stores nothing and imports no application.
//
// # The pipeline
//
// A host builds a [Steer] with [New] (the zero Steer is not usable) and calls
// [Steer.Prepare] once per owner message, before any routing tool runs:
//
//  1. Mode. The deliberate mode wins. Otherwise a classified preset applies, but only if
//     the [Policy] lets the preset facet act on its own; by default it is only suggested.
//     Otherwise the registry's default preset applies. The default is not a mode: the
//     Situation's Mode stays "" for it, so it neither selects slots by mode nor matches mode
//     conditions.
//  2. Slots. A [Selector] picks the active slots from the Situation: the window in front,
//     the mode, a pinned slot. The preset may add slots by name. A classified slot may lead,
//     again only as far as the Policy allows, and never for an empty message.
//  3. Reflexes. The ones switched on everywhere, plus the ones the preset activates, run
//     against the trimmed message. Text and predicate triggers fire here; event and focus
//     triggers fire when the host reports the event or the focus change.
//  4. Rules. A preset can switch on routing rules that are off by default ([ActiveRules]);
//     [MessageSubject] gives the fields a rule matches on.
//
// A halt from step 3 answers the message, and the host should then run nothing else on it,
// routing included: run Prepare first, and route only when Fired.Halt is empty. (Surface's
// older stack routed first; this order is deliberate.)
//
// The result, [Turn], carries what the host gives the model (instructions, the selection,
// reminders, an answer that stops the turn) and a [Decision].
//
// # Owner control
//
// Only the owner sets the mode or accepts a suggestion. [New] returns an [OwnerControls]
// capability for that; the host keeps it away from the model and from anything that comes
// in as data. It cannot be built or decoded. [OwnerControls.Accept] takes only a suggest
// outcome for a preset or slot the host knows.
//
// # Modes and presets
//
// A [Preset] is a named bundle of behaviour: slots, standing instructions, active reflexes
// and rules, tool emphasis, output style, a delivery policy name and the dispatch strategies
// allowed. Presets come in layers ([Registry]): the defaults this package ships, presets
// modules contribute, and the app's own. A preset of the same name in a higher layer
// replaces a lower one whole: app > module > default. [Registry.Check] reports presets that
// name slots, reflexes or rules the host does not have.
//
// # Reflexes
//
// Reflex instances are configuration ([Reflex]): a trigger kind and spec, an action kind and
// spec, a cooldown. The trigger kinds are fixed here (text, predicate, event, focus); the
// action kinds are a vocabulary ([Vocabulary]). This package carries out remind and halt
// itself; any other kind the host registers is returned to the host to carry out, and a kind
// outside the vocabulary is never returned. Cooldowns live in a [CooldownStore] the host may
// persist. Predicates see the situation as attributes, the host's Signals, and whatever a
// [StateFill] adds (unread mail, prefix pressure, scope tier).
//
// # Classifier
//
// The classifier runs in the host. Its results come in per facet ([Classification]). The
// [Policy] says, per facet, whether a result is only recorded (shadow), offered to the
// owner (suggest), or applied when it clears its threshold (auto, at least [AutoFloor]).
// Nothing auto-applies by default. In this version only the slot and preset facets act; act
// and affect are recorded as data only.
//
// # Trace
//
// [Decision.TraceFields] fills a libs agent/trace record's reserved fields. It records no
// prompt: no message text, selection, window titles or host signals, only views, modes,
// slots, the selector's reason, classifier labels and confidences, and the reflexes that
// fired.
package steer
