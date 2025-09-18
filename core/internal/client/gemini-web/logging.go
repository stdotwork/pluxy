package geminiwebapi

import (
	"fmt"
	"os"
	"strings"

	log "github.com/sirupsen/logrus"
)

// init honors GEMINI_WEBAPI_LOG to keep parity with the Python client.
func init() {
	if lvl := os.Getenv("GEMINI_WEBAPI_LOG"); lvl != "" {
		SetLogLevel(lvl)
	}
}

// SetLogLevel adjusts logging verbosity using CLI-style strings.
func SetLogLevel(level string) {
	switch strings.ToUpper(level) {
	case "TRACE":
		log.SetLevel(log.TraceLevel)
	case "DEBUG":
		log.SetLevel(log.DebugLevel)
	case "INFO":
		log.SetLevel(log.InfoLevel)
	case "WARNING", "WARN":
		log.SetLevel(log.WarnLevel)
	case "ERROR":
		log.SetLevel(log.ErrorLevel)
	case "CRITICAL", "FATAL":
		log.SetLevel(log.FatalLevel)
	default:
		log.SetLevel(log.InfoLevel)
	}
}

func prefix(format string) string { return "[gemini_webapi] " + format }

func Debug(format string, v ...any) { log.Debugf(prefix(format), v...) }

// DebugRaw logs without the module prefix; use sparingly for messages
// that should integrate with global formatting without extra tags.
func DebugRaw(format string, v ...any) { log.Debugf(format, v...) }
func Info(format string, v ...any)     { log.Infof(prefix(format), v...) }
func Warning(format string, v ...any)  { log.Warnf(prefix(format), v...) }
func Error(format string, v ...any)    { log.Errorf(prefix(format), v...) }
func Success(format string, v ...any)  { log.Infof(prefix("SUCCESS "+format), v...) }

// MaskToken hides the middle part of a sensitive value with '*'.
// It keeps up to left and right edge characters for readability.
// If input is very short, it returns a fully masked string of the same length.
func MaskToken(s string) string {
	n := len(s)
	if n == 0 {
		return ""
	}
	if n <= 6 {
		return strings.Repeat("*", n)
	}
	// Keep up to 6 chars on the left and 4 on the right, but never exceed available length
	left := 6
	if left > n-4 {
		left = n - 4
	}
	right := 4
	if right > n-left {
		right = n - left
	}
	if left < 0 {
		left = 0
	}
	if right < 0 {
		right = 0
	}
	middle := n - left - right
	if middle < 0 {
		middle = 0
	}
	return s[:left] + strings.Repeat("*", middle) + s[n-right:]
}

// MaskToken28 returns a fixed-length (28) masked representation showing:
// first 8 chars + 8 asterisks + 4 middle chars + last 8 chars.
// If the input is shorter than 20 characters, it returns a fully masked string
// of length min(len(s), 28).
func MaskToken28(s string) string {
	n := len(s)
	if n == 0 {
		return ""
	}
	if n < 20 {
		// Too short to safely reveal; mask entirely but cap to 28
		if n > 28 {
			n = 28
		}
		return strings.Repeat("*", n)
	}
	// Pick 4 middle characters around the center
	midStart := n/2 - 2
	if midStart < 8 {
		midStart = 8
	}
	if midStart+4 > n-8 {
		midStart = n - 8 - 4
		if midStart < 8 {
			midStart = 8
		}
	}
	prefix := s[:8]
	middle := s[midStart : midStart+4]
	suffix := s[n-8:]
	return prefix + strings.Repeat("*", 4) + middle + strings.Repeat("*", 4) + suffix
}

// BuildUpstreamRequestLog builds a compact preview string for upstream request logging.
func BuildUpstreamRequestLog(account string, contextOn bool, useTags, explicitContext bool, prompt string, filesCount int, reuse bool, metaLen int, gem *Gem) string {
	var sb strings.Builder
	sb.WriteString("\n\n=== GEMINI WEB UPSTREAM ===\n")
	sb.WriteString(fmt.Sprintf("account: %s\n", account))
	if contextOn {
		sb.WriteString("context_mode: on\n")
	} else {
		sb.WriteString("context_mode: off\n")
	}
	if reuse {
		sb.WriteString("reuseIdx: 1\n")
	} else {
		sb.WriteString("reuseIdx: 0\n")
	}
	sb.WriteString(fmt.Sprintf("useTags: %t\n", useTags))
	sb.WriteString(fmt.Sprintf("metadata_len: %d\n", metaLen))
	if explicitContext {
		sb.WriteString("explicit_context: true\n")
	} else {
		sb.WriteString("explicit_context: false\n")
	}
	if filesCount > 0 {
		sb.WriteString(fmt.Sprintf("files: %d\n", filesCount))
	}

	if gem != nil {
		sb.WriteString("gem:\n")
		if gem.ID != "" {
			sb.WriteString(fmt.Sprintf("  id: %s\n", gem.ID))
		}
		if gem.Name != "" {
			sb.WriteString(fmt.Sprintf("  name: %s\n", gem.Name))
		}
		sb.WriteString(fmt.Sprintf("  predefined: %t\n", gem.Predefined))
	} else {
		sb.WriteString("gem: none\n")
	}

	chunks := ChunkByRunes(prompt, 4096)
	preview := prompt
	truncated := false
	if len(chunks) > 1 {
		preview = chunks[0]
		truncated = true
	}
	sb.WriteString("prompt_preview:\n")
	sb.WriteString(preview)
	if truncated {
		sb.WriteString("\n... [truncated]\n")
	}
	return sb.String()
}
