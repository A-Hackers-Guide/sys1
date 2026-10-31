package marketplace

import (
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/A-Hackers-Guide/sys1/internal/protocol"
)

func sample() Manifest {
	return Manifest{
		Name: "demo", Version: "1.0", Title: "Demo", Category: "Test",
		Run: protocol.TaskSpec{Kind: protocol.KindContainer, Image: "alpine:3", Command: []string{"echo", "hi"}},
	}
}

func TestBuiltinCatalogIsValid(t *testing.T) {
	c, err := Load("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Packages) < 5 {
		t.Fatalf("only %d builtin packages", len(c.Packages))
	}
}

func TestSignatureDetectsTampering(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	m := sample()
	m.Sign(priv)
	if err := m.VerifySignature(); err != nil {
		t.Fatal(err)
	}
	m.Run.Image = "evil/miner"
	if m.VerifySignature() == nil {
		t.Fatal("tampered manifest verified")
	}
}

func TestOnlyTrustedKeysVerify(t *testing.T) {
	dir := t.TempDir()
	_, priv, _ := ed25519.GenerateKey(nil)
	m := sample()
	m.Sign(priv)
	b, _ := json.Marshal(m)
	os.WriteFile(filepath.Join(dir, "demo.json"), b, 0o600)

	c, _ := Load(dir, nil)
	if c.Packages["demo"].Verified {
		t.Fatal("untrusted key verified")
	}
	c, _ = Load(dir, []string{m.PublicKey})
	if !c.Packages["demo"].Verified {
		t.Fatalf("trusted key rejected: %s", c.Packages["demo"].Problem)
	}
}

func TestCompatibility(t *testing.T) {
	m := sample()
	if ok, _ := m.Compatible([]Machine{{OS: "windows"}}); ok {
		t.Fatal("container compatible without a runtime")
	}
	if ok, _ := m.Compatible([]Machine{{OS: "windows", Containers: true}}); !ok {
		t.Fatal("container incompatible with a runtime")
	}
	m.Run = protocol.TaskSpec{Kind: protocol.KindExec, Command: []string{"x"}, OS: []string{"linux"}}
	if ok, _ := m.Compatible([]Machine{{OS: "linux"}}); ok {
		t.Fatal("exec allowed on a community machine")
	}
	if ok, _ := m.Compatible([]Machine{{OS: "linux", Owner: true}}); !ok {
		t.Fatal("exec refused on an owner machine")
	}
}

func TestApplyInputs(t *testing.T) {
	m := sample()
	m.Inputs = []Input{{Name: "IMAGE", Label: "Image", Default: "alpine"}}
	m.Run.Image = "${IMAGE}"
	if got := m.Apply(nil).Image; got != "alpine" {
		t.Fatalf("default not applied: %q", got)
	}
	if got := m.Apply(map[string]string{"IMAGE": "nginx"}).Image; got != "nginx" {
		t.Fatalf("value not applied: %q", got)
	}
}
