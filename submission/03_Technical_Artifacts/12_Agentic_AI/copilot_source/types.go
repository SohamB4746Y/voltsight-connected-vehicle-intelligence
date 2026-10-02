// Package copilot is the Charge Ops Copilot: a scoped assistant that answers fleet questions and proposes
// charging plans using ONLY typed tools that call the platform with the caller's own identity. The caller's
// tenant and permissions come from the verified token, never from the model; every step is audited; every write
// is two-phase (the agent can only propose, a person approves); every statement must be backed by a tool result.
package copilot

import "encoding/json"

// ToolCall is a request to run a tool.
type ToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// ToolResult is what a tool returned. Refs are the evidence identifiers (alert:<id>, vehicle:<vin>, ...) that an
// answer may cite. Untrusted marks free text that originates from data (incident narratives): it is passed to
// the model as data, never as instructions.
type ToolResult struct {
	CallID    string   `json:"call_id"`
	Name      string   `json:"name"`
	OK        bool     `json:"ok"`
	Error     string   `json:"error,omitempty"`
	Data      any      `json:"data,omitempty"`
	Refs      []string `json:"refs,omitempty"`
	Untrusted bool     `json:"untrusted,omitempty"`
}

// Turn is one entry of the conversation.
type Turn struct {
	Role    string // "user" | "assistant"
	Text    string
	Calls   []ToolCall
	Results []ToolResult
}

// Step is what a provider decides next: run tools, or give the final answer.
type Step struct {
	Calls  []ToolCall
	Answer string
}

// Pending is a write the agent proposed and a person must approve.
type Pending struct {
	ID      string `json:"id"`
	Tool    string `json:"tool"`
	Summary string `json:"summary"`
}

// Response is returned to the console.
type Response struct {
	SessionID string       `json:"session_id"`
	Answer    string       `json:"answer"`
	Citations []string     `json:"citations"`
	ToolCalls []CallRecord `json:"tool_calls"`
	Pending   []Pending    `json:"pending_actions"`
	Provider  string       `json:"provider"`
}

// CallRecord summarises one executed tool call.
type CallRecord struct {
	Tool string `json:"tool"`
	OK   bool   `json:"ok"`
}
