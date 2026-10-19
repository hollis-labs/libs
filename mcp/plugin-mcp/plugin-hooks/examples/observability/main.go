// Command observability wires a concurrency-safe structured sink and shows an
// owner-generation breaker opening without exporting payloads or error messages.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"sync"
	"time"

	hooks "github.com/hollis-labs/plugin-hooks"
)

type jsonSink struct{ mu sync.Mutex }

func (s *jsonSink) Record(r hooks.TraceRecord)          { s.write(r) }
func (s *jsonSink) BreakerChanged(e hooks.BreakerEvent) { s.write(e) }
func (s *jsonSink) write(v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := json.NewEncoder(os.Stdout).Encode(v); err != nil {
		log.Print("telemetry output unavailable")
	}
}
func main() {
	no := false
	schema := json.RawMessage(`{"type":"object","additionalProperties":false}`)
	validate := func(raw json.RawMessage) error {
		var v map[string]json.RawMessage
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		if v == nil || len(v) != 0 {
			return errors.New("expected empty object")
		}
		return nil
	}
	d := hooks.Definition{
		Name: "operation.observe", Kind: hooks.Action, Mode: hooks.Sequential,
		InputSchema: schema, ValidateInput: validate, Since: "1", RemoteOK: &no,
		Budget: time.Second, HandlerTimeout: 500 * time.Millisecond,
		OnErrorDefault: hooks.Open, AllowedOnError: []hooks.ErrorPolicy{hooks.Open},
		MaxPayloadBytes: 1024, MaxHandlers: 1, MaxParallelism: 1, SchemaDigest: "example-object-v1",
	}
	registry, err := hooks.NewRegistry(hooks.Catalog{Version: "1", Definitions: []hooks.Definition{d}})
	if err != nil {
		log.Fatal(err)
	}
	scope, err := registry.NewScope(hooks.ScopeConfig{Owner: "example-plugin", Generation: "one", Hooks: []string{d.Name}})
	if err != nil {
		log.Fatal(err)
	}
	engine, err := hooks.NewEngine(registry, hooks.ExecutionConfig{Sink: &jsonSink{}})
	if err != nil {
		log.Fatal(err)
	}
	if _, err := scope.AddAction(d.Name, "observe", hooks.Options{}, func(context.Context, hooks.Invocation) error { return errors.New("example operation failed") }); err != nil {
		log.Fatal(err)
	}
	for range 6 {
		// Open policy preserves dispatch progress while records surface every failure
		// and the sixth attempt is visibly unavailable under the open breaker.
		if _, err := engine.EmitAction(context.Background(), d.Name, json.RawMessage(`{}`), nil); err != nil {
			log.Fatal(err)
		}
	}
	if err := engine.ResetBreaker("example-plugin", "one"); err != nil {
		log.Fatal(err)
	}
	if err := scope.Dispose(context.Background()); err != nil {
		log.Fatal(err)
	}
	if err := engine.Shutdown(context.Background()); err != nil {
		log.Fatal(err)
	}
}
