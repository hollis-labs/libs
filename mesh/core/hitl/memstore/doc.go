// Package memstore is an in-memory hitl.Store for tests, examples and
// single-process use. It applies hitl.CheckSwap under one mutex, so Create and
// Swap are atomic and terminal records never change. Nothing is persisted.
package memstore
