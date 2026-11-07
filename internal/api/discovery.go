package api

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"strconv"

	"github.com/iamxvbaba/td/crypto"
)

// Discovery serves /.well-known/greengram-gateway.json, which the Android
// app reads from the instance URL to find the MTProto address and pin the key.
// Same document the gateway serves, so existing builds keep working.
func Discovery(dc int, host string, port int, key *rsa.PrivateKey) http.Handler {
	pub := pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey)})
	doc, _ := json.Marshal(map[string]any{
		"version":     1,
		"dc":          dc,
		"host":        host,
		"port":        port,
		"publicKey":   string(pub),
		"fingerprint": strconv.FormatInt(crypto.RSAFingerprint(&key.PublicKey), 10),
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "max-age=300")
		w.Write(doc)
	})
}
