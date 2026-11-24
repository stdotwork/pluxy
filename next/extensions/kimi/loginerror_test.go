package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stdotwork/pluxy/next/sdk/sdktest"
)

func TestLoginErrorTextIsBounded(t *testing.T) {
	for _, code := range []string{sdktest.HostileCode, strings.Repeat("a", 5000), sdktest.HostileJWT} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"` + code + `","error_description":"` + sdktest.HostileText() + `"}`))
		}))
		f := oauthPost(context.Background(), ts.URL, "device", url.Values{}, &struct{}{})
		ts.Close()
		if f == nil {
			t.Fatal("a 400 reply was taken as success")
		}
		sdktest.CheckLoginError(t, "code "+code[:min(len(code), 10)], tokenError(f), code == sdktest.HostileCode)
	}
}
