package llm

import (
	"encoding/json"
	"slices"
	"sync/atomic"
)

// ToolCodeExecutionUnsent is the detail of the loss for a code execution tool an encoder leaves
// out because nothing says its upstream takes one.
const ToolCodeExecutionUnsent = "nothing says this upstream takes a code execution tool, so it is not sent"

// CodeExecution is one server-side code execution run reported in a reply (protocol minor version
// 32). It carries only what more than one format can express: the code or command that ran, its
// language if given, its text output, whether it failed, and whether non-text outputs (files or
// images) were omitted. A last record with only FilesOmitted set (see CodeRuns)
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

// codeExecutionSize is what one run counts against MaxCodeExecutionBytes.
func codeExecutionSize(r CodeExecution) int {
	return len(r.Code) + len(r.Language) + len(r.Output) + 64
}

// codeRunSteps counts the runs the CodeRuns methods look at, so a test can bound their work
// without timing it.
var codeRunSteps atomic.Int64

// CodeRuns collects a reply's runs for a decoder. It keeps the total size of the runs so each Append
// costs the size of the run it adds, not of the whole list. The zero value is an empty list.
type CodeRuns struct {
	runs    []CodeExecution
	size    int
	leftOut bool
}

// Append adds run. A run whose code passes MaxCodeExecutionCodeBytes, or that would take the list
// past MaxCodeExecutionBytes, is left out instead and the list ends with the record that says runs
// were left out; a later run that fits is still added before that record.
func (c *CodeRuns) Append(run CodeExecution) {
	codeRunSteps.Add(1)
	n := codeExecutionSize(run)
	if len(run.Code) > MaxCodeExecutionCodeBytes || c.size+n > MaxCodeExecutionBytes {
		c.leftOut = true
		return
	}
	c.runs = append(c.runs, run)
	c.size += n
}

// LeaveOut ends the list with the one record that says runs were left out.
func (c *CodeRuns) LeaveOut() { c.leftOut = true }

// Remove takes out the last run equal to run, if the list has one. It looks from the end because
// the runs a decoder takes back are the latest ones; runs equal to each other are the same
// whichever is taken out.
func (c *CodeRuns) Remove(run CodeExecution) {
	for i := len(c.runs) - 1; i >= 0; i-- {
		codeRunSteps.Add(1)
		if c.runs[i] == run {
			c.runs = slices.Delete(c.runs, i, i+1)
			c.size -= codeExecutionSize(run)
			return
		}
	}
}

// List returns the runs, ended with the left-out record if runs were left out, or nil if there are
// none. Appending to the CodeRuns afterwards does not change a returned list.
func (c *CodeRuns) List() []CodeExecution {
	if c.leftOut {
		return append(c.runs[:len(c.runs):len(c.runs)], codeExecutionsLeftOut)
	}
	return c.runs
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
