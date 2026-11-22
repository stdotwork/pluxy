package llm

import "encoding/json"

// ToolCodeExecutionUnsent is the detail of the loss for a code execution tool an encoder leaves
// out because nothing says its upstream takes one.
const ToolCodeExecutionUnsent = "nothing says this upstream takes a code execution tool, so it is not sent"

// CodeExecution is one server-side code execution run reported in a reply (protocol minor version
// 32). It carries only what more than one format can express: the code or command that ran, its
// language if given, its text output, whether it failed, and whether non-text outputs (files or
// images) were omitted. A last record with only FilesOmitted set stands for the runs left out past
// MaxCodeExecutionBytes.
type CodeExecution struct {
	Code         string `json:"code,omitempty"`
	Language     string `json:"language,omitempty"`
	Output       string `json:"output,omitempty"`
	Failed       bool   `json:"failed,omitempty"`
	FilesOmitted bool   `json:"files_omitted,omitempty"`
}

// MaxCodeExecutionBytes bounds what one reply's runs hold. They travel together on the finish event,
// so without a limit a reply whose blocks each fit a protocol frame could fail on the last one.
const MaxCodeExecutionBytes = 4 << 20

// AppendCodeExecution adds run to runs, or, past MaxCodeExecutionBytes, ends the list with one record
// that says runs were left out.
func AppendCodeExecution(runs []CodeExecution, run CodeExecution) []CodeExecution {
	size := len(run.Code) + len(run.Language) + len(run.Output) + 64
	for _, r := range runs {
		size += len(r.Code) + len(r.Language) + len(r.Output) + 64
	}
	if n := len(runs); n > 0 && runs[n-1] == (CodeExecution{FilesOmitted: true}) {
		return runs
	}
	if size > MaxCodeExecutionBytes {
		return append(runs, CodeExecution{FilesOmitted: true})
	}
	return append(runs, run)
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
