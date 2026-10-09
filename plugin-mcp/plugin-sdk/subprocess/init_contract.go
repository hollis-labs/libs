package subprocess

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/capability"
	"github.com/hollis-labs/libs/plugin-mcp/plugin-sdk/internal/strictjson"
)

type InitFailureCode string

const (
	InitInvalid                    InitFailureCode = "invalid_init"
	InitProtocolMismatch           InitFailureCode = "protocol_mismatch"
	InitCapabilityContractMismatch InitFailureCode = "capability_contract_mismatch"
	InitProfileMismatch            InitFailureCode = "profile_mismatch"
)

// InitError is structural handshake failure, not a host application error.
// It excludes rejected values and is available to errors.As callers.
type InitError struct {
	Code               InitFailureCode
	Field              string
	Expected, Received int
}

func (e *InitError) Error() string { return fmt.Sprintf("subprocess init: %s (%s)", e.Code, e.Field) }
func (e *InitError) RPCData() map[string]any {
	d := map[string]any{"contract": "plugin-init/2", "code": e.Code, "field": e.Field}
	if e.Code != InitInvalid {
		d["expected"] = e.Expected
		d["received"] = e.Received
	}
	return d
}
func initInvalid(field string) error { return &InitError{Code: InitInvalid, Field: field} }
func initVersion(code InitFailureCode, field string, got, want int) error {
	if got != want {
		return &InitError{Code: code, Field: field, Expected: want, Received: got}
	}
	return nil
}
func initFields(data []byte, field string, required, optional []string) (map[string]json.RawMessage, error) {
	f, err := strictjson.ObjectFields(data, required, optional)
	if err != nil {
		return nil, initInvalid(field)
	}
	return f, nil
}
func initInteger(data []byte, field string) (int, error) {
	b := bytes.TrimSpace(data)
	if len(b) == 0 {
		return 0, initInvalid(field)
	}
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, initInvalid(field)
		}
	}
	v, err := strconv.ParseUint(string(b), 10, 32)
	if err != nil {
		return 0, initInvalid(field)
	}
	return int(v), nil
}
func initStrings(data []byte, field string) error {
	var values map[string]json.RawMessage
	if json.Unmarshal(data, &values) != nil || values == nil {
		return initInvalid(field)
	}
	for _, v := range values {
		var s *string
		if json.Unmarshal(v, &s) != nil || s == nil {
			return initInvalid(field)
		}
	}
	return nil
}

func (p InitParams) Validate() error {
	if err := initVersion(InitProtocolMismatch, "host_info.protocol", p.HostInfo.Protocol, ProtocolVersion); err != nil {
		return err
	}
	if err := initVersion(InitCapabilityContractMismatch, "capability_contract", p.CapabilityContract, capability.ContractVersion); err != nil {
		return err
	}
	for _, f := range []struct{ name, value string }{{"plugin_dir", p.PluginDir}, {"data_dir", p.DataDir}, {"cache_dir", p.CacheDir}, {"host_info.version", p.HostInfo.Version}} {
		if !utf8.ValidString(f.value) || strings.TrimSpace(f.value) == "" {
			return initInvalid(f.name)
		}
	}
	if p.Config == nil {
		return initInvalid("config")
	}
	for key, value := range p.Config {
		if !utf8.ValidString(key) || !utf8.ValidString(value) {
			return initInvalid("config")
		}
	}
	switch p.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return initInvalid("log_level")
	}
	if p.GrantsRenewalVersion != nil && *p.GrantsRenewalVersion != GrantsRenewalVersion {
		return initInvalid("grants_renewal_version")
	}
	if p.Grants.ValidateForRuntime(p.Incarnation) != nil {
		return initInvalid("grants/incarnation")
	}
	if len(p.Identity) > 0 && (strictjson.Validate(p.Identity) != nil || bytes.Equal(bytes.TrimSpace(p.Identity), []byte("null"))) {
		return initInvalid("identity")
	}
	if p.Context != nil && p.Context.Validate() != nil {
		return initInvalid("context")
	}
	if p.HostServices != nil {
		if err := p.HostServices.Validate(); err != nil {
			return err
		}
		if p.HostServices.Incarnation != p.Incarnation {
			return initInvalid("host_services.incarnation")
		}
	}
	if p.HooksProfile != nil {
		return initVersion(InitProfileMismatch, "hooks_profile.hooks_profile_version", p.HooksProfile.HooksProfileVersion, 1)
	}
	return nil
}
func (p InitParams) MarshalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	type plain InitParams
	return json.Marshal(plain(p))
}
func (p *InitParams) UnmarshalJSON(data []byte) error {
	f, err := initFields(data, "params", []string{"plugin_dir", "data_dir", "cache_dir", "config", "log_level", "host_info", "capability_contract", "incarnation", "grants"}, []string{"identity", "host_services", "hooks_profile", "context", "grants_renewal_version"})
	if err != nil {
		return err
	}
	host, err := initFields(f["host_info"], "host_info", []string{"version", "protocol"}, nil)
	if err != nil {
		return err
	}
	protocol, err := initInteger(host["protocol"], "host_info.protocol")
	if err != nil {
		return err
	}
	if err := initVersion(InitProtocolMismatch, "host_info.protocol", protocol, ProtocolVersion); err != nil {
		return err
	}
	contract, err := initInteger(f["capability_contract"], "capability_contract")
	if err != nil {
		return err
	}
	if err := initVersion(InitCapabilityContractMismatch, "capability_contract", contract, capability.ContractVersion); err != nil {
		return err
	}
	if err := initStrings(f["config"], "config"); err != nil {
		return err
	}
	if v, ok := f["context"]; ok && ValidateHostRPCDTO("ForwardContext", v) != nil {
		return initInvalid("context")
	}
	if raw, ok := f["grants_renewal_version"]; ok {
		version, err := initInteger(raw, "grants_renewal_version")
		if err != nil {
			return err
		}
		if version != GrantsRenewalVersion {
			return initInvalid("grants_renewal_version")
		}
	}
	type plain InitParams
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		var typed *InitError
		if errors.As(err, &typed) {
			return typed
		}
		return initInvalid("params")
	}
	if err := InitParams(next).Validate(); err != nil {
		return err
	}
	*p = InitParams(next)
	return nil
}

