package capability

import "encoding/json"

// RequestID is a positive JavaScript-safe integer for host-rpc/1 correlation.
// It is distinct from the broader ID vocabulary in base JSON-RPC. Zero denotes
// an internal error without RPC correlation and cannot appear on the wire.
type RequestID uint64

func (id RequestID) Validate() error {
	if id == 0 || uint64(id) > MaxSafeInteger {
		return refusal(InvalidRequest, "")
	}
	return nil
}

func (id RequestID) MarshalJSON() ([]byte, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(uint64(id))
}

func (id *RequestID) UnmarshalJSON(data []byte) error {
	if id == nil || !rawInteger(data) {
		return refusal(InvalidRequest, "")
	}
	var next uint64
	if json.Unmarshal(data, &next) != nil {
		return refusal(InvalidRequest, "")
	}
	if err := RequestID(next).Validate(); err != nil {
		return err
	}
	*id = RequestID(next)
	return nil
}
