// Package nativefiles encodes Codex native documents from resolved inputs.
// Config has one owner, including its MCP tables. It performs no I/O and
// never renders authentication files or reads ambient configuration.
package nativefiles

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/hollis-labs/substrate/harness/adapters/internal/owner"
	contract "github.com/hollis-labs/substrate/harness/adapters/nativefiles"
)

var serializerOwner = owner.New("codex")

// Owner returns the immutable serializer handle; it cannot mint another owner.
func Owner() contract.Owner { return serializerOwner }

// Instructions copies the already composed instruction body without adding bindings.
func Instructions(body []byte) []byte { return bytes.Clone(body) }

// ConfigInput carries explicit native policy values. Empty values are absent;
// callers choose defaults before rendering, rather than inferring a posture.
type ConfigInput struct {
	Mode, ApprovalPolicy, SandboxMode string
	WritableRoots                     []string
	Servers                           []contract.Server
	Slots                             []contract.Slot
}

// Config encodes one TOML document; MCP belongs to Servers, never raw slots.
func Config(in ConfigInput) ([]byte, error) {
	ctx := contract.Context{Provider: "codex", Mode: in.Mode, Concern: "settings"}
	if err := contract.ValidateDirectories(ctx, in.WritableRoots); err != nil {
		return nil, err
	}
	switch in.ApprovalPolicy {
	case "", "on-failure", "on-request", "never":
	default:
		return nil, ctx.Refuse(contract.InvalidInput, "unknown native approval policy")
	}
	switch in.SandboxMode {
	case "", "read-only", "workspace-write", "danger-full-access":
	default:
		return nil, ctx.Refuse(contract.InvalidInput, "unknown native sandbox mode")
	}
	if err := contract.ValidateServers(contract.Context{Provider: ctx.Provider, Mode: ctx.Mode, Concern: "mcp"}, in.Servers); err != nil {
		return nil, err
	}
	for _, slot := range in.Slots {
		if slot.Key == "mcp_servers" {
			return nil, ctx.Refuse(contract.DuplicateKey, "MCP native slot belongs to typed server bindings")
		}
	}
	generated := []contract.Slot{}
	if in.ApprovalPolicy != "" {
		generated = append(generated, contract.Slot{Key: "approval_policy", Value: in.ApprovalPolicy})
	}
	if in.SandboxMode != "" {
		generated = append(generated, contract.Slot{Key: "sandbox_mode", Value: in.SandboxMode})
	}
	if len(in.WritableRoots) > 0 {
		generated = append(generated, contract.Slot{Key: "sandbox_workspace_write", Value: map[string]any{"writable_roots": in.WritableRoots}})
	}
	servers := map[string]any{}
	for _, s := range in.Servers {
		entry := map[string]any{}
		if s.HTTPURL != "" {
			entry["url"] = s.HTTPURL
		} else {
			entry["command"] = s.Command
			args := s.Args
			if args == nil {
				args = []string{}
			}
			entry["args"] = args
			if len(s.Env) > 0 {
				env := map[string]string{}
				for _, v := range s.Env {
					env[v.Name] = v.Value
				}
				entry["env"] = env
			}
		}
		servers[s.Name] = entry
	}
	if len(servers) > 0 {
		generated = append(generated, contract.Slot{Key: "mcp_servers", Value: servers})
	}
	doc, err := contract.Object(ctx, in.Slots, generated)
	if err != nil {
		return nil, err
	}
	if section, ok := doc["sandbox_workspace_write"].(map[string]any); ok {
		if grants, present := section["writable_roots"]; present {
			values, ok := grants.([]any)
			if !ok {
				return nil, ctx.Refuse(contract.InvalidInput, "directory grants must be an array of strings")
			}
			dirs := make([]string, len(values))
			for i, v := range values {
				dir, ok := v.(string)
				if !ok {
					return nil, ctx.Refuse(contract.InvalidInput, "directory grants must be strings")
				}
				dirs[i] = dir
			}
			if err := contract.ValidateDirectories(ctx, dirs); err != nil {
				return nil, err
			}
		}
	}
	var out strings.Builder
	// Preserve the native policy header ordering used by the existing projection.
	for _, key := range []string{"approval_policy", "sandbox_mode"} {
		if v, ok := doc[key]; ok {
			text, ok := v.(string)
			if !ok {
				return nil, ctx.Refuse(contract.InvalidInput, "native policy must be a string")
			}
			switch key {
			case "approval_policy":
				switch text {
				case "on-failure", "on-request", "never":
				default:
					return nil, ctx.Refuse(contract.InvalidInput, "unknown native approval policy")
				}
			case "sandbox_mode":
				switch text {
				case "read-only", "workspace-write", "danger-full-access":
				default:
					return nil, ctx.Refuse(contract.InvalidInput, "unknown native sandbox mode")
				}
			}
			if err := assignment(ctx, &out, key, v, false); err != nil {
				return nil, err
			}
			delete(doc, key)
		}
	}
	orders := map[string][]string{"": {"sandbox_workspace_write", "mcp_servers"}}
	for _, server := range in.Servers {
		orders["mcp_servers"] = append(orders["mcp_servers"], server.Name)
		orders["mcp_servers\x00"+server.Name] = []string{"url", "command", "args"}
		for _, variable := range server.Env {
			orders["mcp_servers\x00"+server.Name+"\x00env"] = append(orders["mcp_servers\x00"+server.Name+"\x00env"], variable.Name)
		}
	}
	if err := table(ctx, &out, nil, doc, orders); err != nil {
		return nil, err
	}
	return []byte(out.String()), nil
}
func keys(doc map[string]any) []string {
	out := make([]string, 0, len(doc))
	for k := range doc {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func key(s string) string {
	if s != "" {
		bare := true
		for _, c := range s {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
				bare = false
				break
			}
		}
		if bare {
			return s
		}
	}
	return quoted(s)
}
func quoted(s string) string {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(s)
	return strings.ReplaceAll(strings.TrimSuffix(b.String(), "\n"), string(rune(0x7f)), `\u007f`)
}
func table(ctx contract.Context, b *strings.Builder, path []string, doc map[string]any, orders map[string][]string) error {
	all := []string{}
	seen := map[string]bool{}
	for _, key := range orders[strings.Join(path, "\x00")] {
		if _, ok := doc[key]; ok {
			all = append(all, key)
			seen[key] = true
		}
	}
	for _, key := range keys(doc) {
		if !seen[key] {
			all = append(all, key)
		}
	}
	for _, k := range all {
		if _, nested := doc[k].(map[string]any); nested {
			continue
		}
		if err := assignment(ctx, b, k, doc[k], len(path) > 0 && path[len(path)-1] == "env"); err != nil {
			return err
		}
	}
	for _, k := range all {
		if sub, ok := doc[k].(map[string]any); ok {
			next := append(append([]string{}, path...), k)
			emit := len(sub) == 0
			for _, value := range sub {
				if _, nested := value.(map[string]any); !nested {
					emit = true
					break
				}
			}
			if emit {
				b.WriteString("\n[")
				for i, p := range next {
					if i > 0 {
						b.WriteByte('.')
					}
					b.WriteString(key(p))
				}
				b.WriteString("]\n")
			}
			if err := table(ctx, b, next, sub, orders); err != nil {
				return err
			}
		}
	}
	return nil
}
func assignment(ctx contract.Context, b *strings.Builder, k string, v any, quoteKey bool) error {
	text, err := value(ctx, v)
	if err != nil {
		return err
	}
	if quoteKey {
		b.WriteString(quoted(k))
	} else {
		b.WriteString(key(k))
	}
	b.WriteString(" = ")
	b.WriteString(text)
	b.WriteByte('\n')
	return nil
}
func value(ctx contract.Context, v any) (string, error) {
	switch x := v.(type) {
	case string:
		return quoted(x), nil
	case bool:
		return strconv.FormatBool(x), nil
	case json.Number:
		text := x.String()
		var err error
		if strings.ContainsAny(text, ".eE") {
			_, err = strconv.ParseFloat(text, 64)
		} else {
			_, err = strconv.ParseInt(text, 10, 64)
		}
		if err != nil {
			return "", ctx.Refuse(contract.InvalidInput, "invalid TOML number")
		}
		return text, nil
	case []any:
		parts := make([]string, len(x))
		for i, v := range x {
			s, err := value(ctx, v)
			if err != nil {
				return "", err
			}
			parts[i] = s
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case map[string]any:
		parts := []string{}
		for _, k := range keys(x) {
			s, err := value(ctx, x[k])
			if err != nil {
				return "", err
			}
			parts = append(parts, key(k)+" = "+s)
		}
		return "{ " + strings.Join(parts, ", ") + " }", nil
	default:
		return "", ctx.Refuse(contract.InvalidInput, "native value cannot be represented in TOML")
	}
}
