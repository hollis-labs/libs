package llmtypes

import (
	"encoding/json"
	"testing"
)

func TestCacheHintTTL(t *testing.T) {
	for _, ttl := range []string{"", "5m", "1h", "60m", " 1h", "1H", "2h"} {
		hint := CacheHint{Position: "system", TTL: ttl}
		valid := ttl == "" || ttl == "5m" || ttl == "1h"
		if err := hint.ValidateTTL(); (err == nil) != valid {
			t.Fatalf("TTL %q: %v", ttl, err)
		}
		raw, err := json.Marshal(hint)
		if err != nil {
			t.Fatal(err)
		}
		if ttl == "" && string(raw) != `{"Position":"system","Index":0}` {
			t.Fatalf("empty TTL changed bytes: %s", raw)
		}
		var restored CacheHint
		if err = json.Unmarshal(raw, &restored); err != nil || restored != hint {
			t.Fatalf("round trip: %+v %v", restored, err)
		}
	}
}
