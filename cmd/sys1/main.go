// Command sys1 is the node: the hub that sys0 clients join, the
// dashboard for the whole pool, and the marketplace.
//
//	sys1                    start the node and open the dashboard
//	sys1 open               open the dashboard of a running node
//	sys1 join-code          print the code new machines paste in
//	sys1 pkg keygen|sign|verify   sign marketplace packages
//
// Run `sys1 help` for everything.
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/agent"
	"github.com/A-Hackers-Guide/sys1/internal/cli"
	"github.com/A-Hackers-Guide/sys1/internal/crash"
	"github.com/A-Hackers-Guide/sys1/internal/marketplace"
	"github.com/A-Hackers-Guide/sys1/internal/node"
	"github.com/A-Hackers-Guide/sys1/internal/paths"
	"github.com/A-Hackers-Guide/sys1/internal/protocol"
	"github.com/A-Hackers-Guide/sys1/internal/resources"
	"github.com/A-Hackers-Guide/sys1/internal/setup"
	"github.com/A-Hackers-Guide/sys1/internal/update"
	"github.com/A-Hackers-Guide/sys1/internal/version"
)

const usage = `sys1: the node that pools every machine's spare power

Usage:
  sys1 [serve]              start the node and open the dashboard (default)
        --listen <addr>       client API address (default :7443)
        --gui <addr>          dashboard address (default 127.0.0.1:7700)
        --public-addr <h:p>   address machines should dial (default: this machine's LAN IP)
        --no-open             do not open the dashboard in a browser
        --foreground          stay in the foreground (for containers)
        --no-local            do not lend this machine's own capacity to the pool
        --trust-key <key>     trust marketplace packages signed by this key (repeatable)
        --copies <n>          copies kept of app data in pooled storage (default 3)
        --staging-gb <n>      disk the node uses to pass data between machines (default 4)
        --no-auto-update      don't install new versions automatically
  sys1 update               install the latest version now
  sys1 open                 open the dashboard of a running node
  sys1 join-code            print the join code to share with other people
        --mine                print the join code for your own machines
  sys1 install|uninstall    run the node as a background service (starts at boot)
  sys1 start|stop|restart|status
  sys1 --cluster            run as a member of a cluster of nodes (see docs/CLUSTER.md)
        --raft-addr <addr>    address for traffic between nodes (default :7444)
  sys1 cluster code         print the code another node uses to join this cluster
  sys1 cluster join <code>  make this machine a node of an existing cluster, then run: sys1 --cluster
  sys1 pkg keygen <file>    create a package signing key
  sys1 pkg sign <manifest.json> --key <file>
  sys1 pkg verify <manifest.json>
  sys1 version

Every command accepts --data <dir> (default: %s).
`

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	paths.ExtendPath() // the node's own client finds Docker and QEMU like sys0 does
	cmd, args := "serve", os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	sub := ""
	if (cmd == "pkg" || cmd == "cluster") && len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("sys1 "+cmd, flag.ExitOnError)
	dataDir := fs.String("data", paths.DataDir("sys1"), "data directory")
	listen := fs.String("listen", ":7443", "client API address")
	gui := fs.String("gui", "127.0.0.1:7700", "dashboard address")
	public := fs.String("public-addr", "", "address machines dial")
	noOpen := fs.Bool("no-open", false, "do not open a browser")
	foreground := fs.Bool("foreground", false, "run in the foreground even when no terminal is attached (containers)")
	noLocal := fs.Bool("no-local", false, "do not lend this machine's capacity")
	keyFile := fs.String("key", "", "signing key file")
	copies := fs.Int("copies", 3, "copies kept of app data")
	clusterMode := fs.Bool("cluster", false, "run as a member of a cluster of nodes")
	raftAddr := fs.String("raft-addr", ":7444", "address for traffic between cluster nodes")
	stagingGB := fs.Int("staging-gb", 4, "disk the node may use to pass chunks between machines")
	verbose := fs.Bool("v", false, "verbose logging")
	noAutoUpdate := fs.Bool("no-auto-update", false, "don't install new versions automatically")
	mine := fs.Bool("mine", false, "join-code: the code for your own machines")
	var trustKeys multi
	fs.Var(&trustKeys, "trust-key", "trusted package signing key")
	fs.Usage = func() { fmt.Fprintf(os.Stderr, usage, paths.DataDir("sys1")) }
	var positional []string
	for len(args) > 0 {
		fs.Parse(args)
		args = fs.Args()
		if len(args) > 0 {
			positional = append(positional, args[0])
			args = args[1:]
		}
	}

	// Containers and services need an absolute path, whatever was typed.
	if abs, err := filepath.Abs(*dataDir); err == nil {
		*dataDir = abs
	}
	log := cli.Logger(*dataDir, "sys1", *verbose)
	cfg := node.Config{DataDir: *dataDir, ListenAddr: *listen, GUIAddr: *gui, PublicAddr: *public, TrustedKeys: append(trustKeys, readTrustedKeys(*dataDir)...),
		Copies: *copies, StagingBytes: int64(*stagingGB) << 30, Cluster: *clusterMode, RaftAddr: *raftAddr}
	svcArgs := []string{"serve", "--no-open", "--data", *dataDir, "--listen", *listen, "--gui", *gui,
		"--copies", fmt.Sprint(*copies), "--staging-gb", fmt.Sprint(*stagingGB)}
	if *public != "" {
		svcArgs = append(svcArgs, "--public-addr", *public)
	}
	if *noLocal {
		svcArgs = append(svcArgs, "--no-local")
	}
	if *clusterMode {
		svcArgs = append(svcArgs, "--cluster", "--raft-addr", *raftAddr)
	}
	if *noAutoUpdate {
		svcArgs = append(svcArgs, "--no-auto-update")
	}
	u := &update.Updater{App: "sys1", Current: version.Version, DataDir: *dataDir, Log: log}
	cfg.Updates, cfg.OnUpdateNow = "automatic", u.Kick
	if !cli.Interactive() {
		// As a service: exit, and the service manager starts it again.
		cfg.Restart = func() { os.Exit(1) }
	}
	if *noAutoUpdate {
		cfg.Updates = "off (started with --no-auto-update)"
	} else if why := u.Allowed(); why != "" {
		cfg.Updates = why
	}
	run := func(ctx context.Context, quiet bool, restart bool) error {
		work := func(ctx context.Context) error { return serve(ctx, cfg, log, quiet, !*noLocal) }
		if *noAutoUpdate {
			return work(ctx)
		}
		return u.Run(ctx, restart, work)
	}
	svc := &cli.Service{
		Name:        "sys1",
		DisplayName: "sys1 node",
		Description: "The sys1 node: pools spare capacity from every connected machine.",
		Args:        svcArgs,
		Run: func(ctx context.Context) error {
			return run(ctx, true, !cli.Interactive())
		},
	}

	var err error
	switch cmd {
	case "serve":
		u.Relaunch() // a newer copy in the data folder, if updates go there
		if u.Startup() {
			os.Exit(1) // went back to the previous version; let the service manager start it
		}
		if r := crash.Harvest(*dataDir, "sys1", version.Version); r != nil {
			crash.OpenStore(*dataDir).Add(*r)
		}
		crash.Capture(*dataDir, "sys1")
		if *noOpen && !*foreground {
			err = svc.RunManaged()
			break
		}
		ctx, cancel := cli.SignalContext()
		// In a container (--foreground) the image is updated instead; a
		// terminal run picks up a new version the next time it starts.
		err = run(ctx, *noOpen, false)
		cancel()
	case "update":
		err = updateNow(u, svc)
	case "open":
		// Print the link too: in a container or over SSH there is no browser.
		url := "http://" + *gui + "/?key=" + node.GUIKey(*dataDir)
		url = strings.Replace(url, "://0.0.0.0:", "://127.0.0.1:", 1)
		fmt.Println("Dashboard:", url)
		setup.OpenURL(url)
	case "join-code":
		err = printJoinCode(cfg, *mine)
	case "install":
		err = svc.Install()
		if err == nil {
			fmt.Println("sys1 node installed as a service. Open the dashboard with: sys1 open")
		}
	case "uninstall":
		err = svc.Uninstall()
	case "start", "stop", "restart":
		err = svc.Control(cmd)
	case "status":
		fmt.Println("sys1", version.Version, "· service:", svc.Status(), "· data:", *dataDir)
	case "pkg":
		err = pkg(sub, positional, *keyFile)
	case "cluster":
		err = clusterCmd(sub, positional, *dataDir, *raftAddr, *listen, *gui)
	case "version", "--version":
		fmt.Println("sys1", version.String())
	case "help", "-h", "--help":
		fs.Usage()
	default:
		fs.Usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sys1:", err)
		os.Exit(1)
	}
}

