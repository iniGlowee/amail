// Command amail is AMail (Armas Mail): a tiny whitelist/blacklist file
// mail network. Drop a file in outbox/<node>/ and it turns up in that
// node's inbox/<you>/, over TLS on port 4444, relayed by whichever node is
// currently the server.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/iniGlowee/amail/internal/config"
	"github.com/iniGlowee/amail/internal/gateway"
	"github.com/iniGlowee/amail/internal/keys"
	"github.com/iniGlowee/amail/internal/mailbox"
	"github.com/iniGlowee/amail/internal/node"
	"github.com/iniGlowee/amail/internal/proto"
	"github.com/iniGlowee/amail/internal/ui"
)

// version is set at build time: -ldflags "-X main.version=1.2.3".
var version = "dev"

// OperatorName is who issues network keys.
const OperatorName = "Austin Armas"

const usage = `AMail (Armas Mail) %s - file mail between machines you trust.

Usage: amail [--home DIR] <command> [args]

Node commands
  init [--id NAME] [--mailbox DIR] [--listen :4444]   create this node's home, config and mailbox folders
                                                     (--listen off = client-only: no open port, never server)
  request-key                                        show how to ask %s for a network key
  join <file.amailkey>                               install the network key you were given
  run                                                run the node (foreground; use the service scripts to keep it running)
                                                     also serves the local web UI at ui_listen (default http://127.0.0.1:4445)
  ui [--addr 127.0.0.1:4445] [--no-open]             open the web UI without running a node (read, send, delete, settings)
  gateway --config FILE [--once] [--init]            e-mail -> AMail: convert Maildir messages whose subject has "#amail <node>"
  status [id ...]                                    ask this node and every whitelisted peer who they are
  send <node-id> <file> [file ...]                   queue files in outbox/<node-id>/ for delivery
  peers [--merge]                                    list the server's whitelist, optionally adding unknown nodes to ours
  whitelist list | add <id> [host[:port]] [--note ..] | remove <id>
  blacklist list | add <id-or-host-or-cidr> [--reason ..] | remove <id-or-host>
  revoked list | add <serial> | remove <serial>      certificate serials this node refuses (spread to peers automatically)
  config                                             show where everything lives
  version

Operator commands (the person who runs the network)
  ca init --name NETWORK                             create the network certificate authority in <home>/ca
  ca issue <node-id> [--out FILE] [--days N] [--protect]   issue a key bundle (--protect seals it with $AMAIL_KEY_PASS)
  ca revoke <node-id> [--serial HEX]                 revoke a node's certificate(s); the network learns by gossip
  ca protect | unprotect                             seal / unseal the CA private key with $AMAIL_CA_PASS
  ca list                                            show issued and revoked keys

Environment
  AMAIL_HOME      node home (default %%APPDATA%%\AMail on Windows, ~/.amail elsewhere)
  AMAIL_DEBUG     set to 1 for chatty delivery logs
  AMAIL_CA_PASS   passphrase of a protected CA key (operator)
  AMAIL_KEY_PASS  passphrase of a sealed .amailkey bundle (ca issue --protect / join)
`

