package subprocess

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestDuplexSharedCoreVectors(t *testing.T) {
	for _, name := range []string{"duplex-correlation", "duplex-invalid"} {
		raw, err := os.ReadFile("../protocol/v2/fixtures/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		// Wire member names use underscore spelling; preserve all raw token values.
		var document struct{ Cases []json.RawMessage }
		if json.Unmarshal(raw, &document) != nil {
			t.Fatal("corpus")
		}
		for _, item := range document.Cases {
			var v struct {
				Name                   string
				Directional, Immediate bool
				Operations             []json.RawMessage
			}
			if json.Unmarshal(item, &v) != nil {
				t.Fatal("case")
			}
			t.Run(v.Name, func(t *testing.T) {
				c := newFixtureCorrelation(v.Directional)
				defer c.close(nil)
				s := server{}
				c.encode = s.encodeFrame
				var frames []json.RawMessage
				calls := map[int64]<-chan callResult{}
				c.publish = func(frame []byte, receipt func(error)) error {
					frames = append(frames, frame)
					if v.Immediate {
						req, _ := decodeEnvelope(frame)
						b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]bool{"accepted": true}})
						if err := c.reply(b); err != nil {
							t.Fatal(err)
						}
					}
					receipt(nil)
					return nil
				}
				for _, operation := range v.Operations {
					var op struct {
						Op, Method, Raw string
						Params          string `json:"params_raw"`
						ID              RPCID
						Fault           bool
						Recover         int
						Result          json.RawMessage
						Code            int
					}
					if json.Unmarshal(operation, &op) != nil {
						t.Fatal("operation")
					}
					var failure error
					switch op.Op {
					case "admit":
						failure = c.admit(op.ID)
					case "release":
						c.release(op.ID)
					case "assert_inbound":
						if _, ok := c.incoming[op.ID]; !ok {
							t.Fatal("opposite ID released")
						}
					case "call":
						ch, err := c.call(op.Method, json.RawMessage(op.Params))
						failure = err
						calls[c.next] = ch
					case "expect_frame":
						if len(frames) == 0 {
							t.Fatal("missing frame")
						}
						req, fault := decodeEnvelope(frames[0])
						frames = frames[1:]
						if fault != nil || req.ID != op.ID || req.Method != "host/log" {
							t.Fatal("frame correlation")
						}
					case "receive":
						req, fault := decodeEnvelope([]byte(op.Raw))
						if fault != nil {
							if op.Recover != 0 {
								if fault.Error.Code != op.Recover {
									t.Fatal(fault)
								}
								continue
							}
							if c.directional && (!json.Valid([]byte(op.Raw)) || replyCandidate([]byte(op.Raw))) {
								failure = errCorrelation
							} else {
								t.Fatal("unexpected recoverable fault")
							}
						} else if req == nil {
							if c.directional {
								failure = c.reply([]byte(op.Raw))
							}
						} else {
							failure = c.admit(req.ID)
						}
					case "expect_result", "expect_error":
						n, _ := op.ID.Integer()
						select {
						case result := <-calls[n]:
							if op.Op == "expect_error" {
								if result.err == nil {
									t.Fatal("missing error")
								}
								if op.Code != 0 {
									var fault *RPCError
									if !errors.As(result.err, &fault) || fault.Code != op.Code {
										t.Fatal(result.err)
									}
								}
							} else {
								var actual, expected any
								if json.Unmarshal(result.result, &actual) != nil || json.Unmarshal(op.Result, &expected) != nil || !reflect.DeepEqual(actual, expected) || result.err != nil {
									t.Fatalf("result=%+v", result)
								}
							}
						default:
							t.Fatal("pending completion missing")
						}
					case "close":
						c.close(nil)
					default:
						t.Fatal(op.Op)
					}
					if (failure != nil) != op.Fault {
						t.Fatalf("%s fault=%v", op.Op, failure)
					}
					if failure != nil {
						c.close(failure)
					}
				}
			})
		}
	}
}
func TestCorrelationPublicationFailureAndExhaustion(t *testing.T) {
	params := json.RawMessage(`{"grant_id":"g","context":{"binding_id":"b","timeout_ms":10000,"parent_call":{"request_owner":"host","id":1}},"level":"info","message":"ready"}`)
	for _, encodeFail := range []bool{false, true} {
		c := newFixtureCorrelation(true)
		s := server{}
		c.encode = s.encodeFrame
		if encodeFail {
			c.encode = func(any) ([]byte, error) { return nil, errCorrelation }
		}
		c.publish = func([]byte, func(error)) error { return errConnectionClosed }
		ch, err := c.call("host/log", params)
		if err != nil {
			t.Fatal(err)
		}
		if result := <-ch; result.err == nil {
			t.Fatal("missing failure")
		}
		if len(c.pending) != 0 || c.next != 1 {
			t.Fatal("failed registration leaked or ID reused")
		}
		c.next = maxRPCInteger
		if _, err := c.call("host/log", params); err == nil {
			t.Fatal("ID exhaustion wrapped")
		}
	}
}

func TestReplyCloseRaceCompletesOnce(t *testing.T) {
	c := newFixtureCorrelation(true)
	s := server{}
	c.encode = s.encodeFrame
	c.publish = func([]byte, func(error)) error { return nil }
	ch, err := c.call("host/log", json.RawMessage(duplexLogParams))
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		_ = c.reply([]byte(`{"jsonrpc":"2.0","id":1,"result":{"accepted":true}}`))
	}()
	go func() { defer workers.Done(); <-start; c.close(errConnectionClosed) }()
	close(start)
	workers.Wait()
	<-ch
	select {
	case <-ch:
		t.Fatal("completed twice")
	default:
	}
	if len(c.pending) != 0 {
		t.Fatal("pending entry leaked")
	}
}
func TestPublicationFailureFailsOtherPending(t *testing.T) {
	c := newFixtureCorrelation(true)
	s := server{}
	c.encode = s.encodeFrame
	output := receiptTestWriter{make(chan struct{}), make(chan struct{})}
	defer close(output.release)
	writer := newFrameWriter(output, 10*time.Millisecond, func() {})
	writer.onFailure = func(err error) { c.close(err) }
	c.publish = writer.submit
	first, err := c.call("host/log", json.RawMessage(duplexLogParams))
	if err != nil {
		t.Fatal(err)
	}
	<-output.started
	second, err := c.call("host/log", json.RawMessage(duplexLogParams))
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range []<-chan callResult{first, second} {
		if result := <-ch; !errors.Is(result.err, ErrWriteTimeout) {
			t.Fatal(result.err)
		}
	}
	<-writer.done
}

// Fixture-only offer values keep engine recipes independent of host policy.
func newFixtureCorrelation(directional bool) *correlation {
	c := newCorrelation(directional)
	c.methodTimeoutMS = map[string]uint32{}
	for method := range hostMethods {
		c.methodTimeoutMS[method] = 10000
	}
	return c
}
