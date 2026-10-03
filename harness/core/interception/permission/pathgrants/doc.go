// Package pathgrants is an in-process, session-scoped store of path grants.
//
// A host scans user messages for explicit path mentions (see
// ExtractPathMentions); each mention grants the literal path and its parent
// directory to that session until it is cleared. Worker sessions can inherit
// a parent session's grants through RegisterLineage, and a derived
// permission.RuleSet can be stored beside the grants (RegisterDerivedRules).
//
// Grants only widen access: a host's file tools consult IsPathAllowed (or
// the Checker carried by WithPathGrants / FromContext) after their own
// allow-list rejects a path. The store performs no traversal or symlink
// checks; the caller's path-safety layer still applies.
//
// The package imports the root permission package (for RuleSet); the root
// never imports this one.
package pathgrants
