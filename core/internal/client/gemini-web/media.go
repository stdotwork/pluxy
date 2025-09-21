package geminiwebapi

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
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
	// Regex validation (align with Python: ^(.*\.\w+)) to extract name with extension.
	if filename != "" {
		re := regexp.MustCompile(`^(.*\.\w+)`)
		if m := re.FindStringSubmatch(filename); len(m) >= 2 {
			filename = m[1]
		} else {
			if verbose {
				Warning("Invalid filename: %s", filename)
			}
			if skipInvalidFilename {
				return "", nil
			}
		}
	}
	// Build client with cookie jar so cookies persist across redirects.
	tr := &http.Transport{}
	if i.Proxy != "" {
		if pu, err := url.Parse(i.Proxy); err == nil {
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Transport: tr, Timeout: 120 * time.Second, Jar: jar}

	// Helper to set raw Cookie header using provided cookies (to mirror Python client behavior).
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
		return "", fmt.Errorf("Error downloading image: %d %s", resp.StatusCode, resp.Status)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(strings.ToLower(ct), "image") {
		Warning("Content type of %s is not image, but %s.", filename, ct)
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
		Info("Image saved as %s", dest)
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

	tr := &http.Transport{}
	if proxy != "" {
		if pu, errParse := url.Parse(proxy); errParse == nil {
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	client := &http.Client{Transport: tr, Timeout: 300 * time.Second}

	req, _ := http.NewRequest(http.MethodPost, EndpointUpload, &buf)
	for k, v := range HeadersUpload {
		for _, vv := range v {
			req.Header.Add(k, vv)
		}
	}
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
