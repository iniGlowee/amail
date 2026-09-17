// Package config holds the on-disk configuration of an AMail node:
// its identity, where the mailbox folders live, and the whitelist /
// blacklist that decide who it will talk to.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// DefaultPort is the canonical AMail port.
const DefaultPort = 4444

// DefaultUIListen is where `amail run` serves the local web UI (loopback).
const DefaultUIListen = "127.0.0.1:4445"

// FileName is the config file inside the AMail home directory.
const FileName = "config.json"

// ListenOff as the Listen value makes a client-only node: it opens no port,
// can never be server, and still sends directly and pulls its own mail.
const ListenOff = "off"

var idRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// ValidID reports whether id is an acceptable node id. Node ids are used as
// folder names, certificate names and TLS server names, so they are kept to
// DNS-label rules: 1-63 lowercase letters, digits or hyphens.
func ValidID(id string) error {
	if !idRe.MatchString(id) {
		return fmt.Errorf("invalid node id %q: use 1-63 lowercase letters, digits or hyphens (e.g. austin-pc, ausa-web)", id)
	}
	return nil
}

// Peer is a whitelisted node. Host is optional: a node without a host (a
// laptop behind NAT, for example) can still send and pull mail through the
// server node but can never be elected server itself.
type Peer struct {
	ID   string `json:"id"`
	Host string `json:"host,omitempty"`
	Port int    `json:"port,omitempty"`
	Note string `json:"note,omitempty"`
}

// Addr returns host:port for dialing, or "" when the peer has no host.
func (p Peer) Addr() string {
	if p.Host == "" {
		return ""
	}
	port := p.Port
	if port == 0 {
		port = DefaultPort
	}
	return net.JoinHostPort(p.Host, strconv.Itoa(port))
}

