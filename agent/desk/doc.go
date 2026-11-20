// Package desk is an agent's front desk: the layer between the owner and
// the chat agent that reads each message, splits it into work, keeps
// track of that work by topic, and paces what comes back.
//
// It has three parts, all host-neutral and free of I/O:
//
//   - Intake screens a message with rules and, when it is worth it, plans
//     it with a Decomposer (Rules, or Model over any small model) into a
//     Plan of work items with kinds, dependencies and expected outcomes.
//     IntakeShadow records plans, IntakeSuggest shows them as a card for
//     the owner to accept, IntakeAuto runs confident ones.
//   - Ledger keeps one Topic per open line of business: work in flight,
//     results, questions waiting on the owner, and related refs (jobs,
//     threads, tasks, seats). Find routes a finished job back to its topic.
//   - Editor is the delivery editor: every result, failure, question,
//     progress note or notice becomes an Item, a Policy decides to release
//     it now, merge it with more on its topic, hold it while the owner
//     answers a question on that topic, add it to a digest, or file it
//     without a message, and Due composes everything ready into one
//     Message.
//
// DefaultPolicy ships sane defaults; apps and modules put their own rules
// over it with Layer. Two invariants hold whatever the rules say: a
// critical item and a question to the owner always go out now.
//
// The host runs the work (for Nanite, subtasks and Dispatch), sends the
// messages and persists the ledger. Mode names are plain strings so the
// package does not depend on a mode registry.
package desk
