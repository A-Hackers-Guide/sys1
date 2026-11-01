// Command sys0 is the sys1 client. It lends this machine's spare capacity
// to a sys1 node, at the lowest priority, invisibly, in the background.
//
//	sys0                    open the graphical setup wizard
//	sys0 join <code>        join a node from the command line
//	sys0 status             show what sys0 is doing
//	sys0 uninstall          stop and remove the background service
//
// Run `sys0 help` for everything.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/agent"
	"github.com/A-Hackers-Guide/sys1/internal/cli"
	"github.com/A-Hackers-Guide/sys1/internal/egress"
	"github.com/A-Hackers-Guide/sys1/internal/paths"
	"github.com/A-Hackers-Guide/sys1/internal/priority"
	"github.com/A-Hackers-Guide/sys1/internal/protocol"
	"github.com/A-Hackers-Guide/sys1/internal/resources"
	"github.com/A-Hackers-Guide/sys1/internal/setup"
	"github.com/A-Hackers-Guide/sys1/internal/version"
)

const usage = `sys0: lend your machine's spare power to a sys1 pool

Usage:
  sys0 [setup]              open the graphical setup wizard (default)
  sys0 join <code>          join a node without the wizard
        --name <name>         name shown on the node (default: hostname)
        --trust owner|community
                              "owner" if you run the node yourself (default: community)
        --max-tasks <n>       most tasks at once (default 4)
        --storage-gb <n>      disk space to lend for pooled storage (default 10, 0 = none)
        --open-internet       let apps from other people's pools use this machine's
                              internet freely (default: only the hosts each app lists)
        --no-service          do not install the background service
  sys0 run                  run in the foreground (what the service runs)
  sys0 status               show settings and service state
  sys0 install|uninstall    add or remove the background service
  sys0 start|stop|restart   control the background service
  sys0 version              print the version

Every command accepts --data <dir> (default: %s).
`

func main() {
	// First thing: drop to the lowest priority, before any other threads
	// exist, so everything sys0 does or starts inherits it.
	priority.Lowest()
	paths.FixDNS()

	cmd, args := "setup", os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("sys0 "+cmd, flag.ExitOnError)
	dataDir := fs.String("data", paths.DataDir("sys0"), "data directory")
	verbose := fs.Bool("v", false, "verbose logging")
	name := fs.String("name", "", "machine name")
	trust := fs.String("trust", string(protocol.TrustCommunity), "owner or community")
	maxTasks := fs.Int("max-tasks", 4, "most tasks at once")
	noService := fs.Bool("no-service", false, "do not install the service")
	storageGB := fs.Int("storage-gb", agent.DefaultStorageGB, "disk space to lend for pooled storage, in GB (0 = none)")
	noBrowser := fs.Bool("no-browser", false, "do not open a browser")
	openInternet := fs.Bool("open-internet", false, "let apps from other people's pools use this machine's internet freely")
	listenAddr := fs.String("listen", ":3128", "egress-proxy: proxy address")
	allow := fs.String("allow", "", "egress-proxy: hosts apps may reach, comma-separated")
	var forwards multiFlag
	fs.Var(&forwards, "forward", "egress-proxy: port:host:port to forward inbound (repeatable)")
	fs.Usage = func() { fmt.Fprintf(os.Stderr, usage, paths.DataDir("sys0")) }
	// Allow `sys0 join <code> --name x` as well as `sys0 join --name x <code>`.
	var positional []string
	for len(args) > 0 {
		fs.Parse(args)
		args = fs.Args()
		if len(args) > 0 {
			positional = append(positional, args[0])
			args = args[1:]
		}
	}

	log := cli.Logger(*dataDir, "sys0", *verbose)
	svc := &cli.Service{
		Name:        "sys0",
		DisplayName: "sys0 (sys1 pool client)",
		Description: "Lends this machine's spare capacity to a sys1 pool at the lowest priority.",
		Args:        []string{"run", "--data", *dataDir},
		Run: func(ctx context.Context) error {
			return runAgent(ctx, *dataDir, log)
		},
	}

	var err error
	switch cmd {
	case "setup":
		err = setup.Run(context.Background(), setup.Options{
			DataDir: *dataDir, Log: log, OpenBrowser: !*noBrowser,
			Install: func() error {
				if *noService {
					return nil
				}
				return svc.Install()
			},
		})
	case "join":
		if len(positional) != 1 {
			err = errors.New("usage: sys0 join <join code>")
			break
		}
		err = join(positional[0], *dataDir, *name, protocol.Trust(*trust), *maxTasks, *storageGB, *openInternet)
		if err == nil && !*noService {
			err = svc.Install()
		}
		switch {
		case err != nil:
		case *noService:
			fmt.Println("Joined. Start it with: sys0 run")
		default:
			fmt.Println("Joined. sys0 is running in the background.")
		}
	case "run":
		err = svc.RunManaged()
	case "egress-proxy":
		err = egressProxy(*listenAddr, *allow, forwards)
	case "install":
		err = svc.Install()
	case "uninstall":
		err = svc.Uninstall()
		if err == nil {
			fmt.Println("sys0 service removed. Settings remain in", *dataDir)
		}
	case "start", "stop", "restart":
		err = svc.Control(cmd)
	case "status":
		status(*dataDir, svc)
	case "version", "--version":
		fmt.Println("sys0", version.String())
	case "help", "-h", "--help":
		fs.Usage()
	default:
		fs.Usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sys0:", err)
		os.Exit(1)
	}
}