func main() {
	args := os.Args[1:]
	home := config.DefaultHome()
	for len(args) > 0 {
		switch {
		case args[0] == "--home" && len(args) > 1:
			home, args = args[1], args[2:]
			continue
		case strings.HasPrefix(args[0], "--home="):
			home, args = strings.TrimPrefix(args[0], "--home="), args[1:]
			continue
		}
		break
	}
	if len(args) == 0 {
		fmt.Printf(usage, version, OperatorName)
		os.Exit(2)
	}
	cmd, rest := args[0], args[1:]
	var err error
	switch cmd {
	case "init":
		err = cmdInit(home, rest)
	case "request-key":
		cfg, lerr := config.Load(home)
		if lerr != nil {
			err = lerr
		} else {
			printKeyRequest(cfg)
		}
	case "join":
		err = cmdJoin(home, rest)
	case "run":
		err = cmdRun(home)
	case "ui":
		err = cmdUI(home, rest)
	case "gateway":
		err = cmdGateway(home, rest)
	case "status":
		err = cmdStatus(home, rest)
	case "send":
		err = cmdSend(home, rest)
	case "peers":
		err = cmdPeers(home, rest)
	case "whitelist":
		err = cmdWhitelist(home, rest)
	case "blacklist":
		err = cmdBlacklist(home, rest)
	case "revoked":
		err = cmdRevoked(home, rest)
	case "config":
		err = cmdConfig(home)
	case "ca":
		err = cmdCA(home, rest)
	case "version", "--version", "-v":
		fmt.Printf("amail %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
	case "help", "--help", "-h":
		fmt.Printf(usage, version, OperatorName)
	default:
		fmt.Fprintf(os.Stderr, "amail: unknown command %q\n\n", cmd)
		fmt.Fprintf(os.Stderr, usage, version, OperatorName)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "amail: %v\n", err)
		if cmd == "run" || cmd == "ui" {
			// Unattended starts (services, scheduled tasks) have no console:
			// leave the reason where the operator will look.
			if f, ferr := os.OpenFile(filepath.Join(home, "amail.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); ferr == nil {
				fmt.Fprintf(f, "%s amail %s failed to start: %v (home %s)\n", time.Now().Format("2006/01/02 15:04:05"), cmd, err, home)
				_ = f.Close()
			}
		}
		os.Exit(1)
	}
}

// parseMixed parses flags that may appear before or after positional
// arguments (Go's flag package stops at the first positional) and returns
// the positionals.
func parseMixed(fs *flag.FlagSet, args []string) ([]string, error) {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			continue // let Parse report it
		}
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	if err := fs.Parse(flags); err != nil {
		return nil, err
	}
	return pos, nil
}

// --- init / keys ----------------------------------------------------------

func defaultID() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "node"
	}
	h = strings.ToLower(strings.Split(h, ".")[0])
	var b strings.Builder
	for _, r := range h {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	id := strings.Trim(b.String(), "-")
	if config.ValidID(id) != nil {
		return "node"
	}
	return id
}

func cmdInit(home string, args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	id := fs.String("id", "", "node id (default: this machine's host name)")
	mb := fs.String("mailbox", "", "mailbox folder (default: ~/AMail)")
	listen := fs.String("listen", "", "listen address (default :4444; \"off\" for a client-only node that opens no port)")
	force := fs.Bool("force", false, "overwrite an existing config")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if config.Exists(home) && !*force {
		return fmt.Errorf("%s already exists (use --force to overwrite, or 'amail config' to see it)", config.Path(home))
	}
	if *id == "" {
		*id = defaultID()
	}
	if err := config.ValidID(*id); err != nil {
		return err
	}
	cfg := config.Default(home, *id)
	if *mb != "" {
		abs, err := filepath.Abs(*mb)
		if err != nil {
			return err
		}
		cfg.Mailbox = abs
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	if _, err := mailbox.Open(cfg.Mailbox); err != nil {
		return err
	}
	fmt.Printf("Node %q initialised.\n  config:  %s\n  mailbox: %s\n\n", cfg.NodeID, config.Path(home), cfg.Mailbox)
	if keys.Installed(home) {
		fmt.Println("A network key is already installed. Next: amail whitelist add ..., then amail run")
		return nil
	}
	printKeyRequest(cfg)
	return nil
}

func printKeyRequest(cfg *config.Config) {
	host, _ := os.Hostname()
	fmt.Printf(`This node needs a network key before it can talk to anyone.

Send this to the network operator, %s (see the project README for contact details):

    Subject:   AMail key request: %s
    Node id:   %s
    Machine:   %s (%s/%s)
    Reachable: <public host or IP if this machine can accept connections on port %d, otherwise "not reachable">

You will get back a file named %s.amailkey. Install it with:

    amail join %s.amailkey

The operator will also tell you which nodes to whitelist:

    amail whitelist add <id> [host]

Then start the node:

    amail run
`, OperatorName, cfg.NodeID, cfg.NodeID, host, runtime.GOOS, runtime.GOARCH, cfg.ListenPort(), cfg.NodeID, cfg.NodeID)
}

func cmdJoin(home string, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: amail join <file.amailkey>")
	}
	pass := os.Getenv(keys.KeyPassEnv)
	if keys.BundleSealed(args[0]) && pass == "" {
		return fmt.Errorf("%s is passphrase-protected: set %s=<passphrase> and run again", args[0], keys.KeyPassEnv)
	}
	b, err := keys.ReadBundle(args[0], pass)
	if err != nil {
		return err
	}
	if !config.Exists(home) {
		cfg := config.Default(home, b.NodeID)
		if err := cfg.Save(); err != nil {
			return err
		}
		if _, err := mailbox.Open(cfg.Mailbox); err != nil {
			return err
		}
		fmt.Printf("Created %s for node %q.\n", config.Path(home), b.NodeID)
	} else {
		cfg, err := config.Load(home)
		if err != nil {
			return err
		}
		if cfg.NodeID != b.NodeID {
			fmt.Printf("Note: config node_id %q changed to %q to match the key.\n", cfg.NodeID, b.NodeID)
			cfg.NodeID = b.NodeID
			if err := cfg.Save(); err != nil {
				return err
			}
		}
	}
	if err := keys.Install(home, b); err != nil {
		return err
	}
	fmt.Printf("Network key installed: node %q on network %q, expires %s.\n", b.NodeID, b.Network, b.Expires)
	fmt.Println("Next: amail whitelist add <id> [host]   then   amail run")
	return nil
}

