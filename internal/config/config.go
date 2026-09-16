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

// FileName is the config file inside the AMail home directory.
const FileName = "config.json"

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
	NodeID           string  `json:"node_id"`
	Listen           string  `json:"listen"`
	Mailbox          string  `json:"mailbox"`
	ServerEligible   bool    `json:"server_eligible"`
	PollSeconds      int     `json:"poll_seconds"`
	DiscoverySeconds int     `json:"discovery_seconds"`
	MaxFileMB        int     `json:"max_file_mb"`
	Whitelist        []Peer  `json:"whitelist"`
	Blacklist        []Block `json:"blacklist"`

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
		Mailbox:          DefaultMailbox(),
		ServerEligible:   true,
		PollSeconds:      10,
		DiscoverySeconds: 30,
		MaxFileMB:        1024,
		Whitelist:        []Peer{},
		Blacklist:        []Block{},
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
	if c.Whitelist == nil {
		c.Whitelist = []Peer{}
	}
	if c.Blacklist == nil {
		c.Blacklist = []Block{}
	}
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
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return fmt.Errorf("listen %q: %w", c.Listen, err)
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

// ListenPort returns the numeric port from Listen.
func (c *Config) ListenPort() int {
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