func serve(ctx context.Context, cfg node.Config, log *slog.Logger, quiet, local bool) error {
	n, err := node.New(cfg, log)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	return n.Serve(ctx, func(apiAddr, guiAddr string) {
		url := n.GUIURL(guiAddr)
		log.Info("node ready", "clients", n.JoinCode().Addr, "dashboard", "http://"+guiAddr)
		if !quiet {
			fmt.Printf("\n  sys1 node is running.\n\n  Dashboard:  %s\n\n  Join code for your own machines:\n    %s\n  Join code to share with other people:\n    %s\n\n  Press Ctrl+C to stop.\n\n", url, n.OwnerJoinCode(), n.JoinCode())
			setup.OpenURL(url)
		}
		if local && !n.Witness() { // a witness only votes
			go runLocalAgent(ctx, n, filepath.Join(cfg.DataDir, "local-client"), log)
		}
	})
}

// runLocalAgent lends the node's own machine to the pool, so a single
// computer is already a working pool. In a cluster it registers like any
// other machine of yours and follows whichever node is leading.
func runLocalAgent(ctx context.Context, n *node.Node, dir string, log *slog.Logger) {
	log = log.With("component", "local-client")
	code := n.LocalJoinCode()
	if n.Clustered() {
		jc := n.OwnerJoinCode()
		code = protocol.JoinCode{Addr: code.Addr, More: jc.Addrs(), Fingerprint: jc.Fingerprint, Token: jc.Token}
	}
	cfg, err := agent.LoadConfig(dir)
	if err == nil && cfg.Fingerprint != code.Fingerprint {
		err = agent.ErrNotConfigured // node identity changed; rejoin
	}
	for err != nil {
		host, _ := os.Hostname()
		sampler := &resources.Sampler{DataDir: dir}
		if n.Clustered() {
			jc := n.OwnerJoinCode() // the token arrives with the cluster's state
			code.Token, code.More = jc.Token, jc.Addrs()
		}
		cfg, err = agent.Register(ctx, code, host+" (node)", protocol.TrustOwner, sampler.Sample(true), version.Version)
		if err == nil {
			agent.SaveConfig(dir, cfg)
			break
		}
		log.Warn("could not start the node's own client yet; retrying", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
	if !n.Clustered() {
		cfg.NodeAddr, cfg.NodeAddrs = code.Addr, nil // the port may have changed
	}
	a, err := agent.New(dir, cfg, log)
	if err != nil {
		log.Error("could not start the node's own client", "err", err)
		return
	}
	a.FixedAddrs = !n.Clustered()
	a.Updates = n.Updates() // it's the node's own binary
	a.Run(ctx)
}

func updateNow(u *update.Updater, svc *cli.Service) error {
	if why := u.Allowed(); why != "" {
		return errors.New("this install can't update itself: " + why)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	v, err := u.Once(ctx)
	if err != nil {
		return err
	}
	if v == "" {
		fmt.Println("sys1", u.Current, "is the latest version.")
		return nil
	}
	fmt.Println("Installed sys1", v+".")
	if svc.Status() == "running" {
		return svc.Control("restart")
	}
	return nil
}

// printJoinCode reads the code a running node saved, without touching
// the node's journal.
func printJoinCode(cfg node.Config, mine bool) error {
	file := node.JoinCodeFile
	if mine {
		file = node.OwnerJoinCodeFile
	}
	b, err := os.ReadFile(filepath.Join(cfg.DataDir, file))
	if err != nil {
		return errors.New("no join code yet: start the node first (run: sys1)")
	}
	fmt.Println(strings.TrimSpace(string(b)))
	return nil
}

func readTrustedKeys(dataDir string) []string {
	b, err := os.ReadFile(filepath.Join(dataDir, "trusted-keys.txt"))
	if err != nil {
		return nil
	}
	var keys []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			keys = append(keys, l)
		}
	}
	return keys
}

func pkg(sub string, args []string, keyFile string) error {
	switch sub {
	case "keygen":
		if len(args) != 1 {
			return errors.New("usage: sys1 pkg keygen <keyfile>")
		}
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		if err := os.WriteFile(args[0], []byte(base64.StdEncoding.EncodeToString(priv)+"\n"), 0o600); err != nil {
			return err
		}
		fmt.Println("Private key saved to", args[0], "(keep it secret).")
		fmt.Println("Public key (share this; node owners add it to trusted-keys.txt):")
		fmt.Println(base64.StdEncoding.EncodeToString(pub))
		return nil
	case "sign":
		if len(args) != 1 || keyFile == "" {
			return errors.New("usage: sys1 pkg sign <manifest.json> --key <keyfile>")
		}
		kb, err := os.ReadFile(keyFile)
		if err != nil {
			return err
		}
		key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(kb)))
		if err != nil || len(key) != ed25519.PrivateKeySize {
			return errors.New("not a sys1 signing key")
		}
		m, err := readManifest(args[0])
		if err != nil {
			return err
		}
		if err := m.Validate(); err != nil {
			return err
		}
		m.Sign(ed25519.PrivateKey(key))
		out, _ := json.MarshalIndent(m, "", "  ")
		if err := os.WriteFile(args[0], append(out, '\n'), 0o644); err != nil {
			return err
		}
		fmt.Println("Signed", m.Name, m.Version)
		return nil
	case "verify":
		if len(args) != 1 {
			return errors.New("usage: sys1 pkg verify <manifest.json>")
		}
		m, err := readManifest(args[0])
		if err != nil {
			return err
		}
		if err := m.Validate(); err != nil {
			return err
		}
		if err := m.VerifySignature(); err != nil {
			return err
		}
		fmt.Println("OK:", m.Name, m.Version, "signed by", m.PublicKey)
		return nil
	}
	return errors.New("usage: sys1 pkg keygen|sign|verify")
}

