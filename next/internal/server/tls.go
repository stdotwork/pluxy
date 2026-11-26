package server

import (
	"crypto/tls"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

// certCheckInterval is how often CertFiles looks at its files for a change, at most.
const certCheckInterval = time.Second

// CertFiles serves the TLS certificate held in a certificate file and a key file, and reads
// them again when either changes, so a renewed certificate is served without a restart. While
// the files on disk do not make a valid pair (one written, the other not yet), it keeps
// serving the pair it has.
type CertFiles struct {
	certPath, keyPath string

	mu      sync.Mutex
	cur     *tls.Certificate
	stamps  [2]fileStamp // of the files when last read, or last found unreadable
	checked time.Time
}

// fileStamp tells whether a file changed: its size and modification time.
type fileStamp struct {
	size int64
	mod  time.Time
}

func stampOf(path string) fileStamp {
	fi, err := os.Stat(path) // through symbolic links, which certificate tools swap
	if err != nil {
		return fileStamp{size: -1}
	}
	return fileStamp{fi.Size(), fi.ModTime()}
}

// LoadCertFiles reads the certificate (PEM, the chain after the leaf) and private key (PEM) at
// the given paths.
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

// GetCertificate is tls.Config.GetCertificate: the current pair, read again first when a file
// changed since the last look.
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

// TLSConfig returns the TLS settings to serve listen with, or nil when tls.enable is off.
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