// --- run ------------------------------------------------------------------

func loadNode(home string) (*config.Config, *keys.Material, error) {
	cfg, err := config.Load(home)
	if err != nil {
		return nil, nil, err
	}
	mat, err := keys.Load(home)
	if err != nil {
		return nil, nil, err
	}
	return cfg, mat, nil
}

func cmdRun(home string) error {
	cfg0, _, err := loadNode(home)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return err
	}
	var w io.Writer = os.Stdout
	if f, err := os.OpenFile(filepath.Join(home, "amail.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		defer f.Close()
		w = io.MultiWriter(os.Stdout, f)
	}
	logger := log.New(w, "", log.LstdFlags)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Local web UI, served alongside the node. Settings saved there ask for
	// a node restart through restartCh; the process (and the UI) keep running.
	restartCh := make(chan struct{}, 1)
	var srv *ui.Server
	if !ui.Off(cfg0.UIListen) {
		srv = &ui.Server{Home: home, Version: version, AllowRemote: cfg0.UIAllowRemote, Restart: func() {
			select {
			case restartCh <- struct{}{}:
			default:
			}
		}}
		go func() {
			if err := srv.ListenAndServe(ctx, cfg0.UIListen); err != nil {
				logger.Printf("ui: %v (set ui_listen to \"off\" to silence)", err)
			}
		}()
		logger.Printf("ui: http://%s (loopback only; change ui_listen in config.json)", cfg0.UIListen)
	}

	for {
		cfg, mat, err := loadNode(home)
		if err != nil {
			return err
		}
		n, err := node.New(node.Options{Config: cfg, Material: mat, Logger: logger, Version: version})
		if err != nil {
			return err
		}
		if srv != nil {
			srv.SetLocal(n)
		}
		nctx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			_ = n.Run(nctx)
			close(done)
		}()
		select {
		case <-ctx.Done():
			cancel()
			<-done
			return nil
		case <-restartCh:
			logger.Printf("settings changed: restarting node")
			cancel()
			<-done
			if srv != nil {
				srv.SetLocal(nil)
			}
			time.Sleep(300 * time.Millisecond)
		}
	}
}