func readManifest(path string) (marketplace.Manifest, error) {
	var m marketplace.Manifest
	b, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(b, &m)
}

func clusterCmd(sub string, args []string, dataDir, raftAddr, listen, gui string) error {
	switch sub {
	case "code":
		b, err := os.ReadFile(filepath.Join(dataDir, node.ClusterCodeFile))
		if err != nil {
			return errors.New("no cluster code yet: start the node with --cluster first; the code appears once it leads")
		}
		fmt.Println(strings.TrimSpace(string(b)))
		return nil
	case "join":
		witness := len(args) == 2 && args[0] == "--witness"
		if witness {
			args = args[1:]
		}
		if len(args) != 1 {
			return errors.New("usage: sys1 cluster join [--witness] <cluster code>")
		}
		_, port, _ := net.SplitHostPort(listen)
		var api []string
		for _, ip := range localIPs() {
			api = append(api, net.JoinHostPort(ip, port))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if err := node.JoinCluster(ctx, dataDir, args[0], raftAddr, api, witness); err != nil {
			return err
		}
		what := "node"
		if witness {
			what = "witness"
		}
		fmt.Printf("Joined as a %s. Start it with: sys1 --cluster   (or: sudo sys1 install --cluster)\n", what)
		return nil
	case "prefer":
		if len(args) != 1 {
			return errors.New("usage: sys1 cluster prefer <node name or ID>")
		}
		if err := dashboardPost(dataDir, gui, "/api/cluster/prefer", map[string]string{"node": args[0]}); err != nil {
			return err
		}
		fmt.Println(args[0], "is now the preferred node: it leads whenever it is up.")
		return nil
	}
	return errors.New("usage: sys1 cluster code | sys1 cluster join [--witness] <code> | sys1 cluster prefer <node>")
}

// dashboardPost calls the dashboard API of the node running here, with
// the dashboard key from its data directory.
func dashboardPost(dataDir, gui, path string, req any) error {
	key, err := os.ReadFile(filepath.Join(dataDir, "dashboard.key"))
	if err != nil {
		return errors.New("no node here: run this on a cluster node (as the same user, or with sudo)")
	}
	host, port, _ := net.SplitHostPort(gui)
	if ip := net.ParseIP(host); host == "" || ip == nil || ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	body, _ := json.Marshal(req)
	hreq, _ := http.NewRequest(http.MethodPost, "http://"+net.JoinHostPort(host, port)+path, bytes.NewReader(body))
	hreq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(key)))
	hreq.Header.Set("X-Sys1", "1")
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(hreq)
	if err != nil {
		return fmt.Errorf("the node isn't answering: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return errors.New(strings.TrimSpace(string(msg)))
	}
	return nil
}

// localIPs lists this machine's IPv4 addresses that other machines might
// reach, LAN first.
func localIPs() []string {
	var out []string
	if c, err := net.Dial("udp", "1.1.1.1:80"); err == nil {
		out = append(out, c.LocalAddr().(*net.UDPAddr).IP.String())
		c.Close()
	}
	ifaces, _ := net.Interfaces()
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLinkLocalUnicast() && !slices.Contains(out, ipn.IP.String()) {
				out = append(out, ipn.IP.String())
			}
		}
	}
	return out
}
