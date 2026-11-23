package llm

import "encoding/json"

// ToolCodeExecutionUnsent is the detail of the loss for a code execution tool an encoder leaves
// out because nothing says its upstream takes one.
const ToolCodeExecutionUnsent = "nothing says this upstream takes a code execution tool, so it is not sent"

// CodeExecution is one server-side code execution run reported in a reply (protocol minor version
// 32). It carries only what more than one format can express: the code or command that ran, its
// language if given, its text output, whether it failed, and whether non-text outputs (files or
// images) were omitted. A last record with only FilesOmitted set (see LeaveOutCodeExecutions)
// stands for runs the reply had that are not in the list: runs past MaxCodeExecutionBytes, a run
// whose code passes MaxCodeExecutionCodeBytes, and a run with no result a decoder could use.
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

// MaxCodeExecutionCodeBytes bounds the code or command of one run. A run whose code is longer is
// left out whole, never cut, so the code a client is shown is always the code that ran. The limit
// is the same for streamed and non-streamed replies.
const MaxCodeExecutionCodeBytes = 1 << 20

// codeExecutionsLeftOut is the record that stands for runs left out of a list.
var codeExecutionsLeftOut = CodeExecution{FilesOmitted: true}

// LeaveOutCodeExecutions ends runs with the one record that says runs were left out, unless it
// ends with it already. Decoders use it for a run they cannot carry, so a renderer reports the
// loss.
func LeaveOutCodeExecutions(runs []CodeExecution) []CodeExecution {
	if n := len(runs); n > 0 && runs[n-1] == codeExecutionsLeftOut {
		return runs
	}
	return append(runs, codeExecutionsLeftOut)
}

// AppendCodeExecution adds run to runs. A run whose code passes MaxCodeExecutionCodeBytes, or that
// would take the list past MaxCodeExecutionBytes, is left out instead and the list ends with the
// record that says runs were left out; a later run that fits is still added before that record.
func AppendCodeExecution(runs []CodeExecution, run CodeExecution) []CodeExecution {
	leftOut := false
	if n := len(runs); n > 0 && runs[n-1] == codeExecutionsLeftOut {
		runs, leftOut = runs[:n-1], true
	}
	size := len(run.Code) + len(run.Language) + len(run.Output) + 64
	for _, r := range runs {
		size += len(r.Code) + len(r.Language) + len(r.Output) + 64
	}
	if len(run.Code) > MaxCodeExecutionCodeBytes || size > MaxCodeExecutionBytes {
		return LeaveOutCodeExecutions(runs)
	}
	runs = append(runs, run)
	if leftOut {
		runs = LeaveOutCodeExecutions(runs)
	}
	return runs
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