func cmdUI(home string, args []string) error {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	addr := fs.String("addr", "", "address to serve on (default: ui_listen from config, or 127.0.0.1:4445)")
	noOpen := fs.Bool("no-open", false, "do not open the browser")
	if _, err := parseMixed(fs, args); err != nil {
		return err
	}
	cfg, err := config.Load(home)
	if err != nil {
		return err
	}
	if *addr == "" {
		*addr = cfg.UIListen
		if ui.Off(*addr) {
			*addr = config.DefaultUIListen
		}
	}
	url := "http://" + *addr
	srv := &ui.Server{Home: home, Version: version, AllowRemote: cfg.UIAllowRemote}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		// Most likely the running node already serves the UI there.
		fmt.Printf("%s is busy (%v).\nIf 'amail run' is up it already serves the UI at %s; opening that.\n", *addr, err, url)
		if !*noOpen {
			ui.OpenBrowser(url)
		}
		return nil
	}
	_ = ln.Close()
	fmt.Printf("AMail UI for node %q at %s  (Ctrl+C to stop)\n", cfg.NodeID, url)
	if !*noOpen {
		ui.OpenBrowser(url)
	}
	return srv.ListenAndServe(ctx, *addr)
}

func cmdGateway(home string, args []string) error {
	fs := flag.NewFlagSet("gateway", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "gateway config file (JSON)")
	once := fs.Bool("once", false, "scan once and exit")
	initCfg := fs.Bool("init", false, "write an example config to --config and exit")
	if _, err := parseMixed(fs, args); err != nil {
		return err
	}
	if *cfgPath == "" {
		*cfgPath = filepath.Join(home, "gateway.json")
	}
	if *initCfg {
		if _, err := os.Stat(*cfgPath); err == nil {
			return fmt.Errorf("%s already exists", *cfgPath)
		}
		example := gateway.Default()
		example.Maildir = "/home/you/Mail/example.com/info"
		example.Outbox = filepath.Join(config.DefaultMailbox(), "outbox")
		example.AllowedSenders = []string{"you@example.com", "@yourcompany.example"}
		example.State = *cfgPath + ".state"
		js, _ := json.MarshalIndent(example, "", "  ")
		if err := os.MkdirAll(filepath.Dir(*cfgPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(*cfgPath, append(js, '\n'), 0o600); err != nil {
			return err
		}
		fmt.Printf("Example gateway config written to %s. Edit maildir, outbox and allowed_senders, then: amail gateway --config %s\n", *cfgPath, *cfgPath)
		return nil
	}
	cfg, err := gateway.Load(*cfgPath)
	if err != nil {
		return err
	}
	if *once {
		cfg.PollSeconds = 0
	}
	logger := log.New(os.Stdout, "", log.LstdFlags)
	g, err := gateway.New(cfg, logger)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	g.Run(ctx.Done())
	return nil
}

// --- status ---------------------------------------------------------------

func cmdStatus(home string, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "machine readable output")
	ids, err := parseMixed(fs, args)
	if err != nil {
		return err
	}
	cfg, mat, err := loadNode(home)
	if err != nil {
		return err
	}
	cli := node.NewClient(mat)
	cli.Timeout = 5 * time.Second
	cli.Revoked = cfg.IsRevoked
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	type row struct {
		Peer   config.Peer
		Status *proto.Status
		Err    string
	}
	var rows []row
	local := config.Peer{ID: cfg.NodeID, Host: "127.0.0.1", Port: cfg.ListenPort(), Note: "this machine"}
	if len(want) == 0 || want[cfg.NodeID] {
		if cfg.ClientOnly() {
			rows = append(rows, row{Peer: config.Peer{ID: cfg.NodeID, Note: "this machine"}, Err: "client-only (listen: off); see the server's view of it"})
		} else {
			rows = append(rows, probe(cli, local))
		}
	}
	for _, p := range cfg.Whitelist {
		if p.ID == cfg.NodeID || (len(want) > 0 && !want[p.ID]) {
			continue
		}
		rows = append(rows, probe(cli, p))
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NODE\tHOST\tROLE\tSERVER\tVERSION\tOS\tUP\tIN/OUT/FWD\tNOTE")
	for _, r := range rows {
		host := r.Peer.Host
		if host == "" {
			host = "(no host)"
		}
		if r.Status == nil {
			fmt.Fprintf(tw, "%s\t%s\t-\t-\t-\t-\t-\t-\t%s\n", r.Peer.ID, host, r.Err)
			continue
		}
		s := r.Status
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s/%s\t%s\t%d/%d/%d\t%s\n", s.NodeID, host, s.Role, s.Server, s.Version, s.OS, s.Arch, s.Uptime, s.Inbox, s.Outbox, s.Forward, r.Peer.Note)
	}
	return tw.Flush()
}

func probe(cli *node.Client, p config.Peer) (r struct {
	Peer   config.Peer
	Status *proto.Status
	Err    string
}) {
	r.Peer = p
	if p.Host == "" {
		r.Err = "no host: reachable only through the server"
		return r
	}
	ctx, cancel := context.WithTimeout(context.Background(), cli.Timeout)
	defer cancel()
	st, err := cli.Status(ctx, p)
	if err != nil {
		r.Err = shortErr(err)
		return r
	}
	r.Status = st
	return r
}

func shortErr(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "refused"):
		return "not running / port closed"
	case strings.Contains(s, "timeout") || strings.Contains(s, "deadline"):
		return "no answer (firewall or offline)"
	case strings.Contains(s, "no such host"):
		return "unknown host"
	}
	return s
}

