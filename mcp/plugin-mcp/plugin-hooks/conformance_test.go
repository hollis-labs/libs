package pluginhooks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"
)

type corpusRegistration struct {
	Name     string `json:"name"`
	Behavior string `json:"behavior"`
	Output   string `json:"output"`
	Options  struct {
		Priority  *int        `json:"priority"`
		Once      bool        `json:"once"`
		TimeoutMS *float64    `json:"timeoutMs"`
		OnError   ErrorPolicy `json:"onError"`
		View      string      `json:"view"`
	} `json:"options"`
}
type corpusExpected struct {
	Status  Status   `json:"status"`
	Order   []string `json:"order"`
	Classes []string `json:"classes"`
	Value   *string  `json:"value"`
}
type corpusCase struct {
	Config struct {
		MaxDepth int `json:"maxDepth"`
	} `json:"config"`
	Name          string               `json:"name"`
	Hook          string               `json:"hook"`
	Payload       string               `json:"payload"`
	Registrations []corpusRegistration `json:"registrations"`
	Expected      []corpusExpected     `json:"expected"`
	Seen          []string             `json:"seen"`
	Remove        []string             `json:"remove"`
	Dispose       bool                 `json:"dispose"`
	Warnings      *int                 `json:"warnings"`
	Receipt       Status               `json:"receipt"`
	Error         string               `json:"error"`
}

