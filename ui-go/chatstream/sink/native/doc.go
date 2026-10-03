// Package native encodes chatstream events as themselves: SSE with the event's
// Seq as the frame id (so a client resumes with Last-Event-ID), the verb as the
// event name, and the event's JSON as the data. Nothing is dropped or
// translated, gap and raw events included; a native client decodes each data
// payload straight back into a chatstream.Event.
package native
