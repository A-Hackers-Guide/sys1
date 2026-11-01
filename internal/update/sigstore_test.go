package update

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestVerifyARealRelease checks a published release's signature with the
// real Sigstore trust root. It needs the internet, so it runs only with
// SYS1_NETWORK_TESTS=1 (CI sets it).
func TestVerifyARealRelease(t *testing.T) {
	if os.Getenv("SYS1_NETWORK_TESTS") == "" {
		t.Skip("set SYS1_NETWORK_TESTS=1 to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	get := func(name string) []byte {
		resp, err := http.Get("https://github.com/" + Repo + "/releases/download/v0.7.0/" + name)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %s", name, resp.Status)
		}
		return b
	}
	sums, bundle := get(checksumsName), get(bundleName)
	cache := t.TempDir()
	if err := VerifySigstore(ctx, cache, sums, bundle); err != nil {
		t.Fatalf("the real release doesn't verify: %v", err)
	}
	tampered := bytes.Replace(sums, []byte("sys1_0.7.0_linux_amd64"), []byte("sys1_0.7.0_linux_amd65"), 1)
	if VerifySigstore(ctx, cache, tampered, bundle) == nil {
		t.Fatal("tampered checksums verified")
	}
}
