package agent

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/A-Hackers-Guide/sys1/internal/protocol"
	"github.com/A-Hackers-Guide/sys1/internal/version"
)

// The sandbox for apps on machines lent by other people: the app's
// container joins an internal network with no route anywhere, and a small
// gateway container (sys0 egress-proxy) is the only way in or out. It
// forwards the app's declared ports inbound, and lets it reach only the
// hosts its package lists, never the lender's own network.

const gatewayProxyPort = 3128

var semverRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// gatewayImage is sys1's own image, which carries sys0.
func gatewayImage() string {
	if img := os.Getenv("SYS1_GATEWAY_IMAGE"); img != "" {
		return img
	}
	if semverRe.MatchString(version.Version) {
		return "ghcr.io/a-hackers-guide/sys1:" + version.Version
	}
	return "ghcr.io/a-hackers-guide/sys1:latest"
}

type sandbox struct {
	rt, network, gateway string
}

func sandboxFor(rt string, t protocol.Task) *sandbox {
	return &sandbox{rt: rt, network: "sys1-n-" + t.ID, gateway: "sys1-g-" + t.ID}
}

// start creates the internal network and the gateway in front of it.
func (sb *sandbox) start(t protocol.Task) error {
	sb.stop() // leftovers from a crash
	if out, err := exec.Command(sb.rt, "network", "create", "--internal", sb.network).CombinedOutput(); err != nil {
		return fmt.Errorf("creating the sandbox network: %v: %s", err, strings.TrimSpace(string(out)))
	}
	args := []string{"run", "-d", "--rm", "--name", sb.gateway, "--label", "sys1.task=" + t.ID,
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--read-only",
		"--pids-limit", "256", "--memory", "64m", "--cpu-shares", "2"}
	proxyArgs := []string{"egress-proxy", "--listen", ":" + strconv.Itoa(gatewayProxyPort)}
	if len(t.Spec.Egress) > 0 {
		proxyArgs = append(proxyArgs, "--allow", strings.Join(t.Spec.Egress, ","))
	}
	// Each published port lands on the gateway (on a high port, as the
	// gateway has no privileges) and is forwarded to the app.
	for i, p := range t.Spec.Ports {
		host, cont, _ := strings.Cut(strings.TrimSuffix(p, "/tcp"), ":")
		gw := strconv.Itoa(20000 + i)
		args = append(args, "-p", host+":"+gw)
		proxyArgs = append(proxyArgs, "--forward", gw+":sys1-"+t.ID+":"+cont)
	}
	args = append(args, "--entrypoint", "sys0", gatewayImage())
	args = append(args, proxyArgs...)
	if out, err := exec.Command(sb.rt, args...).CombinedOutput(); err != nil {
		sb.stop()
		return fmt.Errorf("starting the sandbox gateway: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command(sb.rt, "network", "connect", sb.network, sb.gateway).CombinedOutput(); err != nil {
		sb.stop()
		return fmt.Errorf("connecting the sandbox gateway: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (sb *sandbox) stop() {
	exec.Command(sb.rt, "rm", "-f", sb.gateway).Run()
	exec.Command(sb.rt, "network", "rm", sb.network).Run()
}

// proxyEnv points the app at the gateway, in both spellings tools use.
func (sb *sandbox) proxyEnv() []string {
	u := "http://" + sb.gateway + ":" + strconv.Itoa(gatewayProxyPort)
	var env []string
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		env = append(env, k+"="+u)
	}
	return append(env, "NO_PROXY=localhost,127.0.0.1", "no_proxy=localhost,127.0.0.1")
}
