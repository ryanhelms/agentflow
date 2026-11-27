package acp

import "encoding/json"

// ProtocolVersion is the only ACP major version agentflow speaks.
// On the wire it is an integer (uint16).
const ProtocolVersion = 1

// Methods used in phase 6. Phase 7 adds the session methods.
const MethodInitialize = "initialize"

// ACP error codes. They overlap internal/jsonrpc's agentflow codes
// (-32002 is "resource not found" in ACP and "rate limited" in jsonrpc):
// an error from an ACP peer is always read by these meanings, never by the
// jsonrpc.Code* constants, which agentflow sends only to its own plugins.
const (
	CodeAuthRequired     = -32000
	CodeResourceNotFound = -32002
	CodeRequestCancelled = -32800
)

type InitializeRequest struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities ClientCapabilities `json:"clientCapabilities"`
	ClientInfo         *Implementation    `json:"clientInfo,omitempty"`
}

type ClientCapabilities struct {
	FS       FileSystemCapabilities `json:"fs"`
	Terminal bool                   `json:"terminal"`
	Auth     AuthCapabilities       `json:"auth"`
}

type FileSystemCapabilities struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

type AuthCapabilities struct {
	Terminal bool `json:"terminal"`
}

type Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

type InitializeResponse struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities AgentCapabilities `json:"agentCapabilities"`
	AuthMethods       []AuthMethod      `json:"authMethods"`
	AgentInfo         *Implementation   `json:"agentInfo,omitempty"`
}

type AgentCapabilities struct {
	LoadSession         bool                  `json:"loadSession"`
	PromptCapabilities  PromptCapabilities    `json:"promptCapabilities"`
	MCPCapabilities     MCPCapabilities       `json:"mcpCapabilities"`
	SessionCapabilities json.RawMessage       `json:"sessionCapabilities,omitempty"` // kept raw; phase 7 decodes it
	Auth                AgentAuthCapabilities `json:"auth"`
}

type PromptCapabilities struct {
	Image           bool `json:"image"`
	Audio           bool `json:"audio"`
	EmbeddedContext bool `json:"embeddedContext"`
}

type MCPCapabilities struct {
	HTTP bool `json:"http"`
	SSE  bool `json:"sse"`
}

type AgentAuthCapabilities struct {
	Logout *struct{} `json:"logout,omitempty"`
}

// AuthMethod is one advertised sign-in method. Type "" means "agent".
type AuthMethod struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Type        string            `json:"type,omitempty"` // "", "agent" or "terminal"
	Args        []string          `json:"args,omitempty"` // terminal only
	Env         map[string]string `json:"env,omitempty"`  // terminal only
}
