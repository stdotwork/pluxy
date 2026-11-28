package route

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

var callerSecret = func() []byte {
	b := make([]byte, 32)
	rand.Read(b)
	return b
}()

func caller(extension, tenant string) string {
	if tenant == "" {
		return ""
	}
	m := hmac.New(sha256.New, callerSecret)
	m.Write([]byte(extension))
	m.Write([]byte{0})
	m.Write([]byte(tenant))
	return hex.EncodeToString(m.Sum(nil)[:16])
}
