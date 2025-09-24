package geminiwebapi

import (
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

type httpOptions struct {
	ProxyURL        string
	Insecure        bool
	FollowRedirects bool
}

func newHTTPClient(opts httpOptions) *http.Client {
	transport := &http.Transport{}
	if opts.ProxyURL != "" {
		if pu, err := url.Parse(opts.ProxyURL); err == nil {
			transport.Proxy = http.ProxyURL(pu)
		}
	}
	if opts.Insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Transport: transport, Timeout: 60 * time.Second, Jar: jar}
	if !opts.FollowRedirects {
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}
	return client
}

func applyHeaders(req *http.Request, headers http.Header) {
	for k, v := range headers {
		for _, vv := range v {
			req.Header.Add(k, vv)
		}
	}
}

func applyCookies(req *http.Request, cookies map[string]string) {
	for k, v := range cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
}

func sendInitRequest(cookies map[string]string, proxy string, insecure bool) (*http.Response, map[string]string, error) {
	client := newHTTPClient(httpOptions{ProxyURL: proxy, Insecure: insecure, FollowRedirects: true})
	req, _ := http.NewRequest(http.MethodGet, EndpointInit, nil)
	applyHeaders(req, HeadersGemini)
	applyCookies(req, cookies)
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp, nil, &AuthError{Msg: resp.Status}
	}
	outCookies := map[string]string{}
	for _, c := range resp.Cookies() {
		outCookies[c.Name] = c.Value
	}
	for k, v := range cookies {
		outCookies[k] = v
	}
	return resp, outCookies, nil
}

func getAccessToken(baseCookies map[string]string, proxy string, verbose bool, insecure bool) (string, map[string]string, error) {
	// Warm-up google.com to gain extra cookies (NID, etc.) and capture them.
	extraCookies := map[string]string{}
	{
		client := newHTTPClient(httpOptions{ProxyURL: proxy, Insecure: insecure, FollowRedirects: true})
		req, _ := http.NewRequest(http.MethodGet, EndpointGoogle, nil)
		resp, _ := client.Do(req)
		if resp != nil {
			if u, err := url.Parse(EndpointGoogle); err == nil {
				for _, c := range client.Jar.Cookies(u) {
					extraCookies[c.Name] = c.Value
				}
			}
			_ = resp.Body.Close()
		}
	}

	trySets := make([]map[string]string, 0, 8)

	if v1, ok1 := baseCookies["__Secure-1PSID"]; ok1 {
		if v2, ok2 := baseCookies["__Secure-1PSIDTS"]; ok2 {
			merged := map[string]string{"__Secure-1PSID": v1, "__Secure-1PSIDTS": v2}
			if nid, ok := baseCookies["NID"]; ok {
				merged["NID"] = nid
			}
			trySets = append(trySets, merged)
		} else if verbose {
			log.Debug("Skipping base cookies: __Secure-1PSIDTS missing")
		}
	}

	cacheDir := "temp"
	_ = os.MkdirAll(cacheDir, 0o755)
	if v1, ok1 := baseCookies["__Secure-1PSID"]; ok1 {
		cacheFile := filepath.Join(cacheDir, ".cached_1psidts_"+v1+".txt")
		if b, err := os.ReadFile(cacheFile); err == nil {
			cv := strings.TrimSpace(string(b))
			if cv != "" {
				merged := map[string]string{"__Secure-1PSID": v1, "__Secure-1PSIDTS": cv}
				trySets = append(trySets, merged)
			}
		}
	}

	if len(extraCookies) > 0 {
		trySets = append(trySets, extraCookies)
	}

	reToken := regexp.MustCompile(`"SNlM0e":"([^"]+)"`)

	for _, cookies := range trySets {
		resp, mergedCookies, err := sendInitRequest(cookies, proxy, insecure)
		if err != nil {
			if verbose {
				log.Warnf("Failed init request: %v", err)
			}
			continue
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return "", nil, err
		}
		matches := reToken.FindStringSubmatch(string(body))
		if len(matches) >= 2 {
			token := matches[1]
			if verbose {
				log.Infof("Gemini access token acquired.")
			}
			return token, mergedCookies, nil
		}
	}
	return "", nil, &AuthError{Msg: "Failed to retrieve token."}
}

// rotate1PSIDTS refreshes __Secure-1PSIDTS
func rotate1PSIDTS(cookies map[string]string, proxy string, insecure bool) (string, error) {
	_, ok := cookies["__Secure-1PSID"]
	if !ok {
		return "", &AuthError{Msg: "__Secure-1PSID missing"}
	}

	tr := &http.Transport{}
	if proxy != "" {
		if pu, err := url.Parse(proxy); err == nil {
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	client := &http.Client{Transport: tr, Timeout: 60 * time.Second}

	req, _ := http.NewRequest(http.MethodPost, EndpointRotateCookies, io.NopCloser(stringsReader("[000,\"-0000000000000000000\"]")))
	applyHeaders(req, HeadersRotateCookies)
	applyCookies(req, cookies)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == http.StatusUnauthorized {
		return "", &AuthError{Msg: "unauthorized"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", errors.New(resp.Status)
	}

	for _, c := range resp.Cookies() {
		if c.Name == "__Secure-1PSIDTS" {
			return c.Value, nil
		}
	}
	return "", nil
}

// Minimal reader helpers to avoid importing strings everywhere.
type constReader struct {
	s string
	i int
}

func (r *constReader) Read(p []byte) (int, error) {
	if r.i >= len(r.s) {
		return 0, io.EOF
	}
	n := copy(p, r.s[r.i:])
	r.i += n
	return n, nil
}

func stringsReader(s string) io.Reader { return &constReader{s: s} }

func MaskToken28(s string) string {
	n := len(s)
	if n == 0 {
		return ""
	}
	if n < 20 {
		return strings.Repeat("*", n)
	}
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
	prefixByte := s[:8]
	middle := s[midStart : midStart+4]
	suffix := s[n-8:]
	return prefixByte + strings.Repeat("*", 4) + middle + strings.Repeat("*", 4) + suffix
}
