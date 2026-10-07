package strictjson

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSharedTokenFixtures(t *testing.T) {
	data, err := os.ReadFile("../../protocol/v2/fixtures/strict-json.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name   string
		Input  string
		Valid  bool
		Fields []string
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			var err error
			if f.Fields != nil {
				_, err = Object([]byte(f.Input), f.Fields...)
			} else {
				err = Validate([]byte(f.Input))
			}
			if (err == nil) != f.Valid {
				t.Fatalf("valid=%v, err=%v", f.Valid, err)
			}
		})
	}
}

func TestInvalidUTF8(t *testing.T) {
	if Validate([]byte{'"', 0xff, '"'}) == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
}
