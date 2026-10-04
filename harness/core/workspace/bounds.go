package workspace

import (
	"github.com/hollis-labs/substrate/harness/workspace/render"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	MaxCollectionItems   = render.MaxTreeEntries
	MaxFrozenBytes       = 64 << 20
	MaxObservationWindow = 5 * time.Minute
)

// validateFrozenValues runs before JSON copying. Binary artifact bytes remain
// binary; every textual field and map key must survive JSON without replacement.
func validateFrozenValues(values ...any) error {
	items, bytes := 0, 0
	var walk func(reflect.Value) error
	walk = func(v reflect.Value) error {
		if !v.IsValid() {
			return nil
		}
		switch v.Kind() {
		case reflect.String:
			s := v.String()
			bytes += len(s)
			if bytes > MaxFrozenBytes {
				return refuse(CodeInputLimit, "inputs", Conflict)
			}
			if !utf8.ValidString(s) {
				return refuse(CodeInvalidUtf8, "inputs", Conflict)
			}
		case reflect.Pointer, reflect.Interface:
			if !v.IsNil() {
				return walk(v.Elem())
			}
		case reflect.Slice, reflect.Array:
			if v.Type().Elem().Kind() == reflect.Uint8 {
				bytes += v.Len()
				if bytes > MaxFrozenBytes {
					return refuse(CodeInputLimit, "inputs", Conflict)
				}
				return nil
			}
			if v.Len() > MaxCollectionItems {
				return refuse(CodeInputLimit, "inputs", Conflict)
			}
			items += v.Len()
			if items > MaxCollectionItems*16 {
				return refuse(CodeInputLimit, "inputs", Conflict)
			}
			for i := 0; i < v.Len(); i++ {
				if err := walk(v.Index(i)); err != nil {
					return err
				}
			}
		case reflect.Map:
			if v.Len() > MaxCollectionItems {
				return refuse(CodeInputLimit, "inputs", Conflict)
			}
			items += v.Len()
			if items > MaxCollectionItems*16 {
				return refuse(CodeInputLimit, "inputs", Conflict)
			}
			iter := v.MapRange()
			for iter.Next() {
				if err := walk(iter.Key()); err != nil {
					return err
				}
				if err := walk(iter.Value()); err != nil {
					return err
				}
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				if v.Type().Field(i).PkgPath == "" {
					if err := walk(v.Field(i)); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	for _, value := range values {
		if err := walk(reflect.ValueOf(value)); err != nil {
			return err
		}
	}
	return nil
}
func foldText(s string) string {
	return strings.Map(func(r rune) rune {
		least := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < least {
				least = next
			}
		}
		return least
	}, s)
}
