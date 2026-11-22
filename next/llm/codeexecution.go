package llm

import "encoding/json"

// ToolCodeExecutionUnsent is the detail of the loss for a code execution tool an encoder leaves
// out because nothing says its upstream takes one.
const ToolCodeExecutionUnsent = "nothing says this upstream takes a code execution tool, so it is not sent"

// CodeExecution is one server-side code execution run reported in a reply (protocol minor version
// 32). It carries only what more than one format can express: the code or command that ran, its
// language if given, its text output, whether it failed, and whether non-text outputs (files or
// images) were omitted.
type CodeExecution struct {
	Code         string `json:"code,omitempty"`
	Language     string `json:"language,omitempty"`
	Output       string `json:"output,omitempty"`
	Failed       bool   `json:"failed,omitempty"`
	FilesOmitted bool   `json:"files_omitted,omitempty"`
}

// CodeExecutions is a list of code execution runs whose empty JSON array parses to nil.
type CodeExecutions []CodeExecution

// UnmarshalJSON decodes CodeExecutions and normalizes an empty JSON array to nil.
func (c *CodeExecutions) UnmarshalJSON(data []byte) error {
	var list []CodeExecution
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	if len(list) == 0 {
		*c = nil
		return nil
	}
	*c = list
	return nil
}
