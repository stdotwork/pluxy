package geminiwebapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// Image helpers ------------------------------------------------------------

type Image struct {
	URL   string
	Title string
	Alt   string
	Proxy string
}

func (i Image) String() string {
	short := i.URL
	if len(short) > 20 {
		short = short[:8] + "..." + short[len(short)-12:]
	}
	return fmt.Sprintf("Image(title='%s', alt='%s', url='%s')", i.Title, i.Alt, short)
}

func (i Image) Save(path string, filename string, cookies map[string]string, verbose bool, skipInvalidFilename bool, insecure bool) (string, error) {
	if filename == "" {
		// Try to parse filename from URL.
		u := i.URL
		if p := strings.Split(u, "/"); len(p) > 0 {
			filename = p[len(p)-1]
		}
		if q := strings.Split(filename, "?"); len(q) > 0 {
			filename = q[0]
		}
	}
	// Regex validation (pattern: ^(.*\.\w+)) to extract name with extension.
	if filename != "" {
		re := regexp.MustCompile(`^(.*\.\w+)`)
		if m := re.FindStringSubmatch(filename); len(m) >= 2 {
			filename = m[1]
		} else {
			if verbose {
				log.Warnf("Invalid filename: %s", filename)
			}
			if skipInvalidFilename {
				return "", nil
			}
		}
	}
	// Build client using shared helper to keep proxy/TLS behavior consistent.
	client := newHTTPClient(httpOptions{ProxyURL: i.Proxy, Insecure: insecure, FollowRedirects: true})
	client.Timeout = 120 * time.Second

	// Helper to set raw Cookie header using provided cookies (parity with the reference client behavior).
	buildCookieHeader := func(m map[string]string) string {
		if len(m) == 0 {
			return ""
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%s", k, m[k]))
		}
		return strings.Join(parts, "; ")
	}
	rawCookie := buildCookieHeader(cookies)

	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		// Ensure provided cookies are always sent across redirects (domain-agnostic).
		if rawCookie != "" {
			req.Header.Set("Cookie", rawCookie)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}

	req, _ := http.NewRequest(http.MethodGet, i.URL, nil)
	if rawCookie != "" {
		req.Header.Set("Cookie", rawCookie)
	}
	// Add browser-like headers to improve compatibility.
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/*,*/*;q=0.8")
	req.Header.Set("Connection", "keep-alive")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("error downloading image: %d %s", resp.StatusCode, resp.Status)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(strings.ToLower(ct), "image") {
		log.Warnf("Content type of %s is not image, but %s.", filename, ct)
	}
	if path == "" {
		path = "temp"
	}
	if err = os.MkdirAll(path, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(path, filename)
	f, err := os.Create(dest)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(f, resp.Body)
	_ = f.Close()
	if err != nil {
		return "", err
	}
	if verbose {
		fmt.Printf("Image saved as %s\n", dest)
	}
	abspath, _ := filepath.Abs(dest)
	return abspath, nil
}

type WebImage struct{ Image }

type GeneratedImage struct {
	Image
	Cookies map[string]string
}

func (g GeneratedImage) Save(path string, filename string, fullSize bool, verbose bool, skipInvalidFilename bool, insecure bool) (string, error) {
	if len(g.Cookies) == 0 {
		return "", &ValueError{Msg: "GeneratedImage requires cookies."}
	}
	strURL := g.URL
	if fullSize {
		strURL = strURL + "=s2048"
	}
	if filename == "" {
		name := time.Now().Format("20060102150405")
		if len(strURL) >= 10 {
			name = fmt.Sprintf("%s_%s.png", name, strURL[len(strURL)-10:])
		} else {
			name += ".png"
		}
		filename = name
	}
	tmp := g.Image
	tmp.URL = strURL
	return tmp.Save(path, filename, g.Cookies, verbose, skipInvalidFilename, insecure)
}

// Request parsing & file helpers -------------------------------------------

func ParseMessagesAndFiles(rawJSON []byte) ([]RoleText, [][]byte, []string, [][]int, error) {
	var messages []RoleText
	var files [][]byte
	var mimes []string
	var perMsgFileIdx [][]int

	contents := gjson.GetBytes(rawJSON, "contents")
	if contents.Exists() {
		contents.ForEach(func(_, content gjson.Result) bool {
			role := NormalizeRole(content.Get("role").String())
			var b strings.Builder
			startFile := len(files)
			content.Get("parts").ForEach(func(_, part gjson.Result) bool {
				if text := part.Get("text"); text.Exists() {
					if b.Len() > 0 {
						b.WriteString("\n")
					}
					b.WriteString(text.String())
				}
				if inlineData := part.Get("inlineData"); inlineData.Exists() {
					data := inlineData.Get("data").String()
					if data != "" {
						if dec, err := base64.StdEncoding.DecodeString(data); err == nil {
							files = append(files, dec)
							m := inlineData.Get("mimeType").String()
							if m == "" {
								m = inlineData.Get("mime_type").String()
							}
							mimes = append(mimes, m)
						}
					}
				}
				return true
			})
			messages = append(messages, RoleText{Role: role, Text: b.String()})
			endFile := len(files)
			if endFile > startFile {
				idxs := make([]int, 0, endFile-startFile)
				for i := startFile; i < endFile; i++ {
					idxs = append(idxs, i)
				}
				perMsgFileIdx = append(perMsgFileIdx, idxs)
			} else {
				perMsgFileIdx = append(perMsgFileIdx, nil)
			}
			return true
		})
	}
	return messages, files, mimes, perMsgFileIdx, nil
}

func MaterializeInlineFiles(files [][]byte, mimes []string) ([]string, *interfaces.ErrorMessage) {
	if len(files) == 0 {
		return nil, nil
	}
	paths := make([]string, 0, len(files))
	for i, data := range files {
		ext := MimeToExt(mimes, i)
		f, err := os.CreateTemp("", "gemini-upload-*"+ext)
		if err != nil {
			return nil, &interfaces.ErrorMessage{StatusCode: http.StatusInternalServerError, Error: fmt.Errorf("failed to create temp file: %w", err)}
		}
		if _, err = f.Write(data); err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
			return nil, &interfaces.ErrorMessage{StatusCode: http.StatusInternalServerError, Error: fmt.Errorf("failed to write temp file: %w", err)}
		}
		if err = f.Close(); err != nil {
			_ = os.Remove(f.Name())
			return nil, &interfaces.ErrorMessage{StatusCode: http.StatusInternalServerError, Error: fmt.Errorf("failed to close temp file: %w", err)}
		}
		paths = append(paths, f.Name())
	}
	return paths, nil
}

