package subprocess

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSharedControlDTOs(t *testing.T) {
	raw, err := os.ReadFile("../protocol/v2/fixtures/duplex-control.json")
	if err != nil {
		t.Fatal(err)
	}
	var corpus struct {
		RawDTOs []struct {
			Name, DTO, Raw     string
			Valid, Directional bool
		} `json:"raw_dtos"`
	}
	if err = json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	for _, v := range corpus.RawDTOs {
		t.Run(v.Name, func(t *testing.T) {
			err := ValidateRPCControlDTO(v.DTO, []byte(v.Raw), v.Directional)
			if (err == nil) != v.Valid {
				t.Fatalf("valid=%v err=%v", v.Valid, err)
			}
			if v.Valid {
				var encoded []byte
				if v.DTO == "CancelParams" {
					var p CancelParams
					_ = json.Unmarshal([]byte(v.Raw), &p)
					encoded, err = json.Marshal(p)
				} else {
					var p PluginRPCErrorData
					_ = json.Unmarshal([]byte(v.Raw), &p)
					encoded, err = json.Marshal(p)
				}
				if err != nil || ValidateRPCControlDTO(v.DTO, encoded, v.Directional) != nil {
					t.Fatal("codec roundtrip")
				}
			}
		})
	}
}
