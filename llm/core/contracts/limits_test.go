package agentcontracts

import (
	"encoding/json"
	"reflect"
	"testing"
)

// ToolOutputBytes is Nanite's cumulative tool-output-per-turn ceiling: nil must
// round-trip to nil (the host's own default), and a set value must survive
// marshal/unmarshal unchanged, matching the convention every other Limits
// pointer field already follows.
func TestLimitsToolOutputBytesJSONRoundTrip(t *testing.T) {
	bytes := int64(2097152)
	tests := []struct {
		name     string
		limits   Limits
		wantJSON string
	}{
		{
			name:     "nil is omitted",
			limits:   Limits{},
			wantJSON: `{}`,
		},
		{
			name:     "set value round-trips",
			limits:   Limits{ToolOutputBytes: &bytes},
			wantJSON: `{"tool_output_bytes":2097152}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.limits)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.wantJSON {
				t.Errorf("Marshal() = %s, want %s", got, tc.wantJSON)
			}
			var back Limits
			if err := json.Unmarshal(got, &back); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(back, tc.limits) {
				t.Errorf("round trip changed Limits:\n got: %+v\nwant: %+v", back, tc.limits)
			}
		})
	}
}