func (r InitResult) Validate() error {
	if err := initVersion(InitProtocolMismatch, "protocol", r.Protocol, ProtocolVersion); err != nil {
		return err
	}
	if err := initVersion(InitCapabilityContractMismatch, "capability_contract", r.CapabilityContract, capability.ContractVersion); err != nil {
		return err
	}
	for _, f := range []struct{ name, value string }{{"id", r.ID}, {"name", r.Name}, {"version", r.Version}} {
		if !utf8.ValidString(f.value) || strings.TrimSpace(f.value) == "" {
			return initInvalid(f.name)
		}
	}
	if !utf8.ValidString(r.Description) {
		return initInvalid("description")
	}
	if r.GrantsRenewalVersion != nil && *r.GrantsRenewalVersion != GrantsRenewalVersion {
		return initInvalid("grants_renewal_version")
	}
	if r.ReverseRPCVersion != nil {
		if err := initVersion(InitProfileMismatch, "reverse_rpc_version", *r.ReverseRPCVersion, 1); err != nil {
			return err
		}
	}
	if r.HooksProfileVersion != nil {
		if err := initVersion(InitProfileMismatch, "hooks_profile_version", *r.HooksProfileVersion, 1); err != nil {
			return err
		}
	}
	return nil
}

// ValidateInitResult checks agreement; hosts additionally check expected plugin
// identity/version and whether their required service profiles were accepted.
func ValidateInitResult(p InitParams, r InitResult) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if r.GrantsRenewalVersion != nil && p.GrantsRenewalVersion == nil {
		return &InitError{Code: InitProfileMismatch, Field: "grants_renewal_version", Expected: 0, Received: *r.GrantsRenewalVersion}
	}
	if r.ReverseRPCVersion != nil && p.HostServices == nil {
		return &InitError{Code: InitProfileMismatch, Field: "reverse_rpc_version", Expected: 0, Received: *r.ReverseRPCVersion}
	}
	if r.HooksProfileVersion != nil && p.HooksProfile == nil {
		return &InitError{Code: InitProfileMismatch, Field: "hooks_profile_version", Expected: 0, Received: *r.HooksProfileVersion}
	}
	return nil
}
func (r InitResult) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	type plain InitResult
	return json.Marshal(plain(r))
}
func (r *InitResult) UnmarshalJSON(data []byte) error {
	f, err := initFields(data, "result", []string{"id", "name", "version", "description", "protocol", "capability_contract"}, []string{"reverse_rpc_version", "hooks_profile_version", "grants_renewal_version"})
	if err != nil {
		return err
	}
	for _, v := range []struct {
		field string
		code  InitFailureCode
		want  int
	}{{"protocol", InitProtocolMismatch, ProtocolVersion}, {"capability_contract", InitCapabilityContractMismatch, capability.ContractVersion}, {"reverse_rpc_version", InitProfileMismatch, 1}, {"hooks_profile_version", InitProfileMismatch, 1}, {"grants_renewal_version", InitProfileMismatch, 1}} {
		raw, ok := f[v.field]
		if !ok {
			continue
		}
		n, err := initInteger(raw, v.field)
		if err != nil {
			return err
		}
		if err := initVersion(v.code, v.field, n, v.want); err != nil {
			return err
		}
	}
	type plain InitResult
	var next plain
	if json.Unmarshal(data, &next) != nil {
		return initInvalid("result")
	}
	if err := InitResult(next).Validate(); err != nil {
		return err
	}
	*r = InitResult(next)
	return nil
}

