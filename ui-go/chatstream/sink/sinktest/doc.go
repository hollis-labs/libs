// Package sinktest is the shared test kit for sink encoders: a canonical set of
// event scenarios, a recording Writer, a Writer that fails on demand, parsing of
// encoded output back into SSE frames with go-ssekit, and golden-file helpers.
// Every encoder's tests run the same scenarios so their outputs can be compared
// side by side.
package sinktest