// --- send / peers ---------------------------------------------------------

func cmdSend(home string, args []string) error {
	if len(args) < 2 {
		return errors.New("usage: amail send <node-id> <file> [file ...]")
	}
	cfg, err := config.Load(home)
	if err != nil {
		return err
	}
	to := args[0]
	if err := config.ValidID(to); err != nil {
		return err
	}
	if _, ok := cfg.Peer(to); !ok && to != cfg.NodeID {
		fmt.Printf("Warning: %q is not on the whitelist; the file will fail until you run: amail whitelist add %s [host]\n", to, to)
	}
	mb, err := mailbox.Open(cfg.Mailbox)
	if err != nil {
		return err
	}
	for _, f := range args[1:] {
		dest, err := mb.Drop(to, f)
		if err != nil {
			return err
		}
		fmt.Printf("queued %s -> %s\n", f, dest)
	}
	fmt.Println("The running node will deliver it; watch 'amail status' or sent/ and failed/.")
	return nil
}

func findServer(cfg *config.Config, cli *node.Client) (config.Peer, *proto.Status, error) {
	for _, p := range cfg.Whitelist {
		if p.Host == "" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), cli.Timeout)
		st, err := cli.Status(ctx, p)
		cancel()
		if err != nil {
			continue
		}
		if st.Role == node.RoleServer {
			return p, st, nil
		}
		if sp, ok := cfg.Peer(st.Server); ok && sp.Host != "" {
			ctx, cancel := context.WithTimeout(context.Background(), cli.Timeout)
			st2, err := cli.Status(ctx, sp)
			cancel()
			if err == nil {
				return sp, st2, nil
			}
		}
		return p, st, nil
	}
	return config.Peer{}, nil, errors.New("no whitelisted peer answered")
}

func cmdPeers(home string, args []string) error {
	fs := flag.NewFlagSet("peers", flag.ContinueOnError)
	merge := fs.Bool("merge", false, "add nodes we do not know to our whitelist")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, mat, err := loadNode(home)
	if err != nil {
		return err
	}
	cli := node.NewClient(mat)
	cli.Timeout = 5 * time.Second
	cli.Revoked = cfg.IsRevoked
	sp, _, err := findServer(cfg, cli)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), cli.Timeout)
	defer cancel()
	peers, err := cli.Peers(ctx, sp)
	if err != nil {
		return err
	}
	fmt.Printf("Whitelist of %s:\n", sp.ID)
	added := 0
	for _, p := range peers {
		_, known := cfg.Peer(p.ID)
		mark := " "
		if !known && p.ID != cfg.NodeID {
			mark = "+"
			if *merge {
				cfg.AddPeer(config.Peer{ID: p.ID, Host: p.Host, Port: p.Port, Note: "from " + sp.ID})
				added++
			}
		}
		host := p.Host
		if host == "" {
			host = "(no host)"
		}
		fmt.Printf("  %s %-24s %s\n", mark, p.ID, host)
	}
	if *merge && added > 0 {
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("Added %d node(s) to %s\n", added, config.Path(home))
	} else if !*merge {
		fmt.Println("(+ = not on our whitelist; rerun with --merge to add them)")
	}
	return nil
}

