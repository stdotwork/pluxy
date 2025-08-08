// Package util provides utility functions for the CLI Proxy API server.
// It includes helper functions for proxy configuration, HTTP client setup,
// and other common operations used across the application.
package util

import (
	"context"
	"net"
	"net/http"
	"net/url"

	"github.com/luispater/CLIProxyAPI/internal/config"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

// SetProxy configures the provided HTTP client with proxy settings from the configuration.
// It supports SOCKS5, HTTP, and HTTPS proxies. The function modifies the client's transport
// to route requests through the configured proxy server.
func SetProxy(cfg *config.Config, httpClient *http.Client) *http.Client {
	var transport *http.Transport
	proxyURL, errParse := url.Parse(cfg.ProxyURL)
	if errParse == nil {
		if proxyURL.Scheme == "socks5" {
			username := proxyURL.User.Username()
			password, _ := proxyURL.User.Password()
			proxyAuth := &proxy.Auth{User: username, Password: password}
			dialer, errSOCKS5 := proxy.SOCKS5("tcp", proxyURL.Host, proxyAuth, proxy.Direct)
			if errSOCKS5 != nil {
				log.Errorf("create SOCKS5 dialer failed: %v", errSOCKS5)
				return httpClient
			}
			transport = &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					return dialer.Dial(network, addr)
				},
			}
		} else if proxyURL.Scheme == "http" || proxyURL.Scheme == "https" {
			transport = &http.Transport{Proxy: http.ProxyURL(proxyURL)}
		}
	}
	if transport != nil {
		httpClient.Transport = transport
	}
	return httpClient
}
