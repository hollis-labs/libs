// cairn consumes resolved boot inputs. It never loads a definition or creates
// host authority. Process launch and native authority binding belong to callers.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/hollis-labs/substrate/harness/boot"
)

const maxResolvedBytes = 32 << 20

type resolvedRequest struct {
	SchemaVersion string            `json:"schema_version"`
	Input         boot.Input        `json:"input"`
	Host          boot.HostInputDTO `json:"host"`
}

type errorDescription struct {
	Phase   boot.Phase `json:"phase"`
	Code    string     `json:"code"`
	Message string     `json:"message"`
}

type response struct {
	SchemaVersion     string            `json:"schema_version"`
	Result            boot.Result       `json:"result"`
	ArtifactsComplete bool              `json:"artifacts_complete"`
	Error             *errorDescription `json:"error,omitempty"`
}

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	output := response{SchemaVersion: boot.SchemaVersion}
	finish := func(err error, exit int) int {
		if err != nil {
			var be *boot.Error
			if errors.As(err, &be) {
				output.Error = &errorDescription{be.Phase, be.Code, be.Error()}
			} else {
				output.Error = &errorDescription{boot.PhasePlan, "cli_input", err.Error()}
			}
		}
		output.ArtifactsComplete = output.Result.ArtifactsComplete()
		if err := json.NewEncoder(stdout).Encode(output); err != nil {
			fmt.Fprintln(stderr, "cairn: encode result:", err)
			return 1
		}
		return exit
	}
	if len(args) == 0 || args[0] != "boot" {
		return finish(errors.New("usage: cairn boot --resolved FILE|- [--plan]"), 2)
	}
	flags := flag.NewFlagSet("boot", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("resolved", "", "resolved Input and HostInputDTO JSON (use - for stdin)")
	planOnly := flags.Bool("plan", false, "describe a pure plan without materialization")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return finish(err, 2)
	}
	if *path == "" || flags.NArg() != 0 {
		return finish(errors.New("--resolved is required; positional arguments are unsupported"), 2)
	}
	reader := stdin
	if *path != "-" {
		file, err := os.Open(*path)
		if err != nil {
			return finish(err, 2)
		}
		defer file.Close()
		reader = file
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxResolvedBytes+1))
	if err != nil {
		return finish(err, 2)
	}
	if len(data) > maxResolvedBytes {
		return finish(errors.New("resolved input exceeds 32 MiB"), 2)
	}
	if err := validateJSONText(data); err != nil {
		return finish(err, 2)
	}
	if err := rejectDuplicateKeys(json.NewDecoder(bytes.NewReader(data))); err != nil {
		return finish(err, 2)
	}
	if err := validateFieldNames(data, reflect.TypeOf(resolvedRequest{})); err != nil {
		return finish(err, 2)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var request resolvedRequest
	if err := decoder.Decode(&request); err != nil {
		return finish(err, 2)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return finish(errors.New("resolved input must contain exactly one JSON value"), 2)
	}
	if request.SchemaVersion != boot.SchemaVersion {
		return finish(&boot.Error{Phase: boot.PhasePlan, Code: "unsupported_schema", Err: errors.New("unsupported resolved envelope schema")}, 2)
	}
	host := boot.HostInputs{HostInputDTO: request.Host}
	if *planOnly {
		planned, err := boot.Plan(request.Input, host)
		output.Result.Description = planned.Description()
		if err != nil {
			return finish(err, 1)
		}
		return finish(nil, 0)
	}
	// Decoded data has no host ports. The real library path returns its typed
	// refusal and complete accounting; no local/ambient authority is created.
	output.Result, err = boot.Prepare(ctx, request.Input, host)
	if err != nil {
		return finish(err, 1)
	}
	return finish(nil, 0)
}