func corpusValidator(raw json.RawMessage) error {
	if !json.Valid(raw) || string(raw) == `{"reject":true}` {
		return errors.New("rejected")
	}
	return nil
}
func corpusCatalog(doc CatalogDocument) Catalog {
	c := Catalog{Version: doc.CatalogVersion}
	for _, d := range doc.Definitions {
		v := Definition{Name: d.Name, OwnerNamespace: d.OwnerNamespace, Kind: d.Kind, Mode: d.Mode, InputSchema: d.InputSchema, OutputSchema: d.OutputSchema, MutablePaths: d.MutablePaths, Since: d.Since, Deprecated: d.Deprecated, RemoteOK: d.RemoteOK, Budget: time.Duration(d.BudgetMS * float64(time.Millisecond)), HandlerTimeout: time.Duration(d.HandlerTimeoutMS * float64(time.Millisecond)), OnErrorDefault: d.OnErrorDefault, AllowedOnError: d.AllowedOnError, MaxPayloadBytes: d.MaxPayloadBytes, MaxHandlers: d.MaxHandlers, MaxParallelism: d.MaxParallelism, Views: d.Views, RequiredView: d.RequiredView, SchemaDigest: d.SchemaDigest, ValidateInput: corpusValidator}
		if v.Kind == Filter {
			v.ValidateOutput = corpusValidator
		}
		c.Definitions = append(c.Definitions, v)
	}
	return c
}
func sameJSON(t *testing.T, got, want string) {
	t.Helper()
	a, ae := decode([]byte(got))
	b, be := decode([]byte(want))
	if ae != nil || be != nil || !reflect.DeepEqual(a, b) {
		t.Fatalf("JSON got %s want %s", got, want)
	}
}
func TestSharedConformance(t *testing.T) {
	raw, err := os.ReadFile("conformance/execution.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		Catalog CatalogDocument `json:"catalog"`
		Cases   []corpusCase    `json:"cases"`
	}
	decoderErr := json.Unmarshal(raw, &corpus)
	if decoderErr != nil {
		t.Fatal(decoderErr)
	}
	for _, tc := range corpus.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			r, err := NewRegistry(corpusCatalog(corpus.Catalog))
			if err != nil {
				t.Fatal(err)
			}
			e, err := NewEngine(r, ExecutionConfig{MaxDepth: tc.Config.MaxDepth})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if shutdownErr := e.Shutdown(context.Background()); shutdownErr != nil {
					t.Error(shutdownErr)
				}
			}()
			s, err := r.NewScope(ScopeConfig{Owner: "example", Generation: "1", Hooks: []string{tc.Hook}})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if disposeErr := s.Dispose(context.Background()); disposeErr != nil {
					t.Error(disposeErr)
				}
			}()
			handles := map[string]Handle{}
			var seen []string
			var mu sync.Mutex
			for _, reg := range tc.Registrations {
				fn := func(ctx context.Context, in Invocation) (json.RawMessage, error) {
					mu.Lock()
					seen = append(seen, string(in.Payload))
					mu.Unlock()
					switch reg.Behavior {
					case "work":
						time.Sleep(30 * time.Millisecond)
						return nil, nil
					case "timeout":
						time.Sleep(30 * time.Millisecond)
						return nil, nil
					case "nested":
						_, nestedErr := e.EmitAction(ctx, tc.Hook, in.Payload, nil)
						return nil, nestedErr
					case "error":
						return nil, errors.New("handler error")
					case "panic":
						panic("handler panic")
					case "cancel":
						return nil, ErrCancelled
					case "approval":
						return nil, ErrApprovalRequired
					case "echo":
						return in.Payload, nil
					}
					if reg.Output != "" {
						return json.RawMessage(reg.Output), nil
					}
					return nil, nil
				}
				o := Options{Priority: reg.Options.Priority, Once: reg.Options.Once, OnError: reg.Options.OnError, View: reg.Options.View}
				if reg.Options.TimeoutMS != nil {
					timeout := time.Duration(*reg.Options.TimeoutMS * float64(time.Millisecond))
					o.Timeout = &timeout
				}
				var h Handle
				if r.definitions[tc.Hook].Kind == Filter {
					h, err = s.AddFilter(tc.Hook, reg.Name, o, fn)
				} else {
					h, err = s.AddAction(tc.Hook, reg.Name, o, func(ctx context.Context, in Invocation) error { _, callErr := fn(ctx, in); return callErr })
				}
				if err != nil {
					t.Fatal(err)
				}
				handles[reg.Name] = h
			}
			if tc.Warnings != nil {
				if got := len(s.Registrations()[0].Warnings); got != *tc.Warnings {
					t.Fatalf("warnings %d", got)
				}
			}
			for _, n := range tc.Remove {
				s.Remove(handles[n])
			}
			if tc.Dispose {
				if err := s.Dispose(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			count := len(tc.Expected)
			if tc.Error != "" {
				count = 1
			}
			for i := 0; i < count; i++ {
				var result DispatchResult
				var callErr error
				if r.definitions[tc.Hook].Kind == Filter {
					result, callErr = e.ApplyFilters(context.Background(), tc.Hook, []byte(tc.Payload), nil)
				} else if r.definitions[tc.Hook].Mode == AfterCommit {
					pending, prepareErr := e.PrepareAfterCommit(context.Background(), tc.Hook, []byte(tc.Payload), nil)
					if prepareErr != nil {
						t.Fatal(prepareErr)
					}
					result, callErr = pending.Commit()
				} else {
					result, callErr = e.EmitAction(context.Background(), tc.Hook, []byte(tc.Payload), nil)
				}
				if tc.Error != "" {
					if classify(callErr) != tc.Error {
						t.Fatalf("error %v class %s", callErr, classify(callErr))
					}
					continue
				}
				if tc.Receipt != "" {
					if result.Status != tc.Receipt {
						t.Fatalf("receipt %s", result.Status)
					}
					result, callErr = result.Future.Await(context.Background())
				}
				want := tc.Expected[i]
				if result.Status != want.Status {
					t.Fatalf("status %s want %s error %v", result.Status, want.Status, callErr)
				}
				order := []string{}
				classes := []string{}
				for _, o := range result.Outcomes {
					order = append(order, o.Name)
					classes = append(classes, o.Class)
				}
				if !reflect.DeepEqual(order, want.Order) || !reflect.DeepEqual(classes, want.Classes) {
					t.Fatalf("outcomes %v/%v want %v/%v", order, classes, want.Order, want.Classes)
				}
				if want.Value == nil {
					if len(result.Value) != 0 {
						t.Fatalf("unexpected value %s", result.Value)
					}
				} else {
					sameJSON(t, string(result.Value), *want.Value)
				}
			}
			if tc.Seen != nil {
				mu.Lock()
				defer mu.Unlock()
				if len(seen) != len(tc.Seen) {
					t.Fatalf("seen %v", seen)
				}
				for i := range seen {
					sameJSON(t, seen[i], tc.Seen[i])
				}
			}
		})
	}
}

