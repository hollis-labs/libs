// Package boot prepares a resolved dispatch through the workspace engine.
// Plan is pure. Prepare performs only the effects admitted by that engine;
// neither call loads definitions, executes hooks, starts an agent or grants
// access. Process descriptions are launch instructions, not readiness proofs.
package boot
