package redact

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path"
	"strings"
)

// --- JSON ------------------------------------------------------------------------

type pair struct {
	k string
	v any
}

type object []pair

// JSON redacts a JSON document. A value under a sensitive key is replaced whole
// (whatever its type, so a "credentials" object disappears entirely); every other
// string goes through [Redactor.Text]. Key order is kept, numbers are not
// converted, the output is valid JSON and redacting it again changes nothing. It
// is the right tool for JSON: [Redactor.Text] can only guess where a string ends.
func (r *Redactor) JSON(data []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := r.readValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("redact: trailing data after JSON value")
	}
	var out bytes.Buffer
	writeJSON(&out, v)
	return out.Bytes(), nil
}

// Marshal encodes v as JSON and redacts it. HTML characters are not escaped.
func (r *Redactor) Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return r.JSON(buf.Bytes())
}

// MarshalIndent is [Redactor.Marshal] with indentation.
func (r *Redactor) MarshalIndent(v any, prefix, indent string) ([]byte, error) {
	b, err := r.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, b, prefix, indent); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (r *Redactor) readValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			var obj object
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, _ := kt.(string)
				val, err := r.readValue(dec)
				if err != nil {
					return nil, err
				}
				obj = append(obj, pair{key, r.redactField(key, val)})
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			if obj == nil {
				obj = object{}
			}
			return obj, nil
		case '[':
			arr := []any{}
			for dec.More() {
				val, err := r.readValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, val)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return arr, nil
		}
	case string:
		return r.Text(t), nil
	}
	return tok, nil // number, bool, null
}

// redactField replaces the value of a sensitive key. Booleans and null carry no
// secret and stay; everything else, including a whole object, becomes the marker.
func (r *Redactor) redactField(key string, val any) any {
	if !r.policy.sensitive(key) {
		return val
	}
	switch val.(type) {
	case bool, nil:
		return val
	case string:
		if r.isPlaceholder(val.(string)) {
			return val
		}
	}
	// a plural/benign key was filtered by sensitive() already; here the key holds a secret
	return r.marker
}

func writeJSON(b *bytes.Buffer, v any) {
	switch x := v.(type) {
	case object:
		b.WriteByte('{')
		for i, p := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			writeString(b, p.k)
			b.WriteByte(':')
			writeJSON(b, p.v)
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSON(b, e)
		}
		b.WriteByte(']')
	case string:
		writeString(b, x)
	case json.Number:
		b.WriteString(x.String())
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case nil:
		b.WriteString("null")
	}
}

func writeString(b *bytes.Buffer, s string) {
	var sb bytes.Buffer
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	b.Write(bytes.TrimSuffix(sb.Bytes(), []byte("\n")))
}

// --- argv and environment --------------------------------------------------------

// Args redacts a command line given as words: the value of a secret-named flag
// (--token abc, --password=abc), the password of curl's -u user:pass, sshpass -p
// and mysql -pPASS, and any credential inside a word (a URL with userinfo). It
// returns a new slice. --max-tokens and --password-file are not secrets.
func (r *Redactor) Args(args []string) []string {
	out := make([]string, len(args))
	cmd := ""
	if len(args) > 0 {
		cmd = path.Base(args[0])
	}
	const (
		none = iota
		wholeNext
		userPassNext
	)
	next := none
	for i, a := range args {
		switch next {
		case wholeNext:
			out[i], next = r.marker, none
			continue
		case userPassNext:
			next = none
			if user, _, ok := strings.Cut(a, ":"); ok {
				out[i] = user + ":" + r.marker
				continue
			}
		}
		out[i] = r.Text(a)
		switch {
		case strings.HasPrefix(a, "-p") && len(a) > 2 && strings.HasPrefix(cmd, "mysql") && !strings.HasPrefix(a, "--"):
			out[i] = "-p" + r.marker
		case strings.HasPrefix(a, "-") && !strings.Contains(a, "="):
			name := strings.TrimLeft(a, "-")
			hasNext := i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") && !r.isPlaceholder(args[i+1])
			switch {
			case hasNext && (cmd == "curl" || cmd == "wget") && (a == "-u" || a == "--user" || a == "--proxy-user"):
				next = userPassNext
			case hasNext && (r.policy.sensitive(name) || (cmd == "sshpass" && a == "-p")):
				next = wholeNext
			}
		}
	}
	return out
}

// Env redacts a process environment ("NAME=value" strings): the value of every
// secret-named variable, and credentials inside other values. It returns a new slice.
func (r *Redactor) Env(environ []string) []string {
	out := make([]string, len(environ))
	for i, kv := range environ {
		name, val, ok := strings.Cut(kv, "=")
		switch {
		case !ok:
			out[i] = r.Text(kv)
		case r.policy.sensitive(name) && !r.isPlaceholder(val):
			out[i] = name + "=" + r.marker
		default:
			out[i] = name + "=" + r.Text(val)
		}
	}
	return out
}
