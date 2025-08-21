// Package misc provides miscellaneous utility functions and embedded data for the CLI Proxy API.
// This package contains general-purpose helpers and embedded resources that do not fit into
// more specific domain packages. It includes embedded instructional text for Codex-related operations.
package misc

import _ "embed"

// CodexInstructions holds the content of the codex_instructions.txt file,
// which is embedded into the application binary at compile time. This variable
// contains instructional text used for Codex-related operations and model guidance.
//
//go:embed codex_instructions.txt
var CodexInstructions string
