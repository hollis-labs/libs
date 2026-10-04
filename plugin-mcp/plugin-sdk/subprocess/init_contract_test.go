package subprocess

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestSharedInitFixtures(t *testing.T) {
	b, err := os.ReadFile("../protocol/v2/fixtures/init.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name, Kind, Input, Code string
		Valid                   bool
	}
	if json.Unmarshal(b, &fixtures) != nil {
		t.Fatal("fixtures")
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			var target any
			if f.Kind == "params" {
				target = &InitParams{}
			} else {
				target = &InitResult{}
			}
			err := json.Unmarshal([]byte(f.Input), target)
			if (err == nil) != f.Valid {
				t.Fatalf("valid=%v error=%v", f.Valid, err)
			}
			if !f.Valid {
				var typed *InitError
				if !errors.As(err, &typed) || f.Code != "" && string(typed.Code) != f.Code {
					t.Fatalf("typed failure=%v", err)
				}
			}
		})
	}
}
func TestInitResultProfileAgreement(t *testing.T) {
	p := validInitParams()
	v := 1
	r := InitResult{ID: "test", Name: "Test", Version: "1", Protocol: 2, CapabilityContract: 1, ReverseRPCVersion: &v}
	var typed *InitError
	if !errors.As(ValidateInitResult(p, r), &typed) || typed.Code != InitProfileMismatch {
		t.Fatal("unsolicited profile")
	}
	r.ReverseRPCVersion = nil
	p.HooksProfile = &HooksProfile{HooksProfileVersion: 1}
	if err := ValidateInitResult(p, r); err != nil {
		t.Fatal(err)
	}
}

type initCountingPlugin struct {
	basePlugin
	calls int
}

func (p *initCountingPlugin) Init(ctx context.Context, in InitParams) (InitResult, error) {
	p.calls++
	return p.basePlugin.Init(ctx, in)
}
func TestServeInitAdmission(t *testing.T) {
	cases := []struct {
		name, raw string
		codes     []int
		calls     int
	}{
		{"before-init", `{"jsonrpc":"2.0","id":1,"method":"plugin/load"}` + "\n", []int{-32600}, 0},
		{"malformed", `{"jsonrpc":"2.0","id":1,"method":"plugin/init","params":{}}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"plugin/load"}` + "\n", []int{-32602, -32600}, 0},
		{"repeat", initLine(t, 1) + initLine(t, 2), []int{0, -32600}, 1},
		{"invalid-version", strings.Replace(initLine(t, 1), `"protocol":2`, `"protocol":1`, 1), []int{-32602}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &initCountingPlugin{}
			var out bytes.Buffer
			if err := serveWith(p, strings.NewReader(c.raw), &out); err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			if len(lines) != len(c.codes) {
				t.Fatalf("replies %s", out.String())
			}
			for _, line := range lines {
				var r RPCResponse
				if json.Unmarshal([]byte(line), &r) != nil {
					t.Fatal(line)
				}
				code := 0
				if r.Error != nil {
					code = r.Error.Code
				}
				n, _ := r.ID.Integer()
				if code != c.codes[n-1] {
					t.Fatalf("reply=%s", line)
				}
			}
			if p.calls != c.calls {
				t.Fatalf("init calls=%d", p.calls)
			}
		})
	}
}
func initLine(t *testing.T, id int64) string {
	t.Helper()
	b, err := json.Marshal(RPCRequest{JSONRPC: "2.0", ID: NumberID(id), Method: MethodInit, Params: validInitParams()})
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

type barrierInitPlugin struct {
	basePlugin
	started, release chan struct{}
	handled          chan struct{}
}

func (p *barrierInitPlugin) Init(ctx context.Context, in InitParams) (InitResult, error) {
	close(p.started)
	select {
	case <-p.release:
	case <-ctx.Done():
		return InitResult{}, ctx.Err()
	}
	res, err := p.basePlugin.Init(ctx, in)
	v := 1
	res.HooksProfileVersion = &v
	res.ReverseRPCVersion = &v
	return res, err
}
func (p *barrierInitPlugin) Load(context.Context) (LoadResult, error) {
	close(p.handled)
	return LoadResult{}, nil
}
func TestServeInitBarrierAndProfileDecline(t *testing.T) {
	p := &barrierInitPlugin{started: make(chan struct{}), release: make(chan struct{}), handled: make(chan struct{})}
	in, inW := io.Pipe()
	out, outW := io.Pipe()
	defer in.Close()
	defer inW.Close()
	defer out.Close()
	defer outW.Close()
	done := make(chan error, 1)
	go func() { done <- serveWith(p, in, outW) }()
	params := validInitParams()
	params.HooksProfile = &HooksProfile{HooksProfileVersion: 1}
	b, err := json.Marshal(RPCRequest{JSONRPC: "2.0", ID: NumberID(1), Method: MethodInit, Params: params})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = inW.Write(append(b, '\n')) }()
	<-p.started
	go func() { _, _ = io.WriteString(inW, `{"jsonrpc":"2.0","id":2,"method":"plugin/load"}`+"\n") }()
	decoder := json.NewDecoder(out)
	var response RPCResponse
	response = RPCResponse{}
	if err := decoder.Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.ID != NumberID(2) || response.Error == nil || response.Error.Code != -32600 {
		t.Fatalf("refusal=%+v", response)
	}
	select {
	case <-p.handled:
		t.Fatal("load invoked during Init")
	default:
	}
	close(p.release)
	response = RPCResponse{}
	if err := decoder.Decode(&response); err != nil {
		t.Fatal(err)
	}
	var result InitResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.HooksProfileVersion != nil || result.ReverseRPCVersion != nil {
		t.Fatal("unimplemented profiles advertised")
	}
	go func() { _, _ = io.WriteString(inW, `{"jsonrpc":"2.0","id":3,"method":"plugin/load"}`+"\n") }()
	response = RPCResponse{}
	if err := decoder.Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	<-p.handled
	_ = inW.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
