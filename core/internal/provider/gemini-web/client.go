package geminiwebapi

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// GeminiClient is the async http client interface (Go port)
type GeminiClient struct {
	Cookies     map[string]string
	Proxy       string
	Running     bool
	httpClient  *http.Client
	AccessToken string
	Timeout     time.Duration
	insecure    bool
}

// HTTP bootstrap utilities -------------------------------------------------
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
	extraCookies := map[string]string{}
	{
		client := newHTTPClient(httpOptions{ProxyURL: proxy, Insecure: insecure, FollowRedirects: true})
		req, _ := http.NewRequest(http.MethodGet, EndpointGoogle, nil)
		resp, err := client.Do(req)
		if err != nil {
			if verbose {
				log.Debugf("priming google cookies failed: %v", err)
			}
		} else if resp != nil {
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
				fmt.Println("Gemini access token acquired.")
			}
			return token, mergedCookies, nil
		}
	}
	return "", nil, &AuthError{Msg: "Failed to retrieve token."}
}

func rotate1PSIDTS(cookies map[string]string, proxy string, insecure bool) (string, error) {
	_, ok := cookies["__Secure-1PSID"]
	if !ok {
		return "", &AuthError{Msg: "__Secure-1PSID missing"}
	}

	// Reuse shared HTTP client helper for consistency.
	client := newHTTPClient(httpOptions{ProxyURL: proxy, Insecure: insecure, FollowRedirects: true})

	req, _ := http.NewRequest(http.MethodPost, EndpointRotateCookies, strings.NewReader("[000,\"-0000000000000000000\"]"))
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
	// Fallback: check cookie jar in case the Set-Cookie was on a redirect hop
	if u, err := url.Parse(EndpointRotateCookies); err == nil && client.Jar != nil {
		for _, c := range client.Jar.Cookies(u) {
			if c.Name == "__Secure-1PSIDTS" && c.Value != "" {
				return c.Value, nil
			}
		}
	}
	return "", nil
}

// MaskToken28 masks a sensitive token for safe logging. Keep middle partially visible.
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

var NanoBananaModel = map[string]struct{}{
	"gemini-2.5-flash-image-web": {},
}

// NewGeminiClient creates a client. Pass empty strings to auto-detect via browser cookies (not implemented in Go port).
func NewGeminiClient(secure1psid string, secure1psidts string, proxy string, opts ...func(*GeminiClient)) *GeminiClient {
	c := &GeminiClient{
		Cookies:  map[string]string{},
		Proxy:    proxy,
		Running:  false,
		Timeout:  300 * time.Second,
		insecure: false,
	}
	if secure1psid != "" {
		c.Cookies["__Secure-1PSID"] = secure1psid
		if secure1psidts != "" {
			c.Cookies["__Secure-1PSIDTS"] = secure1psidts
		}
	}
	for _, f := range opts {
		f(c)
	}
	return c
}

// WithInsecureTLS sets skipping TLS verification (to mirror httpx verify=False)
func WithInsecureTLS(insecure bool) func(*GeminiClient) {
	return func(c *GeminiClient) { c.insecure = insecure }
}

