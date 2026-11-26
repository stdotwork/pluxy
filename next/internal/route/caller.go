package route

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
)

// callerSecret keys the caller values extensions get (llm.Request.Caller), so that a value stands
// for a client key without revealing it, even a guessable one, and two extensions get unrelated
// values for the same client. It is made once per core process: the values change when core
// restarts, as the extensions it runs do.
var callerSecret = func() []byte {
	b := make([]byte, 32)
	rand.Read(b)
	return b
}()

// caller returns the caller value extension gets for a request from tenant, or "" when the request
// has no tenant.
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
