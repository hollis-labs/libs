// Package codexjson decodes the JSON Lines that `codex exec --json` writes to
// stdout into chatstream events, for one turn of one thread.
//
// # Mapping
//
//	thread.started                run.start (thread_id in Ext "codex"; it also
//	                              becomes the run id when none was configured)
//	turn.started                  step.start "turn-N"
//	item agent_message            text part, whole (one delta)
//	item reasoning                reasoning part, whole (a summary, no signature)
//	item command_execution,       tool_call part on item.started (arguments whole:
//	     mcp_tool_call,           the command, the MCP arguments, the changes, the
//	     file_change,             query), then a tool_result part on
//	     web_search,              item.completed; an item that first appears
//	     collab_tool_call         completed (file_change always does) gets both
//	item todo_list                activity "codex.todo_list" on every event
//	item error                    activity "codex.error" carrying the raw line;
//	                              NOT terminal: the docs call it a non-fatal item
//	turn.completed                run.finish (stop) with the turn's usage
//	turn.failed                   run.error "turn_failed" (terminal)
//	error                         run.error "upstream_error" (terminal)
//	anything else, or a line     raw (never terminal)
//	that does not parse
//
// Codex text arrives whole, in one agent_message item, so capability Text is
// GranularityFinal. A text or reasoning item that shows text before it
// completes (started or updated) is streamed: the growth is sent as deltas as
// long as each text extends the last; a rewrite is kept as a raw event and the
// part is finished with what had been sent.
//
// A tool_result's is_error is set from the item: command_execution with status
// failed or declined or a non-zero exit code, mcp_tool_call with status failed
// or an error, file_change or collab_tool_call with status failed. The result
// part's Final holds the item's outputs (aggregated_output and exit_code,
// result/error, changes, results, agent states) and its status.
//
// # Usage
//
// turn.completed carries the turn's counters, once. Codex's counts are
// inclusive, like OpenAI's: input_tokens contains cached_input_tokens and
// cache_write_input_tokens, and output_tokens contains
// reasoning_output_tokens. They become chatstream's disjoint components with
// UsageFromInclusive, so nothing is counted twice, and ride on the run.finish
// with scope final. The docs say "during the turn", so by default they are taken
// as per-turn. If a caller knows a stream reports thread-cumulative counters
// (a resumed thread), WithCumulativeUsage(baseline) subtracts the counters the
// thread had reached before this turn. Counters that contradict themselves, or
// go backwards from the baseline, are not reported; the reason goes in the
// run.finish's Ext under "codex".
//
// # Limits
//
// A line that comes before thread.started (noise) starts the run without a
// thread id; the real thread.started is then kept as a raw event, so the id is
// not lost, only not in run.start's Ext.
//
// One decoder is one turn: the run ends at the first turn.completed,
// turn.failed or error, and lines after it are not read. There is no finish
// reason in the format, so a completed turn is always "stop". Codex emits no
// approval events and has no documented cancel.
package codexjson
