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

// Encoding selects the byte convention of a native document.
// BootEncoding is the default; InstalledEncoding preserves the installed
// literal-string and explicit-parent-table convention for drift checks.
type Encoding string

const (
	BootEncoding      Encoding = ""
	InstalledEncoding Encoding = "installed"
)

// ConfigInput carries explicit native policy values. Empty values are absent;
// callers choose defaults before rendering, rather than inferring a posture.
type ConfigInput struct {
	Encoding                          Encoding
	Mode, ApprovalPolicy, SandboxMode string
	WritableRoots                     []string
	Servers                           []contract.Server
	Slots                             []contract.Slot
}

// Config encodes one TOML document; MCP belongs to Servers, never raw slots.
func Config(in ConfigInput) ([]byte, error) { doc, err := ConfigDocument(in); return doc.Bytes, err }

// ConfigDocument also returns declared leaf ownership for installed apply.
func ConfigDocument(in ConfigInput) (contract.Document, error) {
	ctx := contract.Context{Provider: "codex", Mode: in.Mode, Concern: "settings"}
	if in.Encoding != BootEncoding && in.Encoding != InstalledEncoding {
		return contract.Document{}, ctx.Refuse(contract.InvalidInput, "unknown native encoding")
	}
	if err := contract.ValidateDirectories(ctx, in.WritableRoots); err != nil {
		return contract.Document{}, err
	}
	switch in.ApprovalPolicy {
	case "", "on-failure", "on-request", "never":
	default:
		return contract.Document{}, ctx.Refuse(contract.InvalidInput, "unknown native approval policy")
	}
	switch in.SandboxMode {
	case "", "read-only", "workspace-write", "danger-full-access":
	default:
		return contract.Document{}, ctx.Refuse(contract.InvalidInput, "unknown native sandbox mode")
	}
	if err := contract.ValidateServers(contract.Context{Provider: ctx.Provider, Mode: ctx.Mode, Concern: "mcp"}, in.Servers); err != nil {
		return contract.Document{}, err
	}
	for _, slot := range in.Slots {
		if slot.Key == "mcp_servers" {
			return contract.Document{}, ctx.Refuse(contract.DuplicateKey, "MCP native slot belongs to typed server bindings")
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
		return contract.Document{}, err
	}
	if section, ok := doc["sandbox_workspace_write"].(map[string]any); ok {
		if grants, present := section["writable_roots"]; present {
			values, ok := grants.([]any)
			if !ok {
				return contract.Document{}, ctx.Refuse(contract.InvalidInput, "directory grants must be an array of strings")
			}
			dirs := make([]string, len(values))
			for i, v := range values {
				dir, ok := v.(string)
				if !ok {
					return contract.Document{}, ctx.Refuse(contract.InvalidInput, "directory grants must be strings")
				}
				dirs[i] = dir
			}
			if err := contract.ValidateDirectories(ctx, dirs); err != nil {
				return contract.Document{}, err
			}
		}
	}
	owned := contract.ObjectKeyPaths(doc)
	var out strings.Builder
	// Preserve the native policy header ordering used by the existing projection.
	for _, key := range []string{"approval_policy", "sandbox_mode"} {
		if v, ok := doc[key]; ok {
			text, ok := v.(string)
			if !ok {
				return contract.Document{}, ctx.Refuse(contract.InvalidInput, "native policy must be a string")
			}
			switch key {
			case "approval_policy":
				switch text {
				case "on-failure", "on-request", "never":
				default:
					return contract.Document{}, ctx.Refuse(contract.InvalidInput, "unknown native approval policy")
				}
			case "sandbox_mode":
				switch text {
				case "read-only", "workspace-write", "danger-full-access":
				default:
					return contract.Document{}, ctx.Refuse(contract.InvalidInput, "unknown native sandbox mode")
				}
			}
			if in.Encoding == BootEncoding {
				if err := assignment(ctx, &out, key, v, false, in.Encoding); err != nil {
					return contract.Document{}, err
				}
				delete(doc, key)
			}
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
	if in.Encoding == InstalledEncoding {
		orders = nil
	}
	if err := table(ctx, &out, nil, doc, orders, in.Encoding); err != nil {
		return contract.Document{}, err
	}
	return contract.Document{Bytes: []byte(out.String()), KeyPaths: owned}, nil
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
func table(ctx contract.Context, b *strings.Builder, path []string, doc map[string]any, orders map[string][]string, encoding Encoding) error {
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
		if _, nested := doc[k].(map[string]any); nested || encoding == InstalledEncoding && arrayOfObjects(doc[k]) {
			continue
		}
		if err := assignment(ctx, b, k, doc[k], len(path) > 0 && path[len(path)-1] == "env" && encoding != InstalledEncoding, encoding); err != nil {
			return err
		}
	}
	for _, k := range all {
		if encoding == InstalledEncoding && arrayOfObjects(doc[k]) {
			next := append(append([]string{}, path...), k)
			for i, item := range doc[k].([]any) {
				installedHeader(b, next, true, i > 0)
				if err := table(ctx, b, next, item.(map[string]any), orders, encoding); err != nil {
					return err
				}
			}
			continue
		}
		if sub, ok := doc[k].(map[string]any); ok {
			next := append(append([]string{}, path...), k)
			emit := len(sub) == 0 || encoding == InstalledEncoding
			for _, value := range sub {
				if _, nested := value.(map[string]any); !nested {
					emit = true
					break
				}
			}
			if emit {
				if encoding == InstalledEncoding {
					last := strings.TrimSuffix(b.String(), "\n")
					if i := strings.LastIndexByte(last, '\n'); i >= 0 {
						last = last[i+1:]
					}
					if b.Len() > 0 && !strings.HasPrefix(last, "[") {
						b.WriteByte('\n')
					}
					b.WriteByte('[')
				} else {
					b.WriteString("\n[")
				}
				for i, p := range next {
					if i > 0 {
						b.WriteByte('.')
					}
					b.WriteString(encodingKey(p, encoding))
				}
				b.WriteString("]\n")
			}
			if err := table(ctx, b, next, sub, orders, encoding); err != nil {
				return err
			}
		}
	}
	return nil
}
func assignment(ctx contract.Context, b *strings.Builder, k string, v any, quoteKey bool, encoding Encoding) error {
	text, err := value(ctx, v, encoding)
	if err != nil {
		return err
	}
	if quoteKey {
		b.WriteString(quoted(k))
	} else {
		b.WriteString(encodingKey(k, encoding))
	}
	b.WriteString(" = ")
	b.WriteString(text)
	b.WriteByte('\n')
	return nil
}
func value(ctx contract.Context, v any, encoding Encoding) (string, error) {
	switch x := v.(type) {
	case string:
		return encodingString(x, encoding), nil
	case bool:
		return strconv.FormatBool(x), nil
	case json.Number:
		text := x.String()
		var err error
		if strings.ContainsAny(text, ".eE") {
			var number float64
			number, err = strconv.ParseFloat(text, 64)
			if err == nil && encoding == InstalledEncoding {
				text = strconv.FormatFloat(number, 'f', -1, 64)
				if !strings.Contains(text, ".") {
					text += ".0"
				}
			}
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
			s, err := value(ctx, v, encoding)
			if err != nil {
				return "", err
			}
			parts[i] = s
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case map[string]any:
		parts := []string{}
		for _, k := range keys(x) {
			s, err := value(ctx, x[k], encoding)
			if err != nil {
				return "", err
			}
			parts = append(parts, encodingKey(k, encoding)+" = "+s)
		}
		return "{ " + strings.Join(parts, ", ") + " }", nil
	default:
		return "", ctx.Refuse(contract.InvalidInput, "native value cannot be represented in TOML")
	}
}

func encodingKey(s string, encoding Encoding) string {
	if encoding != InstalledEncoding {
		return key(s)
	}
	if key(s) == s {
		return s
	}
	return encodingString(s, encoding)
}
func encodingString(s string, encoding Encoding) string {
	if encoding != InstalledEncoding {
		return quoted(s)
	}
	literal := true
	for _, c := range []byte(s) {
		if c == '\'' || c == '\r' || c == '\n' || c < 0x20 && c != '\t' || c == 0x7f {
			literal = false
			break
		}
	}
	if literal {
		return "'" + s + "'"
	}
	var b strings.Builder
	b.WriteByte('"')
	const hex = "0123456789ABCDEF"
	for _, c := range []byte(s) {
		switch c {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 || c == 0x7f {
				b.WriteString(`\u00`)
				b.WriteByte(hex[c>>4])
				b.WriteByte(hex[c&15])
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func arrayOfObjects(v any) bool {
	items, ok := v.([]any)
	if !ok || len(items) == 0 {
		return false
	}
	for _, item := range items {
		if _, ok := item.(map[string]any); !ok {
			return false
		}
	}
	return true
}
func installedHeader(b *strings.Builder, parts []string, array, forceBlank bool) {
	last := strings.TrimSuffix(b.String(), "\n")
	if i := strings.LastIndexByte(last, '\n'); i >= 0 {
		last = last[i+1:]
	}
	if b.Len() > 0 && (forceBlank || !strings.HasPrefix(last, "[")) {
		b.WriteByte('\n')
	}
	b.WriteByte('[')
	if array {
		b.WriteByte('[')
	}
	for i, part := range parts {
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(encodingKey(part, InstalledEncoding))
	}
	b.WriteByte(']')
	if array {
		b.WriteByte(']')
	}
	b.WriteByte('\n')
}