func CleanupFiles(paths []string) {
	for _, p := range paths {
		if p != "" {
			_ = os.Remove(p)
		}
	}
}

func FetchGeneratedImageData(gi GeneratedImage) (string, string, error) {
	path, err := gi.Save("", "", true, false, true, false)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = os.Remove(path) }()
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	mime := http.DetectContentType(b)
	if !strings.HasPrefix(mime, "image/") {
		if guessed := mimeFromExtension(filepath.Ext(path)); guessed != "" {
			mime = guessed
		} else {
			mime = "image/png"
		}
	}
	return mime, base64.StdEncoding.EncodeToString(b), nil
}

func MimeToExt(mimes []string, i int) string {
	if i < len(mimes) {
		return MimeToPreferredExt(strings.ToLower(mimes[i]))
	}
	return ".png"
}

var preferredExtByMIME = map[string]string{
	"image/png":       ".png",
	"image/jpeg":      ".jpg",
	"image/jpg":       ".jpg",
	"image/webp":      ".webp",
	"image/gif":       ".gif",
	"image/bmp":       ".bmp",
	"image/heic":      ".heic",
	"application/pdf": ".pdf",
}

func MimeToPreferredExt(mime string) string {
	normalized := strings.ToLower(strings.TrimSpace(mime))
	if normalized == "" {
		return ".png"
	}
	if ext, ok := preferredExtByMIME[normalized]; ok {
		return ext
	}
	return ".png"
}

func mimeFromExtension(ext string) string {
	cleaned := strings.TrimPrefix(strings.ToLower(ext), ".")
	if cleaned == "" {
		return ""
	}
	if mt, ok := misc.MimeTypes[cleaned]; ok && mt != "" {
		return mt
	}
	return ""
}

// File upload helpers ------------------------------------------------------

func uploadFile(path string, proxy string, insecure bool) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = f.Close()
	}()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return "", err
	}
	if _, err = io.Copy(fw, f); err != nil {
		return "", err
	}
	_ = mw.Close()

	client := newHTTPClient(httpOptions{ProxyURL: proxy, Insecure: insecure, FollowRedirects: true})
	client.Timeout = 300 * time.Second

	req, _ := http.NewRequest(http.MethodPost, EndpointUpload, &buf)
	applyHeaders(req, HeadersUpload)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Connection", "keep-alive")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &APIError{Msg: resp.Status}
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func parseFileName(path string) (string, error) {
	if st, err := os.Stat(path); err != nil || st.IsDir() {
		return "", &ValueError{Msg: path + " is not a valid file."}
	}
	return filepath.Base(path), nil
}

// Response formatting helpers ----------------------------------------------

