// Package code provides request translation functionality for Claude API.
// It handles parsing and transforming Claude API requests into the internal client format,
// extracting model information, system instructions, message contents, and tool declarations.
// The package also performs JSON data cleaning and transformation to ensure compatibility
// between Claude API format and the internal client's expected format.
package code

import (
	"crypto/rand"
	"math/big"
	"strings"

	"github.com/luispater/CLIProxyAPI/internal/misc"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// PrepareClaudeRequest parses and transforms a Claude API request into internal client format.
// It extracts the model name, system instruction, message contents, and tool declarations
// from the raw JSON request and returns them in the format expected by the internal client.
func ConvertGeminiRequestToCodex(rawJSON []byte) string {
	// Base template
	out := `{"model":"","instructions":"","input":[]}`

	// Inject standard Codex instructions
	instructions := misc.CodexInstructions
	out, _ = sjson.SetRaw(out, "instructions", instructions)

	root := gjson.ParseBytes(rawJSON)

	// helper for generating paired call IDs in the form: call_<alphanum>
	// Gemini uses sequential pairing across possibly multiple in-flight
	// functionCalls, so we keep a FIFO queue of generated call IDs and
	// consume them in order when functionResponses arrive.
	var pendingCallIDs []string

	// genCallID creates a random call id like: call_<8chars>
	genCallID := func() string {
		const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		var b strings.Builder
		// 8 chars random suffix
		for i := 0; i < 24; i++ {
			n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
			b.WriteByte(letters[n.Int64()])
		}
		return "call_" + b.String()
	}

	// Model
	if v := root.Get("model"); v.Exists() {
		out, _ = sjson.Set(out, "model", v.Value())
	}

	// System instruction -> as a user message with input_text parts
	sysParts := root.Get("system_instruction.parts")
	if sysParts.IsArray() {
		msg := `{"type":"message","role":"user","content":[]}`
		arr := sysParts.Array()
		for i := 0; i < len(arr); i++ {
			p := arr[i]
			if t := p.Get("text"); t.Exists() {
				part := `{}`
				part, _ = sjson.Set(part, "type", "input_text")
				part, _ = sjson.Set(part, "text", t.String())
				msg, _ = sjson.SetRaw(msg, "content.-1", part)
			}
		}
		if len(gjson.Get(msg, "content").Array()) > 0 {
			out, _ = sjson.SetRaw(out, "input.-1", msg)
		}
	}

	// Contents -> messages and function calls/results
	contents := root.Get("contents")
	if contents.IsArray() {
		items := contents.Array()
		for i := 0; i < len(items); i++ {
			item := items[i]
			role := item.Get("role").String()
			if role == "model" {
				role = "assistant"
			}

			parts := item.Get("parts")
			if !parts.IsArray() {
				continue
			}
			parr := parts.Array()
			for j := 0; j < len(parr); j++ {
				p := parr[j]
				// text part
				if t := p.Get("text"); t.Exists() {
					msg := `{"type":"message","role":"","content":[]}`
					msg, _ = sjson.Set(msg, "role", role)
					partType := "input_text"
					if role == "assistant" {
						partType = "output_text"
					}
					part := `{}`
					part, _ = sjson.Set(part, "type", partType)
					part, _ = sjson.Set(part, "text", t.String())
					msg, _ = sjson.SetRaw(msg, "content.-1", part)
					out, _ = sjson.SetRaw(out, "input.-1", msg)
					continue
				}

				// function call from model
				if fc := p.Get("functionCall"); fc.Exists() {
					fn := `{"type":"function_call"}`
					if name := fc.Get("name"); name.Exists() {
						fn, _ = sjson.Set(fn, "name", name.String())
					}
					if args := fc.Get("args"); args.Exists() {
						fn, _ = sjson.Set(fn, "arguments", args.Raw)
					}
					// generate a paired random call_id and enqueue it so the
					// corresponding functionResponse can pop the earliest id
					// to preserve ordering when multiple calls are present.
					id := genCallID()
					fn, _ = sjson.Set(fn, "call_id", id)
					pendingCallIDs = append(pendingCallIDs, id)
					out, _ = sjson.SetRaw(out, "input.-1", fn)
					continue
				}

				// function response from user
				if fr := p.Get("functionResponse"); fr.Exists() {
					fno := `{"type":"function_call_output"}`
					// Prefer a string result if present; otherwise embed the raw response as a string
					if res := fr.Get("response.result"); res.Exists() {
						fno, _ = sjson.Set(fno, "output", res.String())
					} else if resp := fr.Get("response"); resp.Exists() {
						fno, _ = sjson.Set(fno, "output", resp.Raw)
					}
					// fno, _ = sjson.Set(fno, "call_id", "call_W6nRJzFXyPM2LFBbfo98qAbq")
					// attach the oldest queued call_id to pair the response
					// with its call. If the queue is empty, generate a new id.
					var id string
					if len(pendingCallIDs) > 0 {
						id = pendingCallIDs[0]
						// pop the first element
						pendingCallIDs = pendingCallIDs[1:]
					} else {
						id = genCallID()
					}
					fno, _ = sjson.Set(fno, "call_id", id)
					out, _ = sjson.SetRaw(out, "input.-1", fno)
					continue
				}
			}
		}
	}

	// Tools mapping: Gemini functionDeclarations -> Codex tools
	tools := root.Get("tools")
	if tools.IsArray() {
		out, _ = sjson.SetRaw(out, "tools", `[]`)
		out, _ = sjson.Set(out, "tool_choice", "auto")
		tarr := tools.Array()
		for i := 0; i < len(tarr); i++ {
			td := tarr[i]
			fns := td.Get("functionDeclarations")
			if !fns.IsArray() {
				continue
			}
			farr := fns.Array()
			for j := 0; j < len(farr); j++ {
				fn := farr[j]
				tool := `{}`
				tool, _ = sjson.Set(tool, "type", "function")
				if v := fn.Get("name"); v.Exists() {
					tool, _ = sjson.Set(tool, "name", v.String())
				}
				if v := fn.Get("description"); v.Exists() {
					tool, _ = sjson.Set(tool, "description", v.String())
				}
				if prm := fn.Get("parameters"); prm.Exists() {
					// Remove optional $schema field if present
					cleaned := prm.Raw
					cleaned, _ = sjson.Delete(cleaned, "$schema")
					cleaned, _ = sjson.Set(cleaned, "additionalProperties", false)
					tool, _ = sjson.SetRaw(tool, "parameters", cleaned)
				}
				tool, _ = sjson.Set(tool, "strict", false)
				out, _ = sjson.SetRaw(out, "tools.-1", tool)
			}
		}
	}

	// Fixed flags aligning with Codex expectations
	out, _ = sjson.Set(out, "parallel_tool_calls", true)
	out, _ = sjson.Set(out, "reasoning.effort", "low")
	out, _ = sjson.Set(out, "reasoning.summary", "auto")
	out, _ = sjson.Set(out, "stream", true)
	out, _ = sjson.Set(out, "store", false)
	out, _ = sjson.Set(out, "include", []string{"reasoning.encrypted_content"})

	return out
}