// Init initializes the access token and http client.
func (c *GeminiClient) Init(timeoutSec float64, verbose bool) error {
	// get access token
	token, validCookies, err := getAccessToken(c.Cookies, c.Proxy, verbose, c.insecure)
	if err != nil {
		c.Close(0)
		return err
	}
	c.AccessToken = token
	c.Cookies = validCookies

	tr := &http.Transport{}
	if c.Proxy != "" {
		if pu, errParse := url.Parse(c.Proxy); errParse == nil {
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	if c.insecure {
		// set via roundtripper in utils_get_access_token for token; here we reuse via default Transport
		// intentionally not adding here, as requests rely on endpoints with normal TLS
	}
	c.httpClient = &http.Client{Transport: tr, Timeout: time.Duration(timeoutSec * float64(time.Second))}
	c.Running = true

	c.Timeout = time.Duration(timeoutSec * float64(time.Second))
	if verbose {
		fmt.Println("Gemini client initialized successfully.")
	}
	return nil
}

func (c *GeminiClient) Close(delaySec float64) {
	if delaySec > 0 {
		time.Sleep(time.Duration(delaySec * float64(time.Second)))
	}
	c.Running = false
}

// ensureRunning mirrors the decorator behavior and retries on APIError.
func (c *GeminiClient) ensureRunning() error {
	if c.Running {
		return nil
	}
	return c.Init(float64(c.Timeout/time.Second), false)
}

// RotateTS performs a RotateCookies request and returns the new __Secure-1PSIDTS value (if any).
func (c *GeminiClient) RotateTS() (string, error) {
	if c == nil {
		return "", fmt.Errorf("gemini web client is nil")
	}
	return rotate1PSIDTS(c.Cookies, c.Proxy, c.insecure)
}

// GenerateContent sends a prompt (with optional files) and parses the response into ModelOutput.
func (c *GeminiClient) GenerateContent(prompt string, files []string, model Model, gem *Gem, chat *ChatSession) (ModelOutput, error) {
	var empty ModelOutput
	if prompt == "" {
		return empty, &ValueError{Msg: "Prompt cannot be empty."}
	}
	if err := c.ensureRunning(); err != nil {
		return empty, err
	}

	// Retry wrapper similar to decorator (retry=2)
	retries := 2
	for {
		out, err := c.generateOnce(prompt, files, model, gem, chat)
		if err == nil {
			return out, nil
		}
		var apiErr *APIError
		var imgErr *ImageGenerationError
		shouldRetry := false
		if errors.As(err, &imgErr) {
			if retries > 1 {
				retries = 1
			} // only once for image generation
			shouldRetry = true
		} else if errors.As(err, &apiErr) {
			shouldRetry = true
		}
		if shouldRetry && retries > 0 {
			time.Sleep(time.Second)
			retries--
			continue
		}
		return empty, err
	}
}

func ensureAnyLen(slice []any, index int) []any {
	if index < len(slice) {
		return slice
	}
	gap := index + 1 - len(slice)
	return append(slice, make([]any, gap)...)
}

func (c *GeminiClient) generateOnce(prompt string, files []string, model Model, gem *Gem, chat *ChatSession) (ModelOutput, error) {
	var empty ModelOutput
	// Build f.req
	var uploaded [][]any
	for _, fp := range files {
		id, err := uploadFile(fp, c.Proxy, c.insecure)
		if err != nil {
			return empty, err
		}
		name, err := parseFileName(fp)
		if err != nil {
			return empty, err
		}
		uploaded = append(uploaded, []any{[]any{id}, name})
	}
	var item0 any
	if len(uploaded) > 0 {
		item0 = []any{prompt, 0, nil, uploaded}
	} else {
		item0 = []any{prompt}
	}
	var item2 any = nil
	if chat != nil {
		item2 = chat.Metadata()
	}

	inner := []any{item0, nil, item2}
	// Attach Gem first to keep index alignment with reference implementation
	// so the Gemini Web UI can recognize the selected Gem.
	if gem != nil {
		// pad with 16 nils then gem ID
		for i := 0; i < 16; i++ {
			inner = append(inner, nil)
		}
		inner = append(inner, gem.ID)
	}
	requestedModel := strings.ToLower(model.Name)
	if chat != nil && chat.RequestedModel() != "" {
		requestedModel = chat.RequestedModel()
	}
	if _, ok := NanoBananaModel[requestedModel]; ok {
		inner = ensureAnyLen(inner, 49)
		inner[49] = 14
	}
	innerJSON, _ := json.Marshal(inner)
	outer := []any{nil, string(innerJSON)}
	outerJSON, _ := json.Marshal(outer)

	// form
	form := url.Values{}
	form.Set("at", c.AccessToken)
	form.Set("f.req", string(outerJSON))

	req, _ := http.NewRequest(http.MethodPost, EndpointGenerate, strings.NewReader(form.Encode()))
	applyHeaders(req, HeadersGemini)
	applyHeaders(req, model.ModelHeader)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=utf-8")
	applyCookies(req, c.Cookies)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return empty, &TimeoutError{GeminiError{Msg: "Generate content request timed out."}}
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode == 429 {
		// Surface 429 as TemporarilyBlocked to match reference behavior
		c.Close(0)
		return empty, &TemporarilyBlocked{GeminiError{Msg: "Too many requests. IP temporarily blocked."}}
	}
	if resp.StatusCode != 200 {
		c.Close(0)
		return empty, &APIError{Msg: fmt.Sprintf("Failed to generate contents. Status %d", resp.StatusCode)}
	}

	// Read body and split lines; take the 3rd line (index 2)
	b, _ := io.ReadAll(resp.Body)
	parts := strings.Split(string(b), "\n")
	if len(parts) < 3 {
		c.Close(0)
		return empty, &APIError{Msg: "Invalid response data received."}
	}
	var responseJSON []any
	if err = json.Unmarshal([]byte(parts[2]), &responseJSON); err != nil {
		c.Close(0)
		return empty, &APIError{Msg: "Invalid response data received."}
	}

	// find body where main_part[4] exists
	var (
		body      any
		bodyIndex int
	)
	for i, p := range responseJSON {
		arr, ok := p.([]any)
		if !ok || len(arr) < 3 {
			continue
		}
		s, ok := arr[2].(string)
		if !ok {
			continue
		}
		var mainPart []any
		if err = json.Unmarshal([]byte(s), &mainPart); err != nil {
			continue
		}
		if len(mainPart) > 4 && mainPart[4] != nil {
			body = mainPart
			bodyIndex = i
			break
		}
	}
	if body == nil {
		// Fallback: scan subsequent lines to locate a data frame with a non-empty body (mainPart[4]).
		var lastTop []any
		for li := 3; li < len(parts) && body == nil; li++ {
			line := strings.TrimSpace(parts[li])
			if line == "" {
				continue
			}
			var top []any
			if err = json.Unmarshal([]byte(line), &top); err != nil {
				continue
			}
			lastTop = top
			for i, p := range top {
				arr, ok := p.([]any)
				if !ok || len(arr) < 3 {
					continue
				}
				s, ok := arr[2].(string)
				if !ok {
					continue
				}
				var mainPart []any
				if err = json.Unmarshal([]byte(s), &mainPart); err != nil {
					continue
				}
				if len(mainPart) > 4 && mainPart[4] != nil {
					body = mainPart
					bodyIndex = i
					responseJSON = top
					break
				}
			}
		}
		// Parse nested error code to align with error mapping
		var top []any
		// Prefer lastTop from fallback scan; otherwise try parts[2]
		if len(lastTop) > 0 {
			top = lastTop
		} else {
			_ = json.Unmarshal([]byte(parts[2]), &top)
		}
		if len(top) > 0 {
			if code, ok := extractErrorCode(top); ok {
				switch code {
				case ErrorUsageLimitExceeded:
					return empty, &UsageLimitExceeded{GeminiError{Msg: fmt.Sprintf("Failed to generate contents. Usage limit of %s has exceeded. Please try switching to another model.", model.Name)}}
				case ErrorModelInconsistent:
					return empty, &ModelInvalid{GeminiError{Msg: "Selected model is inconsistent or unavailable."}}
				case ErrorModelHeaderInvalid:
					return empty, &APIError{Msg: "Invalid model header string. Please update the selected model header."}
				case ErrorIPTemporarilyBlocked:
					return empty, &TemporarilyBlocked{GeminiError{Msg: "Too many requests. IP temporarily blocked."}}
				}
			}
		}
		// Debug("Invalid response: control frames only; no body found")
		// Close the client to force re-initialization on next request (parity with reference client behavior)
		c.Close(0)
		return empty, &APIError{Msg: "Failed to generate contents. Invalid response data received."}
	}

	bodyArr := body.([]any)
	// metadata
	var metadata []string
	if len(bodyArr) > 1 {
		if metaArr, ok := bodyArr[1].([]any); ok {
			for _, v := range metaArr {
				if s, isOk := v.(string); isOk {
					metadata = append(metadata, s)
				}
			}
		}
	}

	// candidates parsing
	candContainer, ok := bodyArr[4].([]any)
	if !ok {
		return empty, &APIError{Msg: "Failed to parse response body."}
	}
	candidates := make([]Candidate, 0, len(candContainer))
	reCard := regexp.MustCompile(`^http://googleusercontent\.com/card_content/\d+`)
	reGen := regexp.MustCompile(`http://googleusercontent\.com/image_generation_content/\d+`)

	for ci, candAny := range candContainer {
		cArr, isOk := candAny.([]any)
		if !isOk {
			continue
		}
		// text: cArr[1][0]
		var text string
		if len(cArr) > 1 {
			if sArr, isOk1 := cArr[1].([]any); isOk1 && len(sArr) > 0 {
				text, _ = sArr[0].(string)
			}
		}
		if reCard.MatchString(text) {
			// candidate[22] and candidate[22][0] or text
			if len(cArr) > 22 {
				if arr, isOk1 := cArr[22].([]any); isOk1 && len(arr) > 0 {
					if s, isOk2 := arr[0].(string); isOk2 {
						text = s
					}
				}
			}
		}

		// thoughts: candidate[37][0][0]
		var thoughts *string
		if len(cArr) > 37 {
			if a, ok1 := cArr[37].([]any); ok1 && len(a) > 0 {
				if b1, ok2 := a[0].([]any); ok2 && len(b1) > 0 {
					if s, ok3 := b1[0].(string); ok3 {
						ss := decodeHTML(s)
						thoughts = &ss
					}
				}
			}
		}

		// web images: candidate[12][1]
		var webImages []WebImage
		var imgSection any
		if len(cArr) > 12 {
			imgSection = cArr[12]
		}
		if arr, ok1 := imgSection.([]any); ok1 && len(arr) > 1 {
			if imagesArr, ok2 := arr[1].([]any); ok2 {
				for _, wiAny := range imagesArr {
					wiArr, ok3 := wiAny.([]any)
					if !ok3 {
						continue
					}
					// url: wiArr[0][0][0], title: wiArr[7][0], alt: wiArr[0][4]
					var urlStr, title, alt string
					if len(wiArr) > 0 {
						if a, ok5 := wiArr[0].([]any); ok5 && len(a) > 0 {
							if b1, ok6 := a[0].([]any); ok6 && len(b1) > 0 {
								urlStr, _ = b1[0].(string)
							}
							if len(a) > 4 {
								if s, ok6 := a[4].(string); ok6 {
									alt = s
								}
							}
						}
					}
					if len(wiArr) > 7 {
						if a, ok4 := wiArr[7].([]any); ok4 && len(a) > 0 {
							title, _ = a[0].(string)
						}
					}
					webImages = append(webImages, WebImage{Image: Image{URL: urlStr, Title: title, Alt: alt, Proxy: c.Proxy}})
				}
			}
		}

		// generated images
		var genImages []GeneratedImage
		hasGen := false
		if arr, ok1 := imgSection.([]any); ok1 && len(arr) > 7 {
			if a, ok2 := arr[7].([]any); ok2 && len(a) > 0 && a[0] != nil {
				hasGen = true
			}
		}
		if hasGen {
			// find img part
			var imgBody []any
			for pi := bodyIndex; pi < len(responseJSON); pi++ {
				part := responseJSON[pi]
				arr, ok1 := part.([]any)
				if !ok1 || len(arr) < 3 {
					continue
				}
				s, ok1 := arr[2].(string)
				if !ok1 {
					continue
				}
				var mp []any
				if err = json.Unmarshal([]byte(s), &mp); err != nil {
					continue
				}
				if len(mp) > 4 {
					if tt, ok2 := mp[4].([]any); ok2 && len(tt) > ci {
						if sec, ok3 := tt[ci].([]any); ok3 && len(sec) > 12 {
							if ss, ok4 := sec[12].([]any); ok4 && len(ss) > 7 {
								if first, ok5 := ss[7].([]any); ok5 && len(first) > 0 && first[0] != nil {
									imgBody = mp
									break
								}
							}
						}
					}
				}
			}
			if imgBody == nil {
				return empty, &ImageGenerationError{APIError{Msg: "Failed to parse generated images."}}
			}
			imgCand := imgBody[4].([]any)[ci].([]any)
			if len(imgCand) > 1 {
				if a, ok1 := imgCand[1].([]any); ok1 && len(a) > 0 {
					if s, ok2 := a[0].(string); ok2 {
						text = strings.TrimSpace(reGen.ReplaceAllString(s, ""))
					}
				}
			}
			// images list at imgCand[12][7][0]
			if len(imgCand) > 12 {
				if s1, ok1 := imgCand[12].([]any); ok1 && len(s1) > 7 {
					if s2, ok2 := s1[7].([]any); ok2 && len(s2) > 0 {
						if s3, ok3 := s2[0].([]any); ok3 {
							for ii, giAny := range s3 {
								ga, ok4 := giAny.([]any)
								if !ok4 || len(ga) < 4 {
									continue
								}
								// url: ga[0][3][3]
								var urlStr, title, alt string
								if a, ok5 := ga[0].([]any); ok5 && len(a) > 3 {
									if b1, ok6 := a[3].([]any); ok6 && len(b1) > 3 {
										urlStr, _ = b1[3].(string)
									}
								}
								// title from ga[3][6]
								if len(ga) > 3 {
									if a, ok5 := ga[3].([]any); ok5 {
										if len(a) > 6 {
											if v, ok6 := a[6].(float64); ok6 && v != 0 {
												title = fmt.Sprintf("[Generated Image %.0f]", v)
											} else {
												title = "[Generated Image]"
											}
										} else {
											title = "[Generated Image]"
										}
										// alt from ga[3][5][ii] fallback
										if len(a) > 5 {
											if tt, ok6 := a[5].([]any); ok6 {
												if ii < len(tt) {
													if s, ok7 := tt[ii].(string); ok7 {
														alt = s
													}
												} else if len(tt) > 0 {
													if s, ok7 := tt[0].(string); ok7 {
														alt = s
													}
												}
											}
										}
									}
								}
								genImages = append(genImages, GeneratedImage{Image: Image{URL: urlStr, Title: title, Alt: alt, Proxy: c.Proxy}, Cookies: c.Cookies})
							}
						}
					}
				}
			}
		}

		cand := Candidate{
			RCID:            fmt.Sprintf("%v", cArr[0]),
			Text:            decodeHTML(text),
			Thoughts:        thoughts,
			WebImages:       webImages,
			GeneratedImages: genImages,
		}
		candidates = append(candidates, cand)
	}

	if len(candidates) == 0 {
		return empty, &GeminiError{Msg: "Failed to generate contents. No output data found in response."}
	}
	output := ModelOutput{Metadata: metadata, Candidates: candidates, Chosen: 0}
	if chat != nil {
		chat.lastOutput = &output
	}
	return output, nil
}

// extractErrorCode attempts to navigate the known nested error structure and fetch the integer code.
// Mirrors reference path: response_json[0][5][2][0][1][0]
func extractErrorCode(top []any) (int, bool) {
	if len(top) == 0 {
		return 0, false
	}
	a, ok := top[0].([]any)
	if !ok || len(a) <= 5 {
		return 0, false
	}
	b, ok := a[5].([]any)
	if !ok || len(b) <= 2 {
		return 0, false
	}
	c, ok := b[2].([]any)
	if !ok || len(c) == 0 {
		return 0, false
	}
	d, ok := c[0].([]any)
	if !ok || len(d) <= 1 {
		return 0, false
	}
	e, ok := d[1].([]any)
	if !ok || len(e) == 0 {
		return 0, false
	}
	f, ok := e[0].(float64)
	if !ok {
		return 0, false
	}
	return int(f), true
}

// StartChat returns a ChatSession attached to the client
func (c *GeminiClient) StartChat(model Model, gem *Gem, metadata []string) *ChatSession {
	return &ChatSession{client: c, metadata: normalizeMeta(metadata), model: model, gem: gem, requestedModel: strings.ToLower(model.Name)}
}

// ChatSession holds conversation metadata
type ChatSession struct {
	client         *GeminiClient
	metadata       []string // cid, rid, rcid
	lastOutput     *ModelOutput
	model          Model
	gem            *Gem
	requestedModel string
}

func (cs *ChatSession) String() string {
	var cid, rid, rcid string
	if len(cs.metadata) > 0 {
		cid = cs.metadata[0]
	}
	if len(cs.metadata) > 1 {
		rid = cs.metadata[1]
	}
	if len(cs.metadata) > 2 {
		rcid = cs.metadata[2]
	}
	return fmt.Sprintf("ChatSession(cid='%s', rid='%s', rcid='%s')", cid, rid, rcid)
}

func normalizeMeta(v []string) []string {
	out := []string{"", "", ""}
	for i := 0; i < len(v) && i < 3; i++ {
		out[i] = v[i]
	}
	return out
}

func (cs *ChatSession) Metadata() []string     { return cs.metadata }
func (cs *ChatSession) SetMetadata(v []string) { cs.metadata = normalizeMeta(v) }
func (cs *ChatSession) RequestedModel() string { return cs.requestedModel }
func (cs *ChatSession) SetRequestedModel(name string) {
	cs.requestedModel = strings.ToLower(name)
}
func (cs *ChatSession) CID() string {
	if len(cs.metadata) > 0 {
		return cs.metadata[0]
	}
	return ""
}
func (cs *ChatSession) RID() string {
	if len(cs.metadata) > 1 {
		return cs.metadata[1]
	}
	return ""
}
func (cs *ChatSession) RCID() string {
	if len(cs.metadata) > 2 {
		return cs.metadata[2]
	}
	return ""
}
func (cs *ChatSession) setCID(v string) {
	if len(cs.metadata) < 1 {
		cs.metadata = normalizeMeta(cs.metadata)
	}
	cs.metadata[0] = v
}
func (cs *ChatSession) setRID(v string) {
	if len(cs.metadata) < 2 {
		cs.metadata = normalizeMeta(cs.metadata)
	}
	cs.metadata[1] = v
}
func (cs *ChatSession) setRCID(v string) {
	if len(cs.metadata) < 3 {
		cs.metadata = normalizeMeta(cs.metadata)
	}
	cs.metadata[2] = v
}

// SendMessage shortcut to client's GenerateContent
func (cs *ChatSession) SendMessage(prompt string, files []string) (ModelOutput, error) {
	out, err := cs.client.GenerateContent(prompt, files, cs.model, cs.gem, cs)
	if err == nil {
		cs.lastOutput = &out
		cs.SetMetadata(out.Metadata)
		cs.setRCID(out.RCID())
	}
	return out, err
}

// ChooseCandidate selects a candidate from last output and updates rcid
func (cs *ChatSession) ChooseCandidate(index int) (ModelOutput, error) {
	if cs.lastOutput == nil {
		return ModelOutput{}, &ValueError{Msg: "No previous output data found in this chat session."}
	}
	if index >= len(cs.lastOutput.Candidates) {
		return ModelOutput{}, &ValueError{Msg: fmt.Sprintf("Index %d exceeds candidates", index)}
	}
	cs.lastOutput.Chosen = index
	cs.setRCID(cs.lastOutput.RCID())
	return *cs.lastOutput, nil
}