func TestSharedPolicyConformance(t *testing.T) {
	type policyCase struct {
		Name         string                     `json:"name"`
		Operation    string                     `json:"operation"`
		Error        string                     `json:"error"`
		Hooks        *[]string                  `json:"hooks"`
		Owner        *string                    `json:"owner"`
		Generation   *string                    `json:"generation"`
		Remote       bool                       `json:"remote"`
		Hook         string                     `json:"hook"`
		Registration *string                    `json:"registration"`
		Kind         Kind                       `json:"kind"`
		Patch        map[string]json.RawMessage `json:"patch"`
		Options      struct {
			TimeoutMS    *float64    `json:"timeoutMs"`
			View         string      `json:"view"`
			SchemaDigest string      `json:"schemaDigest"`
			OnError      ErrorPolicy `json:"onError"`
		} `json:"options"`
	}
	raw, err := os.ReadFile("conformance/execution.json")
	if err != nil {
		t.Fatal(err)
	}
	var base struct {
		Catalog CatalogDocument `json:"catalog"`
	}
	if decodeErr := json.Unmarshal(raw, &base); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	raw, err = os.ReadFile("conformance/policy.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []policyCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			cloned, _ := json.Marshal(base.Catalog)
			var doc CatalogDocument
			if err := json.Unmarshal(cloned, &doc); err != nil {
				t.Fatal(err)
			}
			if len(tc.Patch) > 0 {
				blob, _ := json.Marshal(doc.Definitions[0])
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(blob, &fields); err != nil {
					t.Fatal(err)
				}
				for key, value := range tc.Patch {
					fields[key] = value
				}
				blob, _ = json.Marshal(fields)
				if err := json.Unmarshal(blob, &doc.Definitions[0]); err != nil {
					t.Fatal(err)
				}
			}
			r, got := NewRegistry(corpusCatalog(doc))
			if got == nil && tc.Operation != "definition" {
				config := ScopeConfig{Owner: "example", Generation: "1", Hooks: []string{"event.observe"}}
				if tc.Owner != nil {
					config.Owner = *tc.Owner
				}
				if tc.Generation != nil {
					config.Generation = *tc.Generation
				}
				if tc.Hooks != nil {
					config.Hooks = *tc.Hooks
				}
				config.Remote = tc.Remote
				var scope *Scope
				scope, got = r.NewScope(config)
				if got == nil && tc.Operation != "scope" {
					if tc.Operation == "generation" {
						_, got = r.NewScope(config)
					} else {
						hook := "event.observe"
						if tc.Hook != "" {
							hook = tc.Hook
						}
						name := "handler"
						if tc.Registration != nil {
							name = *tc.Registration
						}
						kind := Action
						if tc.Kind != "" {
							kind = tc.Kind
						}
						options := Options{View: tc.Options.View, SchemaDigest: tc.Options.SchemaDigest, OnError: tc.Options.OnError}
						if tc.Options.TimeoutMS != nil {
							v := time.Duration(*tc.Options.TimeoutMS * float64(time.Millisecond))
							options.Timeout = &v
						}
						_, got = scope.ValidateRegistration(hook, name, kind, options)
						if got == nil && tc.Operation == "duplicate" {
							_, got = scope.AddAction(hook, name, options, func(context.Context, Invocation) error { return nil })
							if got == nil {
								_, got = scope.AddAction(hook, name, options, func(context.Context, Invocation) error { return nil })
							}
						}
					}
				}
			}
			expected := map[string]error{"unknown_hook": ErrUnknownHook, "unauthorized": ErrUnauthorized, "invalid_options": ErrInvalidOptions, "duplicate": ErrDuplicate, "invalid_definition": ErrInvalidDefinition}[tc.Error]
			if expected == nil || !errors.Is(got, expected) {
				t.Fatalf("error %v want %s", got, tc.Error)
			}
		})
	}
}