// runAgent waits for setup to finish (installers may start the service
// before the user has joined a node), then runs until ctx ends.
func runAgent(ctx context.Context, dataDir string, log *slog.Logger) error {
	for {
		cfg, err := agent.LoadConfig(dataDir)
		if errors.Is(err, agent.ErrNotConfigured) {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(10 * time.Second):
				continue
			}
		}
		if err != nil {
			return err
		}
		a, err := agent.New(dataDir, cfg, log)
		if err != nil {
			return err
		}
		return a.Run(ctx)
	}
}

func join(code, dataDir, name string, trust protocol.Trust, maxTasks, storageGB int, openInternet bool) error {
	jc, err := protocol.ParseJoinCode(code)
	if err != nil {
		return err
	}
	if !trust.Valid() {
		return errors.New(`--trust must be "owner" or "community"`)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sampler := &resources.Sampler{DataDir: dataDir}
	cfg, err := agent.Register(ctx, jc, name, trust, sampler.Sample(trust == protocol.TrustOwner), version.Version)
	if err != nil {
		return fmt.Errorf("could not join the node: %w", err)
	}
	if maxTasks > 0 {
		cfg.MaxTasks = maxTasks
	}
	if storageGB >= 0 {
		cfg.StorageGB = storageGB
	}
	cfg.OpenInternet = openInternet
	return agent.SaveConfig(dataDir, cfg)
}

func status(dataDir string, svc *cli.Service) {
	fmt.Println("sys0", version.Version)
	fmt.Println("  data:   ", dataDir)
	fmt.Println("  service:", svc.Status())
	cfg, err := agent.LoadConfig(dataDir)
	if err != nil {
		fmt.Println("  pool:    not joined (run `sys0 setup`)")
		return
	}
	fmt.Println("  node:   ", cfg.NodeAddr)
	fmt.Println("  name:   ", cfg.Name)
	fmt.Println("  trust:  ", cfg.Trust)
	fmt.Println("  max tasks:", cfg.MaxTasks)
	fmt.Println("  storage: ", cfg.StorageGB, "GB lent to the pool")
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// egressProxy runs the sandbox gateway (inside sys1's container image).
func egressProxy(listen, allow string, forwards []string) error {
	ctx, cancel := cli.SignalContext()
	defer cancel()
	var hosts []string
	for _, h := range strings.Split(allow, ",") {
		if h = strings.TrimSpace(h); h != "" {
			if !egress.ValidHost(h) {
				return fmt.Errorf("invalid host %q", h)
			}
			hosts = append(hosts, h)
		}
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	for _, f := range forwards {
		l, target, err := egress.ParseForward(f)
		if err != nil {
			return err
		}
		ln, err := net.Listen("tcp", l)
		if err != nil {
			return err
		}
		go egress.Forward(ctx, ln, target)
	}
	srv := &http.Server{Addr: listen, Handler: &egress.Proxy{Policy: egress.Policy{Hosts: hosts}, Log: log}, ReadHeaderTimeout: 10 * time.Second}
	go func() { <-ctx.Done(); srv.Close() }()
	log.Info("egress gateway ready", "allow", hosts, "forwards", forwards)
	if err := srv.ListenAndServe(); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}
