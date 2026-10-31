package protocol

import (
	"strings"
	"testing"
)

func TestJoinCodeRoundTrip(t *testing.T) {
	in := JoinCode{Addr: "192.168.1.5:7443", Fingerprint: strings.Repeat("ab", 32), Token: "tok"}
	out, err := ParseJoinCode("  " + in.String() + "\n")
	if err != nil || out != in {
		t.Fatalf("got %+v, %v", out, err)
	}
	if _, err := ParseJoinCode("sys1-join:!!!"); err == nil {
		t.Fatal("damaged code accepted")
	}
	if _, err := ParseJoinCode("hello"); err == nil {
		t.Fatal("random text accepted")
	}
}

func TestValidateRejectsInjection(t *testing.T) {
	bad := []TaskSpec{
		{Kind: KindContainer, Image: "--privileged"},
		{Kind: KindContainer, Image: "nginx", Ports: []string{"80:80"}},
		{Kind: KindContainer, Image: "nginx", Network: true, Ports: []string{"80:80 -v /:/host"}},
		{Kind: KindContainer, Image: "nginx", Volumes: []string{"/:/host"}},
		{Kind: KindContainer, Image: "nginx", Volumes: []string{"/data/../etc"}},
		{Kind: KindContainer, Image: "nginx", Env: map[string]string{"A=B": "x"}},
		{Kind: KindExec},
		{Kind: "vm"},
	}
	for i, s := range bad {
		if s.Validate() == nil {
			t.Errorf("case %d accepted: %+v", i, s)
		}
	}
	good := TaskSpec{Kind: KindContainer, Image: "ghcr.io/x/y:1.2@sha256:abc", Network: true, Ports: []string{"8096:8096", "53:53/udp"}, Volumes: []string{"/config"}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
}
