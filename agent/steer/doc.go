// Package steer decides how an agent approaches a turn before the model sees it: which mode
// is in force, which slots are active, which reflexes and routing rules apply, and what a
// classifier may suggest. It is host-neutral. A host (a desktop app, a headless runner)
// supplies the Situation and the slots, modes, reflexes and rules it knows; this package
// returns decisions. It runs no model, stores nothing and imports no application.
//
// # The pipeline
//
// A host calls [Steer.Prepare] once per owner message:
//
//  1. Mode. A deliberate mode, set by the owner through [Modes.Set], wins. Otherwise a
//     classified preset may apply, but only if the [Policy] lets its facet act on its own;
//     by default it is only suggested. Otherwise the registry's default preset applies.
//  2. Slots. A [Selector] picks the active slots from the Situation: the window in front,
//     the mode, a pinned slot. The preset may add slots by name. A classified slot may lead,
//     again only as far as the Policy allows.
//  3. Reflexes. The ones switched on everywhere, plus the ones the preset activates, are run
//     against the message. Text and predicate triggers fire here; event and focus triggers
//     fire when the host reports the event or the focus change.
//  4. Rules. A preset can switch on routing rules that are off by default ([ActiveRules]);
//     [MessageSubject] gives the fields a rule matches on.
//
// The result, [Turn], carries what the host gives the model (instructions, the selection,
// reminders, an answer that stops the turn) and a [Decision] for the turn's trace.
//
// # Modes and presets
//
// A [Preset] is a named bundle of behaviour: slots, standing instructions, active reflexes
// and rules, tool emphasis, output style, a delivery policy name and the dispatch strategies
// allowed. A mode is a preset the owner chose. Presets come in layers ([Registry]): the
// defaults this package ships, presets modules contribute, and the app's own. A preset of
// the same name in a higher layer replaces a lower one whole; the app is the highest.
//
// # Reflexes
//
// Reflex instances are configuration ([Reflex]): a trigger kind and spec, an action kind and
// spec, a cooldown. The trigger kinds are fixed here (text, predicate, event, focus); the
// action kinds are a vocabulary ([Vocabulary]). This package carries out remind and halt
// itself; any other kind the host registers is returned to the host to carry out.
//
// # Classifier
//
// The classifier runs in the host. Its results come in per facet ([Classification]). The
// [Policy] says, per facet, whether a result is only recorded (shadow), offered to the
// owner (suggest), or applied when it clears its threshold (auto). Nothing auto-applies by
// default.
package steer
