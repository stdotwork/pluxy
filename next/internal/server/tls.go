package server

import (
	"crypto/tls"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

const certCheckInterval = time.Second

type CertFiles struct {
	certPath, keyPath string

	mu      sync.Mutex
	cur     *tls.Certificate
	stamps  [2]fileStamp
	checked time.Time
}

type fileStamp struct {
	size int64
	mod  time.Time
}

func stampOf(path string) fileStamp {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{size: -1}
	}
	return fileStamp{fi.Size(), fi.ModTime()}
}

func LoadCertFiles(certPath, keyPath string) (*CertFiles, error) {
	c := &CertFiles{certPath: certPath, keyPath: keyPath}
	c.stamps = [2]fileStamp{stampOf(certPath), stampOf(keyPath)}
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	c.cur, c.checked = &pair, time.Now()
	return c, nil
}

func (c *CertFiles) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if now := time.Now(); now.Sub(c.checked) >= certCheckInterval {
		c.checked = now
		stamps := [2]fileStamp{stampOf(c.certPath), stampOf(c.keyPath)}
		if stamps != c.stamps {
			c.stamps = stamps
			if pair, err := tls.LoadX509KeyPair(c.certPath, c.keyPath); err != nil {
				log.Printf("tls: the certificate files changed but do not load, so the previous certificate is served until they change again: %v", err)
			} else {
				c.cur = &pair
				log.Printf("tls: serving the certificate read again from %s", c.certPath)
			}
		}
	}
	return c.cur, nil
}

func (c *Config) TLSConfig() (*tls.Config, error) {
	if !c.TLS.Enable {
		return nil, nil
	}
	certs, err := LoadCertFiles(c.TLS.Cert, c.TLS.Key)
	if err != nil {
		return nil, fmt.Errorf("tls: %w", err)
	}
	return &tls.Config{GetCertificate: certs.GetCertificate}, nil
}
