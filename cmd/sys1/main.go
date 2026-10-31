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
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/A-Hackers-Guide/sys1/internal/agent"
	"github.com/A-Hackers-Guide/sys1/internal/cli"
	"github.com/A-Hackers-Guide/sys1/internal/marketplace"
	"github.com/A-Hackers-Guide/sys1/internal/node"
	"github.com/A-Hackers-Guide/sys1/internal/paths"
	"github.com/A-Hackers-Guide/sys1/internal/protocol"
	"github.com/A-Hackers-Guide/sys1/internal/resources"
	"github.com/A-Hackers-Guide/sys1/internal/setup"
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
  sys1 open                 open the dashboard of a running node
  sys1 join-code            print the join code for new machines
  sys1 install|uninstall    run the node as a background service (starts at boot)
  sys1 start|stop|restart|status
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
	cmd, args := "serve", os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	sub := ""
	if cmd == "pkg" && len(args) > 0 {
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
	verbose := fs.Bool("v", false, "verbose logging")
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

	log := cli.Logger(*dataDir, "sys1", *verbose)
	cfg := node.Config{DataDir: *dataDir, ListenAddr: *listen, GUIAddr: *gui, PublicAddr: *public, TrustedKeys: append(trustKeys, readTrustedKeys(*dataDir)...)}
	svcArgs := []string{"serve", "--no-open", "--data", *dataDir, "--listen", *listen, "--gui", *gui}
	if *public != "" {
		svcArgs = append(svcArgs, "--public-addr", *public)
	}
	if *noLocal {
		svcArgs = append(svcArgs, "--no-local")
	}
	svc := &cli.Service{
		Name:        "sys1",
		DisplayName: "sys1 node",
		Description: "The sys1 node: pools spare capacity from every connected machine.",
		Args:        svcArgs,
		Run: func(ctx context.Context) error {
			return serve(ctx, cfg, log, true, !*noLocal)
		},
	}

	var err error
	switch cmd {
	case "serve":
		if *noOpen && !*foreground {
			err = svc.RunManaged()
			break
		}
		ctx, cancel := cli.SignalContext()
		err = serve(ctx, cfg, log, *noOpen, !*noLocal)
		cancel()
	case "open":
		// Print the link too: in a container or over SSH there is no browser.
		url := "http://" + *gui + "/?key=" + node.GUIKey(*dataDir)
		url = strings.Replace(url, "://0.0.0.0:", "://127.0.0.1:", 1)
		fmt.Println("Dashboard:", url)
		setup.OpenURL(url)
	case "join-code":
		err = printJoinCode(cfg)
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
			fmt.Printf("\n  sys1 node is running.\n\n  Dashboard:  %s\n  Join code:  %s\n\n  Press Ctrl+C to stop.\n\n", url, n.JoinCode())
			setup.OpenURL(url)
		}
		if local {
			go runLocalAgent(ctx, n, filepath.Join(cfg.DataDir, "local-client"), log)
		}
	})
}

// runLocalAgent lends the node's own machine to the pool, so a single
// computer is already a working pool.
func runLocalAgent(ctx context.Context, n *node.Node, dir string, log *slog.Logger) {
	log = log.With("component", "local-client")
	cfg, err := agent.LoadConfig(dir)
	code := n.LocalJoinCode()
	if err == nil && cfg.Fingerprint != code.Fingerprint {
		err = agent.ErrNotConfigured // node identity changed; rejoin
	}
	if err != nil {
		host, _ := os.Hostname()
		sampler := &resources.Sampler{DataDir: dir}
		cfg, err = agent.Register(ctx, code, host+" (node)", protocol.TrustOwner, sampler.Sample(true), version.Version)
		if err != nil {
			log.Error("could not start the node's own client", "err", err)
			return
		}
		agent.SaveConfig(dir, cfg)
	}
	cfg.NodeAddr = code.Addr // the port may have changed
	a, err := agent.New(dir, cfg, log)
	if err != nil {
		log.Error("could not start the node's own client", "err", err)
		return
	}
	a.Run(ctx)
}

// printJoinCode reads the code a running node saved, without touching
// the node's journal.
func printJoinCode(cfg node.Config) error {
	b, err := os.ReadFile(filepath.Join(cfg.DataDir, node.JoinCodeFile))
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