// encoding/json replaces malformed UTF-8 and unpaired UTF-16 escapes. Resolved
// intent must refuse those inputs instead of silently rewriting their contents.
func validateJSONText(data []byte) error {
	if !utf8.Valid(data) {
		return errors.New("resolved input contains invalid UTF-8")
	}
	quoted := false
	for i := 0; i < len(data); i++ {
		if data[i] == '"' {
			quoted = !quoted
			continue
		}
		if !quoted || data[i] != '\\' {
			continue
		}
		i++
		if i >= len(data) {
			break // The JSON decoder diagnoses incomplete syntax.
		}
		if data[i] != 'u' || i+4 >= len(data) {
			continue
		}
		value, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		if err != nil {
			continue
		}
		i += 4
		if value >= 0xdc00 && value <= 0xdfff {
			return errors.New("resolved input contains an unpaired Unicode surrogate")
		}
		if value < 0xd800 || value > 0xdbff {
			continue
		}
		if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
			return errors.New("resolved input contains an unpaired Unicode surrogate")
		}
		low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return errors.New("resolved input contains an unpaired Unicode surrogate")
		}
		i += 6
	}
	return nil
}

// Duplicate keys would silently discard supplied intent during decoding.
func rejectDuplicateKeys(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, container := token.(json.Delim)
	if !container {
		return nil
	}
	seen := map[string]bool{}
	for decoder.More() {
		if delim == '{' {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("resolved input contains a duplicate or invalid object key")
			}
			seen[name] = true
		}
		if err := rejectDuplicateKeys(decoder); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

// The JSON decoder accepts case-insensitive struct field aliases. Resolved
// intent requires the declared spelling; map keys are data and retain their
// case sensitivity. Custom JSON values keep their own decoding contract.
func validateFieldNames(data json.RawMessage, typ reflect.Type) error {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil
	}
	unmarshaler := reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()
	if typ.Implements(unmarshaler) || reflect.PointerTo(typ).Implements(unmarshaler) {
		return nil
	}
	switch typ.Kind() {
	case reflect.Struct:
		var values map[string]json.RawMessage
		if err := json.Unmarshal(data, &values); err != nil {
			return err
		}
		fields := schemaFields(typ)
		for name, value := range values {
			field, exact := fields[name]
			if !exact {
				for canonical := range fields {
					if strings.EqualFold(name, canonical) {
						return fmt.Errorf("struct field %q must use declared spelling %q", name, canonical)
					}
				}
				continue // DisallowUnknownFields supplies the unknown-field error.
			}
			if err := validateFieldNames(value, field.typ); err != nil {
				return err
			}
		}
	case reflect.Map:
		var values map[string]json.RawMessage
		if err := json.Unmarshal(data, &values); err != nil {
			return err
		}
		for _, value := range values {
			if err := validateFieldNames(value, typ.Elem()); err != nil {
				return err
			}
		}
	case reflect.Array, reflect.Slice:
		if typ.Elem().Kind() == reflect.Uint8 {
			return nil // Encoded byte content is not a struct-field namespace.
		}
		var values []json.RawMessage
		if err := json.Unmarshal(data, &values); err != nil {
			return err
		}
		for _, value := range values {
			if err := validateFieldNames(value, typ.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

type schemaField struct {
	typ       reflect.Type
	depth     int
	tagged    bool
	ambiguous bool
}

// Embedded fields follow JSON's shallowest-field and tagged-field precedence.
// An ambiguous name stays unknown so the strict decoder can refuse it.
func schemaFields(typ reflect.Type) map[string]schemaField {
	fields := map[string]schemaField{}
	var visit func(reflect.Type, int, map[reflect.Type]bool)
	visit = func(typ reflect.Type, depth int, path map[reflect.Type]bool) {
		if path[typ] {
			return
		}
		path[typ] = true
		defer delete(path, typ)
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.PkgPath != "" && !field.Anonymous {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			underlying := field.Type
			for underlying.Kind() == reflect.Pointer {
				underlying = underlying.Elem()
			}
			if field.Anonymous && name == "" && underlying.Kind() == reflect.Struct {
				visit(underlying, depth+1, path)
				continue
			}
			if field.PkgPath != "" {
				continue
			}
			tagged := name != ""
			if name == "" {
				name = field.Name
			}
			candidate := schemaField{typ: field.Type, depth: depth, tagged: tagged}
			prior, exists := fields[name]
			if !exists || depth < prior.depth || depth == prior.depth && tagged && !prior.tagged {
				fields[name] = candidate
			} else if depth == prior.depth && tagged == prior.tagged {
				prior.ambiguous = true
				fields[name] = prior
			}
		}
	}
	visit(typ, 0, map[reflect.Type]bool{})
	for name, field := range fields {
		if field.ambiguous {
			delete(fields, name)
		}
	}
	return fields
}
