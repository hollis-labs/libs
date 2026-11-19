// Package codexappserver decodes the JSON-RPC messages `codex app-server`
// writes to stdout into chatstream events, for one turn of one thread.
//
// The app-server speaks JSON-RPC 2.0 over stdio, one message per line, without
// the "jsonrpc" member. A client starts a thread (thread/start) and a turn
// (turn/start); the server answers each request and then streams notifications
// until turn/completed. This decoder reads that stream; the requests it does
// not send, and the server requests it does not answer.
//
// # Mapping
//
//	thread/started                    run.start (thread id in Ext "codex"; it
//	                                  becomes the run id when none was configured)
//	turn/started                      step.start, the turn id as the step id
//	                                  (run.start first, with the turn id, if needed)
//	item agentMessage                 text part, opened by item/started or the
//	                                  first delta; item/agentMessage/delta streams;
//	                                  item/completed ends it with final {"text"}.
//	                                  phase commentary/final_answer is Meta phase
//	                                  narration/final
//	item reasoning                    reasoning part; item/reasoning/summaryTextDelta
//	                                  streams (a new summaryIndex adds a blank
//	                                  line); final {"text": the summary sections
//	                                  joined, "content": the raw reasoning if any}
//	item commandExecution,            tool_call part on item/started (arguments
//	     mcpToolCall, fileChange,     whole: command and cwd, MCP server, tool and
//	     webSearch, dynamicToolCall,  arguments, the changes, the query), then a
//	     collabAgentToolCall          tool_result part on item/completed; an item
//	                                  that first appears completed gets both
//	server request: item/commandExecution/requestApproval,
//	  item/fileChange/requestApproval, item/permissions/requestApproval,
//	  applyPatchApproval, execCommandApproval
//	                                  activity "codex.approval_request"
//	                                  {request_id, method, item_id, params}; not
//	                                  answered, not an approval.request
//	server request: item/tool/requestUserInput, mcpServer/elicitation/request
//	                                  activity "codex.user_input_request" (same shape)
//	serverRequest/resolved            activity "codex.server_request_resolved"
//	error                             activity "codex.error" {message, will_retry,
//	                                  codex_error_info, additional_details}; NOT
//	                                  terminal: a fatal one is followed by
//	                                  turn/completed failed
//	warning                           activity "codex.warning" {message}
//	turn/plan/updated                 activity "codex.plan" {explanation, plan}
//	turn/diff/updated                 activity "codex.diff" {diff}
//	thread/tokenUsage/updated         no event; the turn's usage rides on run.finish
//	turn/completed completed          run.finish (stop) with the turn's usage
//	turn/completed interrupted        run.abort "interrupted"
//	turn/completed failed             run.error "turn_failed", the turn error's
//	                                  message (an upstream JSON error body is
//	                                  unwrapped to its message); codex_error_info
//	                                  and additional_details in Ext "codex"
//	turn/completed, other status      run.error "upstream_error"
//	a response (id, no method), any   raw (never terminal)
//	other notification or server
//	request, another thread's
//	notification, a line that is
//	not a JSON-RPC message
//
// Exactly one terminal event ends the run, and only turn/completed produces
// one. A stream that ends without it (Close) closes what is open and ends with
// run.error upstream_truncated.
//
// # final
//
// Every part.end carries the part's complete content in final, as the chat wire
// requires. A text or reasoning part's final is {"text": ...}: the completed
// item's text, or, when the part is closed without completing (an interrupted
// or truncated turn), the text that streamed. When a completed item's text does
// not extend what streamed (a rewrite), the completed text is final and the
// item is also kept as a raw event. A tool_call's final is its arguments. A
// tool_result's final is {"text", "is_error", "status"} plus the item's
// structured outputs (exit_code, duration_ms, result, error, changes, results,
// content_items, agents_states); text is the command's aggregated output, the
// MCP error message or text content, the changed paths with their kind, the
// dynamic tool's input text, or the results as JSON.
//
// is_error is set from the item: commandExecution failed or declined or a
// non-zero exit code, mcpToolCall failed or with an error, fileChange failed or
// declined, dynamicToolCall failed or success false, others failed.
//
// # Usage
//
// thread/tokenUsage/updated reports the thread's running total and the last
// model call, after each call. The turn's usage is the latest total minus the
// total before the turn's first call (its total minus its last). Codex's counts
// are inclusive (input contains cached and cache-write input, output contains
// reasoning output) and become chatstream's disjoint components with
// UsageFromInclusive. Counters that contradict themselves are not reported; the
// reason goes in the run.finish's Ext under "codex".
//
// # Limits
//
// One decoder is one turn: lines after turn/completed are not read, and a
// second turn/started is kept raw. The decoder follows the first thread id it
// sees; another thread's notifications are raw. JSON-RPC responses cannot be
// matched to the requests the decoder did not see, so an error response to
// turn/start is raw and the stream ends truncated. item/commandExecution/
// outputDelta, item/fileChange/patchUpdated, item/mcpToolCall/progress and
// item/reasoning/textDelta are raw: the completed item carries the same content.
package codexappserver