var hostServiceMethods = map[string]bool{
	"host/storage/get": true, "host/storage/put": true, "host/storage/delete": true, "host/secrets/get": true, "host/egress/request": true, "host/events/publish": true, "host/log": true, "host/readonly/query": true, "host/mcp/list_tools": true, "host/mcp/call_tool": true, "host/mcp/cancel_call": true, "host/bindings/renew": true,
}
var limitFields = []string{"host_to_plugin_inflight", "plugin_to_host_inflight", "host_global_inflight", "control_slots", "max_frame_bytes", "max_queued_write_bytes", "write_timeout_ms", "max_depth", "method_timeout_ms"}

func (h HostServices) Validate() error {
	if err := initVersion(InitProfileMismatch, "host_services.reverse_rpc_version", h.ReverseRPCVersion, 1); err != nil {
		return err
	}
	if h.Incarnation.Validate() != nil || h.Methods == nil {
		return initInvalid("host_services")
	}
	l := h.Limits
	for _, v := range []uint32{l.HostToPluginInflight, l.PluginToHostInflight, l.HostGlobalInflight, l.ControlSlots, l.MaxFrameBytes, l.MaxQueuedWriteBytes, l.WriteTimeoutMS, l.MaxDepth} {
		if v == 0 {
			return initInvalid("host_services.limits")
		}
	}
	if l.ControlSlots < 2 || l.MaxQueuedWriteBytes < l.MaxFrameBytes || l.MethodTimeoutMS == nil || len(l.MethodTimeoutMS) != len(h.Methods) {
		return initInvalid("host_services.limits")
	}
	seen := map[string]bool{}
	for _, m := range h.Methods {
		if !hostServiceMethods[m] || seen[m] || l.MethodTimeoutMS[m] == 0 {
			return initInvalid("host_services.methods")
		}
		seen[m] = true
	}
	return nil
}
func (h *HostServices) UnmarshalJSON(data []byte) error {
	f, err := initFields(data, "host_services", []string{"reverse_rpc_version", "incarnation", "methods", "limits"}, nil)
	if err != nil {
		return err
	}
	n, err := initInteger(f["reverse_rpc_version"], "host_services.reverse_rpc_version")
	if err != nil {
		return err
	}
	if err := initVersion(InitProfileMismatch, "host_services.reverse_rpc_version", n, 1); err != nil {
		return err
	}
	limits, err := initFields(f["limits"], "host_services.limits", limitFields, nil)
	if err != nil {
		return err
	}
	for _, k := range limitFields {
		if k == "method_timeout_ms" {
			continue
		}
		if _, err := initInteger(limits[k], "host_services.limits."+k); err != nil {
			return err
		}
	}
	var timeouts map[string]json.RawMessage
	if json.Unmarshal(limits["method_timeout_ms"], &timeouts) != nil || timeouts == nil {
		return initInvalid("host_services.limits.method_timeout_ms")
	}
	for _, v := range timeouts {
		if _, err := initInteger(v, "host_services.limits.method_timeout_ms"); err != nil {
			return err
		}
	}
	var methods []json.RawMessage
	if json.Unmarshal(f["methods"], &methods) != nil || methods == nil {
		return initInvalid("host_services.methods")
	}
	for _, v := range methods {
		var s *string
		if json.Unmarshal(v, &s) != nil || s == nil {
			return initInvalid("host_services.methods")
		}
	}
	type plain HostServices
	var next plain
	if json.Unmarshal(data, &next) != nil {
		return initInvalid("host_services")
	}
	if err := HostServices(next).Validate(); err != nil {
		return err
	}
	*h = HostServices(next)
	return nil
}
func (h *HooksProfile) UnmarshalJSON(data []byte) error {
	f, err := initFields(data, "hooks_profile", []string{"hooks_profile_version"}, nil)
	if err != nil {
		return err
	}
	n, err := initInteger(f["hooks_profile_version"], "hooks_profile.hooks_profile_version")
	if err != nil {
		return err
	}
	if err := initVersion(InitProfileMismatch, "hooks_profile.hooks_profile_version", n, 1); err != nil {
		return err
	}
	h.HooksProfileVersion = n
	return nil
}
