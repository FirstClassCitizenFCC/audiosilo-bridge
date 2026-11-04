package audiblesync

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kodestar/audiosilo-bridge/internal/audible"
)

// audibleAnswer is the error the real Audible client answers when Audible
// replies status with retryAfter (a wait past the client's own cap, so it
// gives up at once).
func audibleAnswer(t *testing.T, status int, retryAfter string) error {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(status)
	}))
	defer ts.Close()
	key, err := rsa.GenerateKey(rand.Reader, 1024) //nolint:gosec // a test key that signs nothing real
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	c, err := audible.NewClient(audible.Credentials{ADPToken: "ADP", DevicePrivateKey: string(keyPEM), DeviceType: "A2CZJZGLK2JJVM",
		DeviceSerial: "SER", Marketplace: "us"}, audible.WithEndpoints(audible.Endpoints{API: ts.URL}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.LastPositions(context.Background(), []string{"B000000001"}, nil)
	if err == nil {
		t.Fatal("the answer was no error")
	}
	return err
}
