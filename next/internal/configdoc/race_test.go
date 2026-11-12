//go:build race

package configdoc

// raceDetector reports whether the tests run with the race detector, which slows code down 2 to 20
// times.
const raceDetector = true
