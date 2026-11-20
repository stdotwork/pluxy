// Package privatetest reads back, for Windows tests, what the file system holds for a path made
// private (internal/private): its owner and access list. It declares its own calls rather than
// taking them from internal/private, so a test does not rest on the code it checks. It has
// nothing for other systems, where a test reads a file's mode.
package privatetest