// --- whitelist / blacklist ------------------------------------------------

func parseHostPort(s string) (string, int, error) {
	if s == "" {
		return "", 0, nil
	}
	if h, p, err := net.SplitHostPort(s); err == nil {
		n, err := strconv.Atoi(p)
		if err != nil {
			return "", 0, fmt.Errorf("bad port in %q", s)
		}
		return h, n, nil
	}
	return s, 0, nil
}

func cmdWhitelist(home string, args []string) error {
	cfg, err := config.Load(home)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list", "ls":
		if len(cfg.Whitelist) == 0 {
			fmt.Println("whitelist is empty (any node holding a network key may connect)")
			return nil
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "#\tID\tHOST\tNOTE")
		for i, p := range cfg.Whitelist {
			host := p.Addr()
			if host == "" {
				host = "(no host)"
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", i+1, p.ID, host, p.Note)
		}
		return tw.Flush()
	case "add":
		fs := flag.NewFlagSet("whitelist add", flag.ContinueOnError)
		note := fs.String("note", "", "free text")
		rest, err := parseMixed(fs, args[1:])
		if err != nil {
			return err
		}
		if len(rest) < 1 {
			return errors.New("usage: amail whitelist add <id> [host[:port]] [--note ...]")
		}
		id := rest[0]
		if err := config.ValidID(id); err != nil {
			return err
		}
		host, port := "", 0
		if len(rest) > 1 {
			host, port, err = parseHostPort(rest[1])
			if err != nil {
				return err
			}
		}
		cfg.AddPeer(config.Peer{ID: id, Host: host, Port: port, Note: *note})
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("whitelisted %s %s\n", id, host)
		return nil
	case "remove", "rm":
		if len(args) != 2 {
			return errors.New("usage: amail whitelist remove <id>")
		}
		if !cfg.RemovePeer(args[1]) {
			return fmt.Errorf("%s is not on the whitelist", args[1])
		}
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("removed %s\n", args[1])
		return nil
	}
	return fmt.Errorf("unknown whitelist action %q", args[0])
}

func cmdBlacklist(home string, args []string) error {
	cfg, err := config.Load(home)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list", "ls":
		if len(cfg.Blacklist) == 0 {
			fmt.Println("blacklist is empty")
			return nil
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tHOST\tREASON")
		for _, b := range cfg.Blacklist {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", b.ID, b.Host, b.Reason)
		}
		return tw.Flush()
	case "add":
		fs := flag.NewFlagSet("blacklist add", flag.ContinueOnError)
		why := fs.String("reason", "", "why")
		rest, err := parseMixed(fs, args[1:])
		if err != nil {
			return err
		}
		if len(rest) != 1 {
			return errors.New("usage: amail blacklist add <id-or-host-or-cidr> [--reason ...]")
		}
		b := config.Block{Reason: *why}
		if config.ValidID(rest[0]) == nil && !strings.Contains(rest[0], ".") {
			b.ID = rest[0]
		} else {
			b.Host = rest[0]
		}
		cfg.AddBlock(b)
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("blacklisted %s\n", rest[0])
		return nil
	case "remove", "rm":
		if len(args) != 2 {
			return errors.New("usage: amail blacklist remove <id-or-host>")
		}
		if !cfg.RemoveBlock(args[1]) {
			return fmt.Errorf("%s is not on the blacklist", args[1])
		}
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("removed %s\n", args[1])
		return nil
	}
	return fmt.Errorf("unknown blacklist action %q", args[0])
}

