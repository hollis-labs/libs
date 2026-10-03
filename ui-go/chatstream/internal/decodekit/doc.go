// Package decodekit is the lifecycle bookkeeping every dialect decoder shares:
// stamping events, tracking what is open, refusing to emit past the terminal
// event, and building the events that make a truncated stream well formed. A
// decoder embeds Base and keeps only its dialect's parsing to itself.
package decodekit
