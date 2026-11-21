// Package format holds what the format packages (openaichat, openairesponses,
// anthropicmessages, geminigenerate) share as one: the version of their request parsers.
package format

// ParserVersion is the version of the request parsers of the format packages: what their
// ParseRequest functions make of a client's body (its items, tools and origin, and whether
// they refuse it). Core sends a native-only generate call (protocol.Call.NativeOnly) only to an
// extension that says it parses with the same version (protocol.Info.NativeParser), so an
// extension from another release whose parsers differ gets the full request instead.
//
// Raise it whenever a parser makes something else of a body it already read: the test next to
// it (TestParserVersion) fails when a parser's output on a request it recorded (from the corpus
// and the format tests) changes, and says so.
const ParserVersion = 3