var (
	reGoogle   = regexp.MustCompile("(\\()?\\[`([^`]+?)`\\]\\(https://www\\.google\\.com/search\\?q=[^)]*\\)(\\))?")
	reColonNum = regexp.MustCompile(`([^:]+:\d+)`)
	reInline   = regexp.MustCompile("`(\\[[^\\]]+\\]\\([^\\)]+\\))`")
)

func unescapeGeminiText(s string) string {
	if s == "" {
		return s
	}
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "\\<", "<")
	s = strings.ReplaceAll(s, "\\_", "_")
	s = strings.ReplaceAll(s, "\\>", ">")
	return s
}

func postProcessModelText(text string) string {
	text = reGoogle.ReplaceAllStringFunc(text, func(m string) string {
		subs := reGoogle.FindStringSubmatch(m)
		if len(subs) < 4 {
			return m
		}
		outerOpen := subs[1]
		display := subs[2]
		target := display
		if loc := reColonNum.FindString(display); loc != "" {
			target = loc
		}
		newSeg := "[`" + display + "`](" + target + ")"
		if outerOpen != "" {
			return "(" + newSeg + ")"
		}
		return newSeg
	})
	text = reInline.ReplaceAllString(text, "$1")
	return text
}

func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	rc := float64(utf8.RuneCountInString(s))
	if rc <= 0 {
		return 0
	}
	est := int(math.Ceil(rc / 4.0))
	if est < 0 {
		return 0
	}
	return est
}

// ConvertOutputToGemini converts simplified ModelOutput to Gemini API-like JSON.
// promptText is used only to estimate usage tokens to populate usage fields.
func ConvertOutputToGemini(output *ModelOutput, modelName string, promptText string) ([]byte, error) {
	if output == nil || len(output.Candidates) == 0 {
		return nil, fmt.Errorf("empty output")
	}

	parts := make([]map[string]any, 0, 2)

	var thoughtsText string
	if output.Candidates[0].Thoughts != nil {
		if t := strings.TrimSpace(*output.Candidates[0].Thoughts); t != "" {
			thoughtsText = unescapeGeminiText(t)
			parts = append(parts, map[string]any{
				"text":    thoughtsText,
				"thought": true,
			})
		}
	}

	visible := unescapeGeminiText(output.Candidates[0].Text)
	finalText := postProcessModelText(visible)
	if finalText != "" {
		parts = append(parts, map[string]any{"text": finalText})
	}

	if imgs := output.Candidates[0].GeneratedImages; len(imgs) > 0 {
		for _, gi := range imgs {
			if mime, data, err := FetchGeneratedImageData(gi); err == nil && data != "" {
				parts = append(parts, map[string]any{
					"inlineData": map[string]any{
						"mimeType": mime,
						"data":     data,
					},
				})
			}
		}
	}

	promptTokens := estimateTokens(promptText)
	completionTokens := estimateTokens(finalText)
	thoughtsTokens := 0
	if thoughtsText != "" {
		thoughtsTokens = estimateTokens(thoughtsText)
	}
	totalTokens := promptTokens + completionTokens

	now := time.Now()
	resp := map[string]any{
		"candidates": []any{
			map[string]any{
				"content": map[string]any{
					"parts": parts,
					"role":  "model",
				},
				"finishReason": "stop",
				"index":        0,
			},
		},
		"createTime":   now.Format(time.RFC3339Nano),
		"responseId":   fmt.Sprintf("gemini-web-%d", now.UnixNano()),
		"modelVersion": modelName,
		"usageMetadata": map[string]any{
			"promptTokenCount":     promptTokens,
			"candidatesTokenCount": completionTokens,
			"thoughtsTokenCount":   thoughtsTokens,
			"totalTokenCount":      totalTokens,
		},
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal gemini response: %w", err)
	}
	return ensureColonSpacing(b), nil
}

// ensureColonSpacing inserts a single space after JSON key-value colons while
// leaving string content untouched. This matches the relaxed formatting used by
// Gemini responses and keeps downstream text-processing tools compatible with
// the proxy output.
func ensureColonSpacing(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	var out bytes.Buffer
	out.Grow(len(b) + len(b)/8)
	inString := false
	escaped := false
	for i := 0; i < len(b); i++ {
		ch := b[i]
		out.WriteByte(ch)
		if escaped {
			escaped = false
			continue
		}
		switch ch {
		case '\\':
			escaped = true
		case '"':
			inString = !inString
		case ':':
			if !inString && i+1 < len(b) {
				next := b[i+1]
				if next != ' ' && next != '\n' && next != '\r' && next != '\t' {
					out.WriteByte(' ')
				}
			}
		}
	}
	return out.Bytes()
}
