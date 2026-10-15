package subprocess

import "encoding/json"

// BindingID is an opaque host-issued reference, never an SDK-minted credential.
type BindingID string

// HostRPCRequestOwner identifies the initiating direction in the host ledger.
type HostRPCRequestOwner string

// MCPTool metadata and tool_binding are issued and pinned by the host.
type MCPTool struct {
	ToolName     string          `json:"tool_name"`
	Description  *string         `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
	Effect       string          `json:"effect"`
	ToolBinding  string          `json:"tool_binding"`
}

func (v MCPTool) MarshalJSON() ([]byte, error) {
	type plain MCPTool
	return hostRPCMarshal("MCPTool", plain(v))
}
func (v *MCPTool) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("MCPTool", data); err != nil {
		return err
	}
	type plain MCPTool
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("MCPTool", "invalid field type")
	}
	*v = MCPTool(next)
	return nil
}
func (v MCPTool) Validate() error { _, err := v.MarshalJSON(); return err }

const (
	HostRPCOwnerHost   HostRPCRequestOwner = "host"
	HostRPCOwnerPlugin HostRPCRequestOwner = "plugin"
)

// HostRPC DTOs validate structure only. Hosts own binding/grant policy,
// target schemas, receipt correlation, and live deadlines.

type ParentCall struct {
	RequestOwner HostRPCRequestOwner `json:"request_owner"`
	ID           uint64              `json:"id"`
}

type ForwardContext struct {
	BindingID *BindingID `json:"binding_id,omitempty"`
	TimeoutMS uint32     `json:"timeout_ms"`
}

type ReverseContext struct {
	BindingID  BindingID  `json:"binding_id"`
	TimeoutMS  uint32     `json:"timeout_ms"`
	ParentCall ParentCall `json:"parent_call"`
}

type HostRPCHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type HostRPCLogField struct {
	Name  string          `json:"name"`
	Value json.RawMessage `json:"value"`
}

type HostRPCRemainingBudgets struct {
	TimeoutMS uint32  `json:"timeout_ms"`
	Bytes     *uint64 `json:"bytes,omitempty"`
	Effects   *uint64 `json:"effects,omitempty"`
	Tokens    *uint64 `json:"tokens,omitempty"`
}

type StorageGetParams struct {
	GrantID string         `json:"grant_id"`
	Context ReverseContext `json:"context"`
	Key     string         `json:"key"`
}

type StorageGetResult struct {
	Found    bool            `json:"found"`
	Value    json.RawMessage `json:"value,omitempty"`
	Revision *string         `json:"revision,omitempty"`
}

type StoragePutParams struct {
	GrantID          string          `json:"grant_id"`
	Context          ReverseContext  `json:"context"`
	Key              string          `json:"key"`
	Value            json.RawMessage `json:"value"`
	ExpectedRevision *string         `json:"expected_revision"`
	OperationKey     string          `json:"operation_key"`
}

type StoragePutResult struct {
	OperationKey string `json:"operation_key"`
	Revision     string `json:"revision"`
}

type StorageDeleteParams struct {
	GrantID          string         `json:"grant_id"`
	Context          ReverseContext `json:"context"`
	Key              string         `json:"key"`
	ExpectedRevision string         `json:"expected_revision"`
	OperationKey     string         `json:"operation_key"`
}

type StorageDeleteResult struct {
	OperationKey string `json:"operation_key"`
	Deleted      bool   `json:"deleted"`
	Revision     string `json:"revision"`
}

type SecretsGetParams struct {
	GrantID   string         `json:"grant_id"`
	Context   ReverseContext `json:"context"`
	SecretRef string         `json:"secret_ref"`
}

type SecretsGetResult struct {
	ValueBase64 string `json:"value_base64"`
	ExpiresAt   string `json:"expires_at"`
}

type EgressRequestParams struct {
	GrantID      string           `json:"grant_id"`
	Context      ReverseContext   `json:"context"`
	Method       string           `json:"method"`
	URL          string           `json:"url"`
	Headers      *[]HostRPCHeader `json:"headers,omitempty"`
	BodyBase64   *string          `json:"body_base64,omitempty"`
	OperationKey *string          `json:"operation_key,omitempty"`
}

type EgressRequestResult struct {
	Status       uint64          `json:"status"`
	Headers      []HostRPCHeader `json:"headers"`
	BodyBase64   string          `json:"body_base64"`
	OperationKey *string         `json:"operation_key,omitempty"`
}

type EventsPublishParams struct {
	GrantID      string          `json:"grant_id"`
	Context      ReverseContext  `json:"context"`
	EventName    string          `json:"event_name"`
	Payload      json.RawMessage `json:"payload"`
	OperationKey string          `json:"operation_key"`
}

type EventsPublishResult struct {
	OperationKey string `json:"operation_key"`
	Accepted     bool   `json:"accepted"`
	EventID      string `json:"event_id"`
}

type LogParams struct {
	GrantID string             `json:"grant_id"`
	Context ReverseContext     `json:"context"`
	Level   string             `json:"level"`
	Message string             `json:"message"`
	Fields  *[]HostRPCLogField `json:"fields,omitempty"`
}

type LogResult struct {
	Accepted bool `json:"accepted"`
}

type ReadonlyQueryParams struct {
	GrantID       string          `json:"grant_id"`
	Context       ReverseContext  `json:"context"`
	Resource      string          `json:"resource"`
	SchemaVersion uint32          `json:"schema_version"`
	Params        json.RawMessage `json:"params"`
}

type ReadonlyQueryResult struct {
	Resource      string          `json:"resource"`
	SchemaVersion uint32          `json:"schema_version"`
	Data          json.RawMessage `json:"data"`
}

type MCPListToolsParams struct {
	GrantID  string         `json:"grant_id"`
	Context  ReverseContext `json:"context"`
	ServerID string         `json:"server_id"`
	Cursor   *string        `json:"cursor,omitempty"`
}

type MCPListToolsResult struct {
	ServerID   string    `json:"server_id"`
	Tools      []MCPTool `json:"tools"`
	NextCursor *string   `json:"next_cursor,omitempty"`
}

type MCPCallToolParams struct {
	GrantID      string          `json:"grant_id"`
	Context      ReverseContext  `json:"context"`
	ServerID     string          `json:"server_id"`
	ToolName     string          `json:"tool_name"`
	ToolBinding  string          `json:"tool_binding"`
	Arguments    json.RawMessage `json:"arguments"`
	OperationKey *string         `json:"operation_key,omitempty"`
}

type MCPCallToolResult struct {
	Content           []json.RawMessage `json:"content"`
	IsError           bool              `json:"is_error"`
	StructuredContent json.RawMessage   `json:"structured_content,omitempty"`
	OperationKey      *string           `json:"operation_key,omitempty"`
}

type MCPCancelCallParams struct {
	GrantID      string         `json:"grant_id"`
	Context      ReverseContext `json:"context"`
	TargetCallID uint64         `json:"target_call_id"`
}

type MCPCancelCallResult struct {
	Accepted        bool `json:"accepted"`
	AlreadyTerminal bool `json:"already_terminal"`
}

type BindingsRenewParams struct {
	GrantID          string         `json:"grant_id"`
	Context          ReverseContext `json:"context"`
	RequestedLeaseMS uint64         `json:"requested_lease_ms"`
}

type BindingsRenewResult struct {
	BindingID        BindingID               `json:"binding_id"`
	ExpiresAt        string                  `json:"expires_at"`
	RemainingBudgets HostRPCRemainingBudgets `json:"remaining_budgets"`
}

func (v ParentCall) MarshalJSON() ([]byte, error) {
	type plain ParentCall
	return hostRPCMarshal("ParentCall", plain(v))
}
func (v *ParentCall) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("ParentCall", data); err != nil {
		return err
	}
	type plain ParentCall
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("ParentCall", "invalid field type")
	}
	*v = ParentCall(next)
	return nil
}
func (v ParentCall) Validate() error { _, err := v.MarshalJSON(); return err }

func (v ForwardContext) MarshalJSON() ([]byte, error) {
	type plain ForwardContext
	return hostRPCMarshal("ForwardContext", plain(v))
}
func (v *ForwardContext) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("ForwardContext", data); err != nil {
		return err
	}
	type plain ForwardContext
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("ForwardContext", "invalid field type")
	}
	*v = ForwardContext(next)
	return nil
}
func (v ForwardContext) Validate() error { _, err := v.MarshalJSON(); return err }

func (v ReverseContext) MarshalJSON() ([]byte, error) {
	type plain ReverseContext
	return hostRPCMarshal("ReverseContext", plain(v))
}
func (v *ReverseContext) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("ReverseContext", data); err != nil {
		return err
	}
	type plain ReverseContext
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("ReverseContext", "invalid field type")
	}
	*v = ReverseContext(next)
	return nil
}
func (v ReverseContext) Validate() error { _, err := v.MarshalJSON(); return err }

func (v HostRPCHeader) MarshalJSON() ([]byte, error) {
	type plain HostRPCHeader
	return hostRPCMarshal("Header", plain(v))
}
func (v *HostRPCHeader) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("Header", data); err != nil {
		return err
	}
	type plain HostRPCHeader
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("Header", "invalid field type")
	}
	*v = HostRPCHeader(next)
	return nil
}
func (v HostRPCHeader) Validate() error { _, err := v.MarshalJSON(); return err }

func (v HostRPCLogField) MarshalJSON() ([]byte, error) {
	type plain HostRPCLogField
	return hostRPCMarshal("LogField", plain(v))
}
func (v *HostRPCLogField) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("LogField", data); err != nil {
		return err
	}
	type plain HostRPCLogField
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("LogField", "invalid field type")
	}
	*v = HostRPCLogField(next)
	return nil
}
func (v HostRPCLogField) Validate() error { _, err := v.MarshalJSON(); return err }

func (v HostRPCRemainingBudgets) MarshalJSON() ([]byte, error) {
	type plain HostRPCRemainingBudgets
	return hostRPCMarshal("RemainingBudgets", plain(v))
}
func (v *HostRPCRemainingBudgets) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("RemainingBudgets", data); err != nil {
		return err
	}
	type plain HostRPCRemainingBudgets
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("RemainingBudgets", "invalid field type")
	}
	*v = HostRPCRemainingBudgets(next)
	return nil
}
func (v HostRPCRemainingBudgets) Validate() error { _, err := v.MarshalJSON(); return err }

func (v StorageGetParams) MarshalJSON() ([]byte, error) {
	type plain StorageGetParams
	return hostRPCMarshal("StorageGetParams", plain(v))
}
func (v *StorageGetParams) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("StorageGetParams", data); err != nil {
		return err
	}
	type plain StorageGetParams
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("StorageGetParams", "invalid field type")
	}
	*v = StorageGetParams(next)
	return nil
}
func (v StorageGetParams) Validate() error { _, err := v.MarshalJSON(); return err }

func (v StorageGetResult) MarshalJSON() ([]byte, error) {
	type plain StorageGetResult
	return hostRPCMarshal("StorageGetResult", plain(v))
}
func (v *StorageGetResult) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("StorageGetResult", data); err != nil {
		return err
	}
	type plain StorageGetResult
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("StorageGetResult", "invalid field type")
	}
	*v = StorageGetResult(next)
	return nil
}
func (v StorageGetResult) Validate() error { _, err := v.MarshalJSON(); return err }

func (v StoragePutParams) MarshalJSON() ([]byte, error) {
	type plain StoragePutParams
	return hostRPCMarshal("StoragePutParams", plain(v))
}
func (v *StoragePutParams) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("StoragePutParams", data); err != nil {
		return err
	}
	type plain StoragePutParams
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("StoragePutParams", "invalid field type")
	}
	*v = StoragePutParams(next)
	return nil
}
func (v StoragePutParams) Validate() error { _, err := v.MarshalJSON(); return err }

func (v StoragePutResult) MarshalJSON() ([]byte, error) {
	type plain StoragePutResult
	return hostRPCMarshal("StoragePutResult", plain(v))
}
func (v *StoragePutResult) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("StoragePutResult", data); err != nil {
		return err
	}
	type plain StoragePutResult
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("StoragePutResult", "invalid field type")
	}
	*v = StoragePutResult(next)
	return nil
}
func (v StoragePutResult) Validate() error { _, err := v.MarshalJSON(); return err }

func (v StorageDeleteParams) MarshalJSON() ([]byte, error) {
	type plain StorageDeleteParams
	return hostRPCMarshal("StorageDeleteParams", plain(v))
}
func (v *StorageDeleteParams) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("StorageDeleteParams", data); err != nil {
		return err
	}
	type plain StorageDeleteParams
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("StorageDeleteParams", "invalid field type")
	}
	*v = StorageDeleteParams(next)
	return nil
}
func (v StorageDeleteParams) Validate() error { _, err := v.MarshalJSON(); return err }

func (v StorageDeleteResult) MarshalJSON() ([]byte, error) {
	type plain StorageDeleteResult
	return hostRPCMarshal("StorageDeleteResult", plain(v))
}
func (v *StorageDeleteResult) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("StorageDeleteResult", data); err != nil {
		return err
	}
	type plain StorageDeleteResult
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("StorageDeleteResult", "invalid field type")
	}
	*v = StorageDeleteResult(next)
	return nil
}
func (v StorageDeleteResult) Validate() error { _, err := v.MarshalJSON(); return err }

func (v SecretsGetParams) MarshalJSON() ([]byte, error) {
	type plain SecretsGetParams
	return hostRPCMarshal("SecretsGetParams", plain(v))
}
func (v *SecretsGetParams) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("SecretsGetParams", data); err != nil {
		return err
	}
	type plain SecretsGetParams
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("SecretsGetParams", "invalid field type")
	}
	*v = SecretsGetParams(next)
	return nil
}
func (v SecretsGetParams) Validate() error { _, err := v.MarshalJSON(); return err }

func (v SecretsGetResult) MarshalJSON() ([]byte, error) {
	type plain SecretsGetResult
	return hostRPCMarshal("SecretsGetResult", plain(v))
}
func (v *SecretsGetResult) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("SecretsGetResult", data); err != nil {
		return err
	}
	type plain SecretsGetResult
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("SecretsGetResult", "invalid field type")
	}
	*v = SecretsGetResult(next)
	return nil
}
func (v SecretsGetResult) Validate() error { _, err := v.MarshalJSON(); return err }

func (v EgressRequestParams) MarshalJSON() ([]byte, error) {
	type plain EgressRequestParams
	return hostRPCMarshal("EgressRequestParams", plain(v))
}
func (v *EgressRequestParams) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("EgressRequestParams", data); err != nil {
		return err
	}
	type plain EgressRequestParams
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("EgressRequestParams", "invalid field type")
	}
	*v = EgressRequestParams(next)
	return nil
}
func (v EgressRequestParams) Validate() error { _, err := v.MarshalJSON(); return err }

func (v EgressRequestResult) MarshalJSON() ([]byte, error) {
	type plain EgressRequestResult
	return hostRPCMarshal("EgressRequestResult", plain(v))
}
func (v *EgressRequestResult) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("EgressRequestResult", data); err != nil {
		return err
	}
	type plain EgressRequestResult
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("EgressRequestResult", "invalid field type")
	}
	*v = EgressRequestResult(next)
	return nil
}
func (v EgressRequestResult) Validate() error { _, err := v.MarshalJSON(); return err }

func (v EventsPublishParams) MarshalJSON() ([]byte, error) {
	type plain EventsPublishParams
	return hostRPCMarshal("EventsPublishParams", plain(v))
}
func (v *EventsPublishParams) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("EventsPublishParams", data); err != nil {
		return err
	}
	type plain EventsPublishParams
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("EventsPublishParams", "invalid field type")
	}
	*v = EventsPublishParams(next)
	return nil
}
func (v EventsPublishParams) Validate() error { _, err := v.MarshalJSON(); return err }

func (v EventsPublishResult) MarshalJSON() ([]byte, error) {
	type plain EventsPublishResult
	return hostRPCMarshal("EventsPublishResult", plain(v))
}
func (v *EventsPublishResult) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("EventsPublishResult", data); err != nil {
		return err
	}
	type plain EventsPublishResult
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("EventsPublishResult", "invalid field type")
	}
	*v = EventsPublishResult(next)
	return nil
}
func (v EventsPublishResult) Validate() error { _, err := v.MarshalJSON(); return err }

func (v LogParams) MarshalJSON() ([]byte, error) {
	type plain LogParams
	return hostRPCMarshal("LogParams", plain(v))
}
func (v *LogParams) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("LogParams", data); err != nil {
		return err
	}
	type plain LogParams
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("LogParams", "invalid field type")
	}
	*v = LogParams(next)
	return nil
}
func (v LogParams) Validate() error { _, err := v.MarshalJSON(); return err }

func (v LogResult) MarshalJSON() ([]byte, error) {
	type plain LogResult
	return hostRPCMarshal("LogResult", plain(v))
}
func (v *LogResult) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("LogResult", data); err != nil {
		return err
	}
	type plain LogResult
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("LogResult", "invalid field type")
	}
	*v = LogResult(next)
	return nil
}
func (v LogResult) Validate() error { _, err := v.MarshalJSON(); return err }

func (v ReadonlyQueryParams) MarshalJSON() ([]byte, error) {
	type plain ReadonlyQueryParams
	return hostRPCMarshal("ReadonlyQueryParams", plain(v))
}
func (v *ReadonlyQueryParams) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("ReadonlyQueryParams", data); err != nil {
		return err
	}
	type plain ReadonlyQueryParams
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("ReadonlyQueryParams", "invalid field type")
	}
	*v = ReadonlyQueryParams(next)
	return nil
}
func (v ReadonlyQueryParams) Validate() error { _, err := v.MarshalJSON(); return err }

func (v ReadonlyQueryResult) MarshalJSON() ([]byte, error) {
	type plain ReadonlyQueryResult
	return hostRPCMarshal("ReadonlyQueryResult", plain(v))
}
func (v *ReadonlyQueryResult) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("ReadonlyQueryResult", data); err != nil {
		return err
	}
	type plain ReadonlyQueryResult
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("ReadonlyQueryResult", "invalid field type")
	}
	*v = ReadonlyQueryResult(next)
	return nil
}
func (v ReadonlyQueryResult) Validate() error { _, err := v.MarshalJSON(); return err }

func (v MCPListToolsParams) MarshalJSON() ([]byte, error) {
	type plain MCPListToolsParams
	return hostRPCMarshal("MCPListToolsParams", plain(v))
}
func (v *MCPListToolsParams) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("MCPListToolsParams", data); err != nil {
		return err
	}
	type plain MCPListToolsParams
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("MCPListToolsParams", "invalid field type")
	}
	*v = MCPListToolsParams(next)
	return nil
}
func (v MCPListToolsParams) Validate() error { _, err := v.MarshalJSON(); return err }

func (v MCPListToolsResult) MarshalJSON() ([]byte, error) {
	type plain MCPListToolsResult
	return hostRPCMarshal("MCPListToolsResult", plain(v))
}
func (v *MCPListToolsResult) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("MCPListToolsResult", data); err != nil {
		return err
	}
	type plain MCPListToolsResult
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("MCPListToolsResult", "invalid field type")
	}
	*v = MCPListToolsResult(next)
	return nil
}
func (v MCPListToolsResult) Validate() error { _, err := v.MarshalJSON(); return err }

func (v MCPCallToolParams) MarshalJSON() ([]byte, error) {
	type plain MCPCallToolParams
	return hostRPCMarshal("MCPCallToolParams", plain(v))
}
func (v *MCPCallToolParams) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("MCPCallToolParams", data); err != nil {
		return err
	}
	type plain MCPCallToolParams
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("MCPCallToolParams", "invalid field type")
	}
	*v = MCPCallToolParams(next)
	return nil
}
func (v MCPCallToolParams) Validate() error { _, err := v.MarshalJSON(); return err }

func (v MCPCallToolResult) MarshalJSON() ([]byte, error) {
	type plain MCPCallToolResult
	return hostRPCMarshal("MCPCallToolResult", plain(v))
}
func (v *MCPCallToolResult) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("MCPCallToolResult", data); err != nil {
		return err
	}
	type plain MCPCallToolResult
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("MCPCallToolResult", "invalid field type")
	}
	*v = MCPCallToolResult(next)
	return nil
}
func (v MCPCallToolResult) Validate() error { _, err := v.MarshalJSON(); return err }

func (v MCPCancelCallParams) MarshalJSON() ([]byte, error) {
	type plain MCPCancelCallParams
	return hostRPCMarshal("MCPCancelCallParams", plain(v))
}
func (v *MCPCancelCallParams) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("MCPCancelCallParams", data); err != nil {
		return err
	}
	type plain MCPCancelCallParams
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("MCPCancelCallParams", "invalid field type")
	}
	*v = MCPCancelCallParams(next)
	return nil
}
func (v MCPCancelCallParams) Validate() error { _, err := v.MarshalJSON(); return err }

func (v MCPCancelCallResult) MarshalJSON() ([]byte, error) {
	type plain MCPCancelCallResult
	return hostRPCMarshal("MCPCancelCallResult", plain(v))
}
func (v *MCPCancelCallResult) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("MCPCancelCallResult", data); err != nil {
		return err
	}
	type plain MCPCancelCallResult
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("MCPCancelCallResult", "invalid field type")
	}
	*v = MCPCancelCallResult(next)
	return nil
}
func (v MCPCancelCallResult) Validate() error { _, err := v.MarshalJSON(); return err }

func (v BindingsRenewParams) MarshalJSON() ([]byte, error) {
	type plain BindingsRenewParams
	return hostRPCMarshal("BindingsRenewParams", plain(v))
}
func (v *BindingsRenewParams) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("BindingsRenewParams", data); err != nil {
		return err
	}
	type plain BindingsRenewParams
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("BindingsRenewParams", "invalid field type")
	}
	*v = BindingsRenewParams(next)
	return nil
}
func (v BindingsRenewParams) Validate() error { _, err := v.MarshalJSON(); return err }

func (v BindingsRenewResult) MarshalJSON() ([]byte, error) {
	type plain BindingsRenewResult
	return hostRPCMarshal("BindingsRenewResult", plain(v))
}
func (v *BindingsRenewResult) UnmarshalJSON(data []byte) error {
	if err := ValidateHostRPCDTO("BindingsRenewResult", data); err != nil {
		return err
	}
	type plain BindingsRenewResult
	var next plain
	if err := json.Unmarshal(data, &next); err != nil {
		return hostRPCInvalid("BindingsRenewResult", "invalid field type")
	}
	*v = BindingsRenewResult(next)
	return nil
}
func (v BindingsRenewResult) Validate() error { _, err := v.MarshalJSON(); return err }