func cmdRevoked(home string, args []string) error {
	cfg, err := config.Load(home)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list", "ls":
		if len(cfg.RevokedSerials) == 0 {
			fmt.Println("no revoked certificates known")
			return nil
		}
		for _, s := range cfg.RevokedSerials {
			fmt.Println(s)
		}
		return nil
	case "add":
		if len(args) < 2 {
			return errors.New("usage: amail revoked add <serial-hex> [...]")
		}
		n := cfg.AddRevoked(args[1:]...)
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("added %d serial(s); the running node spreads them to every peer it talks to\n", n)
		return nil
	case "remove", "rm":
		if len(args) != 2 {
			return errors.New("usage: amail revoked remove <serial-hex>")
		}
		kept := cfg.RevokedSerials[:0]
		for _, s := range cfg.RevokedSerials {
			if s != strings.ToLower(args[1]) {
				kept = append(kept, s)
			}
		}
		cfg.RevokedSerials = kept
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Println("removed locally (other nodes will teach it back unless they remove it too)")
		return nil
	}
	return fmt.Errorf("unknown revoked action %q", args[0])
}

// --- config ---------------------------------------------------------------

func cmdConfig(home string) error {
	cfg, err := config.Load(home)
	if err != nil {
		return err
	}
	fmt.Printf("home:     %s\nconfig:   %s\nmailbox:  %s\nlog:      %s\n", home, config.Path(home), cfg.Mailbox, filepath.Join(home, "amail.log"))
	if mat, err := keys.Load(home); err == nil {
		fmt.Printf("key:      node %q on network %q, expires %s\n", mat.NodeID, mat.Network, mat.NotAfter.Format("2006-01-02"))
	} else {
		fmt.Printf("key:      NOT INSTALLED (amail request-key)\n")
	}
	if info, err := keys.LoadCAInfo(keys.CADir(home)); err == nil {
		fmt.Printf("operator: this machine holds the CA for network %q (%s)\n", info.Network, info.Dir)
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	fmt.Printf("\n%s\n", b)
	return nil
}

// --- ca -------------------------------------------------------------------

func cmdCA(home string, args []string) error {
	dir := keys.CADir(home)
	if len(args) == 0 {
		return errors.New("usage: amail ca init|issue|list")
	}
	switch args[0] {
	case "init":
		fs := flag.NewFlagSet("ca init", flag.ContinueOnError)
		name := fs.String("name", "", "network name, e.g. armas")
		if _, err := parseMixed(fs, args[1:]); err != nil {
			return err
		}
		if *name == "" {
			return errors.New("usage: amail ca init --name <network>")
		}
		if err := keys.InitCA(dir, *name); err != nil {
			return err
		}
		fmt.Printf("Network CA for %q created in %s\nKeep %s private. Issue node keys with: amail ca issue <node-id>\n", *name, dir, filepath.Join(dir, keys.CAKey))
		return nil
	case "issue":
		fs := flag.NewFlagSet("ca issue", flag.ContinueOnError)
		out := fs.String("out", "", "output file (default <node-id>.amailkey)")
		days := fs.Int("days", keys.DefaultValidDays, "validity in days")
		by := fs.String("by", OperatorName, "issuer name recorded in the bundle")
		protect := fs.Bool("protect", false, "seal the bundle with the passphrase in "+keys.KeyPassEnv)
		rest, err := parseMixed(fs, args[1:])
		if err != nil {
			return err
		}
		if len(rest) != 1 {
			return errors.New("usage: amail ca issue <node-id> [--out FILE] [--days N] [--protect]")
		}
		id := rest[0]
		pass := ""
		if *protect {
			pass = os.Getenv(keys.KeyPassEnv)
			if len(pass) < 8 {
				return fmt.Errorf("--protect needs %s set to a passphrase of at least 8 characters", keys.KeyPassEnv)
			}
		}
		b, err := keys.Issue(dir, id, *days, *by)
		if err != nil {
			return err
		}
		if *out == "" {
			*out = id + ".amailkey"
		}
		if err := keys.WriteBundle(b, *out, pass); err != nil {
			return err
		}
		fmt.Printf("Issued key for %q on network %q -> %s (expires %s)\n", id, b.Network, *out, b.Expires)
		if pass != "" {
			fmt.Printf("The file is sealed. Tell the owner the passphrase separately; they run: %s=... amail join %s\n", keys.KeyPassEnv, *out)
		} else {
			fmt.Println("Send that file to the node's owner over a channel you trust (it holds a private key). They install it with: amail join", *out)
			fmt.Println("Tip: --protect seals it with a passphrase so it can travel by email.")
		}
		fmt.Println("Remind everyone to add the new node: amail whitelist add", id, "[host]")
		return nil
	case "revoke":
		fs := flag.NewFlagSet("ca revoke", flag.ContinueOnError)
		serial := fs.String("serial", "", "revoke this serial only (default: every certificate issued to the id)")
		rest, err := parseMixed(fs, args[1:])
		if err != nil {
			return err
		}
		if len(rest) != 1 {
			return errors.New("usage: amail ca revoke <node-id> [--serial HEX]")
		}
		id := rest[0]
		var serials []string
		if *serial != "" {
			serials = []string{strings.ToLower(*serial)}
		} else {
			b, err := os.ReadFile(filepath.Join(dir, keys.IssuedLog))
			if err != nil {
				return fmt.Errorf("no issued.log in %s", dir)
			}
			for _, line := range strings.Split(string(b), "\n") {
				f := strings.Split(line, "\t")
				if len(f) >= 3 && f[1] == id && strings.HasPrefix(f[2], "serial=") {
					serials = append(serials, strings.ToLower(strings.TrimPrefix(f[2], "serial=")))
				}
			}
			if len(serials) == 0 {
				return fmt.Errorf("no certificate issued to %q found in issued.log; use --serial", id)
			}
		}
		rf, err := os.OpenFile(filepath.Join(dir, "revoked.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		for _, s := range serials {
			fmt.Fprintf(rf, "%s\t%s\t%s\n", s, id, time.Now().UTC().Format(time.RFC3339))
		}
		_ = rf.Close()
		// Start the gossip from this machine's own node, if it has one.
		if cfg, err := config.Load(home); err == nil {
			n := cfg.AddRevoked(serials...)
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Printf("Revoked %d certificate(s) of %q; %d added to this node's list. Every node learns it on its next contact with a node that knows.\n", len(serials), id, n)
		} else {
			fmt.Printf("Revoked %d certificate(s) of %q. Add them on a node with: amail revoked add <serial>\n", len(serials), id)
		}
		for _, s := range serials {
			fmt.Println("  ", s)
		}
		fmt.Printf("If %s should stay on the network, issue it a fresh key: amail ca issue %s\n", id, id)
		return nil
	case "protect", "unprotect":
		pass := os.Getenv(keys.CAPassEnv)
		if len(pass) < 8 {
			return fmt.Errorf("set %s to a passphrase of at least 8 characters first", keys.CAPassEnv)
		}
		if args[0] == "protect" {
			if err := keys.ProtectCA(dir, pass); err != nil {
				return err
			}
			fmt.Printf("CA key sealed. From now on 'amail ca issue' and 'amail ca revoke' need %s. Keep the passphrase somewhere safe: without it the network can issue no new keys.\n", keys.CAPassEnv)
			return nil
		}
		if err := keys.UnprotectCA(dir, pass); err != nil {
			return err
		}
		fmt.Println("CA key is now stored in the clear again.")
		return nil
	case "list":
		info, err := keys.LoadCAInfo(dir)
		if err != nil {
			return err
		}
		sealedNote := "key stored in the clear (consider: amail ca protect)"
		if keys.CASealed(dir) {
			sealedNote = "key passphrase-protected"
		}
		fmt.Printf("Network %q, CA valid until %s, %s\n", info.Network, info.NotAfter.Format("2006-01-02"), sealedNote)
		if rb, err := os.ReadFile(filepath.Join(dir, "revoked.txt")); err == nil {
			fmt.Printf("Revoked:\n%s", rb)
		}
		b, err := os.ReadFile(filepath.Join(dir, keys.IssuedLog))
		if err != nil {
			fmt.Println("no keys issued yet")
			return nil
		}
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		sort.Strings(lines)
		for _, l := range lines {
			fmt.Println(" ", l)
		}
		return nil
	}
	return fmt.Errorf("unknown ca action %q", args[0])
}
