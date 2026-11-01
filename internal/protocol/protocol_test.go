package protocol

import (
	"reflect"
	"strings"
	"testing"
)

func TestJoinCodeRoundTrip(t *testing.T) {
	in := JoinCode{Addr: "192.168.1.5:7443", Fingerprint: strings.Repeat("ab", 32), Token: "tok"}
	out, err := ParseJoinCode("  " + in.String() + "\n")
	if err != nil || !reflect.DeepEqual(out, in) {
		t.Fatalf("got %+v, %v", out, err)
	}
	in.More = []string{"100.101.102.103:7443", "203.0.113.9:7443"}
	out, err = ParseJoinCode(in.String())
	if err != nil || len(out.Addrs()) != 3 || out.Addrs()[2] != "203.0.113.9:7443" {
		t.Fatalf("multi-address code: %+v, %v", out, err)
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

func TestVMValidation(t *testing.T) {
	ok := TaskSpec{Kind: KindVM, MemoryMB: 512, Ports: []string{"2222:22"}, Network: true,
		VM: &VMSpec{Image: "https://example.com/os.qcow2", SHA256: strings.Repeat("a", 64)}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []func(*TaskSpec){
		func(s *TaskSpec) { s.VM = nil },
		func(s *TaskSpec) { s.VM = &VMSpec{Image: "http://insecure/os.img", SHA256: strings.Repeat("a", 64)} },
		func(s *TaskSpec) { s.VM = &VMSpec{Image: "https://x/os.img", SHA256: "abc"} },
		func(s *TaskSpec) { s.MemoryMB = 0 },
		func(s *TaskSpec) { s.Ports = []string{"53:53/udp"} },
		func(s *TaskSpec) {
			s.VM = &VMSpec{Image: "https://x/os.img", SHA256: strings.Repeat("a", 64), Arch: "mips"}
		},
	}
	for i, f := range bad {
		s := ok
		vm := *ok.VM
		s.VM = &vm
		f(&s)
		if s.Validate() == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	c := TaskSpec{Kind: KindContainer, Image: "x", VM: &VMSpec{}}
	if c.Validate() == nil {
		t.Error("vm settings accepted on a container")
	}
}
