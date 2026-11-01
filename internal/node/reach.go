package node

import (
	"context"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/nat"
)

// How machines reach the node: its LAN address, any VPN addresses
// (Tailscale, ZeroTier, WireGuard), addresses the owner gave with
// --public-addr, and, when the owner turns it on, a port the node opens on
// the home router itself.

// Settings are the node owner's choices, kept in the journal.
type Settings struct {
	Internet bool `json:"internet"` // open the client API port on the router
}

// InternetStatus describes the router mapping for the dashboard.
type InternetStatus struct {
	On       bool   `json:"on"`
	Working  bool   `json:"working"`
	Method   string `json:"method,omitempty"`
	External string `json:"external,omitempty"`
	CGNAT    bool   `json:"cgnat,omitempty"`
	Problem  string `json:"problem,omitempty"`
}

// addrsLocked lists every address machines can reach the node on, best
// first. Clients try them in order and stay with the first that answers.
func (n *Node) addrsLocked() []string {
	out := n.ownAddrsLocked()
	if n.clustered() {
		seen := map[string]bool{}
		for _, a := range out {
			seen[a] = true
		}
		for id, m := range n.cl.members {
			if id == n.cl.id || m.Witness { // clients never use a witness
				continue
			}
			for _, a := range m.APIAddrs {
				if !seen[a] {
					seen[a] = true
					out = append(out, a)
				}
			}
		}
	}
	return out
}

// ownAddrsLocked lists this node's own addresses, best first.
func (n *Node) ownAddrsLocked() []string {
	host, _, _ := net.SplitHostPort(n.cfg.ListenAddr)
	if ip := net.ParseIP(host); host != "" && ip != nil && !ip.IsUnspecified() {
		return []string{n.cfg.ListenAddr} // bound to one address only
	}
	_, port, _ := net.SplitHostPort(n.cfg.ListenAddr)
	var out []string
	seen := map[string]bool{}
	add := func(a string) {
		if a != "" && !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	for _, p := range strings.Split(n.cfg.PublicAddr, ",") {
		add(strings.TrimSpace(p))
	}
	add(net.JoinHostPort(lanIP(), port))
	for _, ip := range vpnIPs() {
		add(net.JoinHostPort(ip, port))
	}
	if n.internet.Working && !n.internet.CGNAT {
		add(n.internet.External)
	}
	return out
}

// vpnIPs finds addresses on VPN interfaces: Tailscale's 100.64.0.0/10
// range, and interfaces named like ZeroTier, WireGuard or tunnels.
func vpnIPs() []string {
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	var out []string
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		name := strings.ToLower(ifc.Name)
		vpnName := strings.HasPrefix(name, "tailscale") || strings.HasPrefix(name, "zt") || strings.HasPrefix(name, "wg") ||
			strings.HasPrefix(name, "tun") || strings.HasPrefix(name, "utun")
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil || ipn.IP.IsLinkLocalUnicast() {
				continue
			}
			if cgnat.Contains(ipn.IP) || vpnName {
				out = append(out, ipn.IP.String())
			}
		}
	}
	return out
}

// SetInternet turns the router port mapping on or off.
func (n *Node) SetInternet(on bool) {
	n.mu.Lock()
	n.settings.Internet = on
	n.persistLocked(event{Settings: &n.settings})
	n.mu.Unlock()
	n.applyInternet()
}

// applyInternet opens or closes the router mapping to match the settings.
func (n *Node) applyInternet() {
	n.mu.Lock()
	on, m := n.settings.Internet && !n.Witness(), n.mapping // a witness serves no machines
	n.mu.Unlock()
	if !on {
		if m != nil {
			m.Close()
		}
		n.mu.Lock()
		n.mapping, n.internet = nil, InternetStatus{}
		n.mu.Unlock()
		n.saveJoinCode()
		return
	}
	if m != nil {
		return
	}
	n.mu.Lock()
	n.internet = InternetStatus{On: true, Problem: "Asking the router to open the port…"}
	_, portStr, _ := net.SplitHostPort(n.cfg.ListenAddr)
	n.mu.Unlock()
	port, _ := strconv.Atoi(portStr)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		m, err := nat.Map(ctx, port, "sys1 node")
		n.mu.Lock()
		if !n.settings.Internet { // turned off meanwhile
			n.mu.Unlock()
			if m != nil {
				m.Close()
			}
			return
		}
		st := InternetStatus{On: true}
		if err != nil {
			st.Problem = err.Error()
		} else {
			n.mapping = m
			st.Working, st.Method, st.External, st.CGNAT = true, m.Method, m.External, m.CGNAT
			if m.CGNAT {
				st.Problem = "Your internet provider shares one public address between customers (carrier-grade NAT), so the router can't make this node reachable. Use a VPN such as Tailscale or ZeroTier instead."
			}
			n.log.Info("router port opened", "method", m.Method, "shared", m.CGNAT)
		}
		n.internet = st
		n.mu.Unlock()
		n.saveJoinCode()
	}()
}

func (n *Node) InternetStatus() InternetStatus {
	n.mu.Lock()
	defer n.mu.Unlock()
	st := n.internet
	st.On = n.settings.Internet
	return st
}