// Block is a blacklist entry: a node id, a host name, an IP or a CIDR.
type Block struct {
	ID     string `json:"id,omitempty"`
	Host   string `json:"host,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Config is the node configuration (config.json in the AMail home).
type Config struct {
	NodeID           string   `json:"node_id"`
	Listen           string   `json:"listen"`
	UIListen         string   `json:"ui_listen"` // local web UI served by `amail run`; "off" disables
	Mailbox          string   `json:"mailbox"`
	ServerEligible   bool     `json:"server_eligible"`
	PollSeconds      int      `json:"poll_seconds"`
	DiscoverySeconds int      `json:"discovery_seconds"`
	MaxFileMB        int      `json:"max_file_mb"`
	MaxMailboxMB     int      `json:"max_mailbox_mb"`            // cap on inbox+forward+failed (received data)
	MinFreeMB        int      `json:"min_free_mb"`               // refuse files when the disk has less free
	MaxConnections   int      `json:"max_connections"`           // concurrent inbound connections
	MaxPerIPPerMin   int      `json:"max_per_ip_per_min"`        // failed handshakes per source IP per minute
	MaxPeerReqPerMin int      `json:"max_peer_req_per_min"`      // requests per authenticated peer per minute
	UIAllowRemote    bool     `json:"ui_allow_remote,omitempty"` // let ui_listen bind a non-loopback address
	Whitelist        []Peer   `json:"whitelist"`
	Blacklist        []Block  `json:"blacklist"`
	RevokedSerials   []string `json:"revoked_serials"` // certificate serials (hex) refused everywhere

	// Home is the directory this config was loaded from. Not serialised.
	Home string `json:"-"`
}

// DefaultHome returns the AMail home directory: $AMAIL_HOME, else
// %APPDATA%\AMail on Windows, else ~/.amail.
func DefaultHome() string {
	if h := os.Getenv("AMAIL_HOME"); h != "" {
		return h
	}
	if runtime.GOOS == "windows" {
		if a := os.Getenv("APPDATA"); a != "" {
			return filepath.Join(a, "AMail")
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".amail")
}

// DefaultMailbox returns the default mailbox root: ~/AMail on every OS.
func DefaultMailbox() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, "AMail")
}

// Default returns a config with sensible defaults for a new node.
func Default(home, nodeID string) *Config {
	return &Config{
		NodeID:           nodeID,
		Listen:           ":" + strconv.Itoa(DefaultPort),
		UIListen:         DefaultUIListen,
		Mailbox:          DefaultMailbox(),
		ServerEligible:   true,
		PollSeconds:      10,
		DiscoverySeconds: 30,
		MaxFileMB:        1024,
		MaxMailboxMB:     10240,
		MinFreeMB:        512,
		MaxConnections:   64,
		MaxPerIPPerMin:   20,
		MaxPeerReqPerMin: 600,
		Whitelist:        []Peer{},
		Blacklist:        []Block{},
		RevokedSerials:   []string{},
		Home:             home,
	}
}

// Path returns the config file path for a home directory.
func Path(home string) string { return filepath.Join(home, FileName) }

// Exists reports whether a config file exists in home.
func Exists(home string) bool {
	_, err := os.Stat(Path(home))
	return err == nil
}

// Load reads config.json from home.
func Load(home string) (*Config, error) {
	b, err := os.ReadFile(Path(home))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no config at %s (run: amail init)", Path(home))
		}
		return nil, err
	}
	c := &Config{}
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		b = b[3:] // UTF-8 BOM (Windows PowerShell 5.1 writes one)
	}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("%s: %w", Path(home), err)
	}
	c.Home = home
	c.normalise()
	return c, nil
}

func (c *Config) normalise() {
	if c.Listen == "" {
		c.Listen = ":" + strconv.Itoa(DefaultPort)
	}
	if c.UIListen == "" {
		c.UIListen = DefaultUIListen
	}
	if c.Mailbox == "" {
		c.Mailbox = DefaultMailbox()
	}
	if c.PollSeconds <= 0 {
		c.PollSeconds = 10
	}
	if c.DiscoverySeconds <= 0 {
		c.DiscoverySeconds = 30
	}
	if c.MaxFileMB <= 0 {
		c.MaxFileMB = 1024
	}
	if c.MaxMailboxMB <= 0 {
		c.MaxMailboxMB = 10240
	}
	if c.MinFreeMB <= 0 {
		c.MinFreeMB = 512
	}
	if c.MaxConnections <= 0 {
		c.MaxConnections = 64
	}
	if c.MaxPerIPPerMin <= 0 {
		c.MaxPerIPPerMin = 20
	}
	if c.MaxPeerReqPerMin <= 0 {
		c.MaxPeerReqPerMin = 600
	}
	if c.Whitelist == nil {
		c.Whitelist = []Peer{}
	}
	if c.Blacklist == nil {
		c.Blacklist = []Block{}
	}
	if c.RevokedSerials == nil {
		c.RevokedSerials = []string{}
	}
	for i, s := range c.RevokedSerials {
		c.RevokedSerials[i] = strings.ToLower(strings.TrimSpace(s))
	}
}

// IsRevoked reports whether a certificate serial (hex) is on the list.
func (c *Config) IsRevoked(serial string) bool {
	serial = strings.ToLower(strings.TrimSpace(serial))
	for _, s := range c.RevokedSerials {
		if s == serial {
			return true
		}
	}
	return false
}

// AddRevoked adds serials to the list; returns how many were new.
func (c *Config) AddRevoked(serials ...string) int {
	added := 0
	for _, s := range serials {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || len(s) > 64 || c.IsRevoked(s) {
			continue
		}
		c.RevokedSerials = append(c.RevokedSerials, s)
		added++
	}
	return added
}

// Save writes the config to its home directory.
func (c *Config) Save() error {
	if c.Home == "" {
		return errors.New("config has no home directory")
	}
	c.normalise()
	if err := os.MkdirAll(c.Home, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := Path(c.Home) + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, Path(c.Home))
}

// Validate checks the config for obvious mistakes.
func (c *Config) Validate() error {
	if err := ValidID(c.NodeID); err != nil {
		return err
	}
	if !c.ClientOnly() {
		if _, _, err := net.SplitHostPort(c.Listen); err != nil {
			return fmt.Errorf("listen %q: use host:port, :4444 or %q", c.Listen, ListenOff)
		}
	}
	seen := map[string]bool{}
	for _, p := range c.Whitelist {
		if err := ValidID(p.ID); err != nil {
			return fmt.Errorf("whitelist: %w", err)
		}
		if seen[p.ID] {
			return fmt.Errorf("whitelist: duplicate id %q", p.ID)
		}
		seen[p.ID] = true
	}
	return nil
}

// ClientOnly reports whether the node opens no port at all.
func (c *Config) ClientOnly() bool { return strings.EqualFold(strings.TrimSpace(c.Listen), ListenOff) }

// ListenPort returns the numeric port from Listen, or 0 for a client-only node.
func (c *Config) ListenPort() int {
	if c.ClientOnly() {
		return 0
	}
	_, p, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return DefaultPort
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		return DefaultPort
	}
	return n
}

// MaxBytes is the largest file the node accepts.
func (c *Config) MaxBytes() int64 { return int64(c.MaxFileMB) << 20 }

// MaxMailboxBytes is the cap on received data kept in the mailbox.
func (c *Config) MaxMailboxBytes() int64 { return int64(c.MaxMailboxMB) << 20 }

// MinFreeBytes is the disk free-space floor below which files are refused.
func (c *Config) MinFreeBytes() int64 { return int64(c.MinFreeMB) << 20 }

// Peer looks up a whitelist entry by id.
func (c *Config) Peer(id string) (Peer, bool) {
	for _, p := range c.Whitelist {
		if p.ID == id {
			return p, true
		}
	}
	return Peer{}, false
}

// AddPeer adds or updates a whitelist entry, keeping list order for existing ids.
func (c *Config) AddPeer(p Peer) {
	for i := range c.Whitelist {
		if c.Whitelist[i].ID == p.ID {
			if p.Host != "" {
				c.Whitelist[i].Host = p.Host
				c.Whitelist[i].Port = p.Port
			}
			if p.Note != "" {
				c.Whitelist[i].Note = p.Note
			}
			return
		}
	}
	c.Whitelist = append(c.Whitelist, p)
}

// RemovePeer deletes a whitelist entry. Returns false if it was not there.
func (c *Config) RemovePeer(id string) bool {
	for i := range c.Whitelist {
		if c.Whitelist[i].ID == id {
			c.Whitelist = append(c.Whitelist[:i], c.Whitelist[i+1:]...)
			return true
		}
	}
	return false
}

// AddBlock adds a blacklist entry (id or host).
func (c *Config) AddBlock(b Block) {
	for i := range c.Blacklist {
		if c.Blacklist[i].ID == b.ID && c.Blacklist[i].Host == b.Host {
			c.Blacklist[i].Reason = b.Reason
			return
		}
	}
	c.Blacklist = append(c.Blacklist, b)
}

// RemoveBlock deletes blacklist entries matching an id or host.
func (c *Config) RemoveBlock(idOrHost string) bool {
	kept := c.Blacklist[:0]
	removed := false
	for _, b := range c.Blacklist {
		if b.ID == idOrHost || b.Host == idOrHost {
			removed = true
			continue
		}
		kept = append(kept, b)
	}
	c.Blacklist = kept
	return removed
}

// IsBlocked reports whether a node id or remote address (host or ip) is
// blacklisted, and why.
func (c *Config) IsBlocked(id, addr string) (bool, string) {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	for _, b := range c.Blacklist {
		if b.ID != "" && id != "" && b.ID == id {
			return true, reason("node id "+id, b.Reason)
		}
		if b.Host == "" {
			continue
		}
		if strings.EqualFold(b.Host, host) {
			return true, reason("host "+host, b.Reason)
		}
		if ip != nil {
			if _, cidr, err := net.ParseCIDR(b.Host); err == nil && cidr.Contains(ip) {
				return true, reason("address "+host+" in "+b.Host, b.Reason)
			}
			if bip := net.ParseIP(b.Host); bip != nil && bip.Equal(ip) {
				return true, reason("address "+host, b.Reason)
			}
		}
	}
	return false, ""
}

func reason(what, why string) string {
	if why == "" {
		return what + " is blacklisted"
	}
	return what + " is blacklisted: " + why
}
