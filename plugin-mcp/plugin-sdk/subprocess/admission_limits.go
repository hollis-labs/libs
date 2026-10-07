package subprocess

import "errors"

// AdmissionLimits narrow connection-local handler ceilings. Zero uses the
// default. Accepted offers may narrow these further, never widen them.
type AdmissionLimits struct{ Forward, Reverse, Control int }

func admissionLimits(v AdmissionLimits) (AdmissionLimits, error) {
	if v.Forward == 0 {
		v.Forward = ForwardHandlerSlots
	}
	if v.Reverse == 0 {
		v.Reverse = ReverseHandlerSlots
	}
	if v.Control == 0 {
		v.Control = ControlHandlerSlots
	}
	if v.Forward < 1 || v.Forward > ForwardHandlerSlots || v.Reverse < 1 || v.Reverse > ReverseHandlerSlots || v.Control < 1 || v.Control > ControlHandlerSlots {
		return v, errors.New("subprocess: admission limits must be positive and may only narrow defaults")
	}
	return v, nil
}
