// Package mailbox is the on-disk side of AMail: plain folders that people
// and programs read and write with normal file tools.
//
//	<mailbox>/
//	  inbox/<from>/...        files that arrived, one folder per sender
//	  outbox/<to>/...         drop files here to send them to <to>
//	  sent/<to>/...           outbox files that were handed off successfully
//	  forward/<to>/<from>/... files this node is holding for another node
//	  failed/<to>/...         files that could not be sent (see *.error.txt)
//
// Files are written to a temporary name and renamed into place, so a file
// you see in inbox/ is always complete.
package mailbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"amail/internal/config"
)

// Folder names under the mailbox root.
const (
	DirInbox   = "inbox"
	DirOutbox  = "outbox"
	DirSent    = "sent"
	DirForward = "forward"
	DirFailed  = "failed"
)

// MetaExt is the sidecar written next to files held in forward/.
const MetaExt = ".amailmeta"

const tmpPrefix = ".amail-tmp-"

// StableAge is how long an outbox file must be unmodified before it is
// picked up, so half-copied files are not sent.
var StableAge = 3 * time.Second

// Mailbox is a mailbox root directory.
type Mailbox struct {
	Root string
}

// Item is a file waiting to be delivered, from outbox/ or forward/.
type Item struct {
	To           string
	Origin       string
	Path         string // absolute path on disk
	Name         string // relative name, slash separated
	Size         int64
	Hops         int
	ID           string
	ReceivedFrom string
	Held         bool // true when the item lives in forward/
}

// Meta is the sidecar stored next to a held file.
type Meta struct {
	ID           string `json:"id"`
	Origin       string `json:"origin"`
	To           string `json:"to"`
	Hops         int    `json:"hops"`
	ReceivedFrom string `json:"received_from"`
	ReceivedAt   string `json:"received_at"`
}

const readme = `This folder is managed by AMail (Armas Mail).

  outbox/<node-id>/   Put a file (or folder) in here and it is sent to that node.
                      It moves to sent/<node-id>/ once it has been handed off.
  inbox/<node-id>/    Files that arrived, one folder per sender.
  sent/<node-id>/     Your delivered outbox files.
  forward/            Files this machine is holding for other nodes (machine managed).
  failed/<node-id>/   Files that could not be delivered, with a .error.txt beside them.

Run "amail status" to see the network, "amail help" for everything else.
`

// Open ensures the folder layout exists and returns the mailbox.
func Open(root string) (*Mailbox, error) {
	if root == "" {
		return nil, errors.New("mailbox path is empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	for _, d := range []string{DirInbox, DirOutbox, DirSent, DirForward, DirFailed} {
		if err := os.MkdirAll(filepath.Join(abs, d), 0o755); err != nil {
			return nil, err
		}
	}
	rp := filepath.Join(abs, "README.txt")
	if _, err := os.Stat(rp); errors.Is(err, os.ErrNotExist) {
		_ = os.WriteFile(rp, []byte(readme), 0o644)
	}
	return &Mailbox{Root: abs}, nil
}

// Dir returns the absolute path of a top-level folder.
func (m *Mailbox) Dir(kind string) string { return filepath.Join(m.Root, kind) }

var reserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

func sanitizeSegment(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f:
			b.WriteRune('_')
		case strings.ContainsRune(`<>:"|?*`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimRight(b.String(), ". ")
	stem := out
	if i := strings.IndexByte(out, '.'); i >= 0 {
		stem = out[:i]
	}
	if reserved[strings.ToUpper(stem)] {
		out = "_" + out
	}
	return out
}

// CleanName normalises a relative file name received from the network:
// forward slashes, no empty / "." / ".." segments, no characters that a
// Windows file system refuses. Returns an error for anything unusable.
func CleanName(name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	var out []string
	for _, p := range strings.Split(name, "/") {
		p = strings.TrimSpace(p)
		if p == "" || p == "." {
			continue
		}
		if p == ".." {
			return "", fmt.Errorf("file name %q must not contain '..'", name)
		}
		p = sanitizeSegment(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return "", errors.New("empty file name")
	}
	clean := strings.Join(out, "/")
	if len(clean) > 900 {
		return "", errors.New("file name too long")
	}
	return clean, nil
}

// IsTemp reports whether a file name should be ignored by the outbox scan.
func IsTemp(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "~$") || strings.HasPrefix(name, tmpPrefix) {
		return true
	}
	for _, s := range []string{".tmp", ".part", ".crdownload", ".partial", MetaExt} {
		if strings.HasSuffix(lower, s) {
			return true
		}
	}
	switch lower {
	case "thumbs.db", "desktop.ini":
		return true
	}
	return false
}

func uniquePath(dir, base string) string {
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for i := 1; ; i++ {
		cand := base
		if i > 1 {
			cand = fmt.Sprintf("%s (%d)%s", stem, i, ext)
		}
		p := filepath.Join(dir, cand)
		if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
			return p
		}
	}
}

// writeUnique streams size bytes into dir/base (or a "(2)" variant if that
// exists) via a temp file, and returns the final path.
func writeUnique(dir, base string, r io.Reader, size int64) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, tmpPrefix+"*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	n, err := io.Copy(tmp, io.LimitReader(r, size))
	cerr := tmp.Close()
	if err == nil {
		err = cerr
	}
	if err == nil && n != size {
		err = fmt.Errorf("short body: got %d of %d bytes", n, size)
	}
	if err != nil {
		_ = os.Remove(tmpName)
		return "", err
	}
	for i := 0; i < 50; i++ {
		final := uniquePath(dir, base)
		if err := os.Rename(tmpName, final); err == nil {
			return final, nil
		} else if i == 49 {
			_ = os.Remove(tmpName)
			return "", err
		}
	}
	return "", errors.New("could not place file")
}

func split(name string) (string, string) {
	native := filepath.FromSlash(name)
	return filepath.Dir(native), filepath.Base(native)
}

// Receive stores an incoming file under inbox/<from>/<name>.
func (m *Mailbox) Receive(from, name string, r io.Reader, size int64) (string, error) {
	if from == "" || config.ValidID(from) != nil {
		from = "unknown"
	}
	clean, err := CleanName(name)
	if err != nil {
		return "", err
	}
	sub, base := split(clean)
	return writeUnique(filepath.Join(m.Root, DirInbox, from, sub), base, r, size)
}

// Hold stores a file for another node under forward/<to>/<origin>/<name>
// together with its metadata sidecar.
func (m *Mailbox) Hold(to, origin, name string, r io.Reader, size int64, meta Meta) (string, error) {
	if err := config.ValidID(to); err != nil {
		return "", err
	}
	if origin == "" || config.ValidID(origin) != nil {
		origin = "unknown"
	}
	clean, err := CleanName(name)
	if err != nil {
		return "", err
	}
	sub, base := split(clean)
	path, err := writeUnique(filepath.Join(m.Root, DirForward, to, origin, sub), base, r, size)
	if err != nil {
		return "", err
	}
	meta.To, meta.Origin = to, origin
	if meta.ReceivedAt == "" {
		meta.ReceivedAt = time.Now().UTC().Format(time.RFC3339)
	}
	js, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(path+MetaExt, js, 0o644); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// HoldFile copies an existing file (usually an outbox item) into forward/.
func (m *Mailbox) HoldFile(it Item, meta Meta) (string, error) {
	f, err := os.Open(it.Path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	return m.Hold(it.To, it.Origin, it.Name, f, st.Size(), meta)
}

// walkFiles calls fn for every regular, non-temporary file under dir with
// its slash-separated relative name.
func walkFiles(dir string, fn func(path, rel string, info fs.FileInfo)) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == dir {
				return nil
			}
			return nil
		}
		if d.IsDir() {
			if p != dir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || IsTemp(d.Name()) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return nil
		}
		fn(p, filepath.ToSlash(rel), info)
		return nil
	})
}

// Outgoing lists stable files in outbox/<to>/.
func (m *Mailbox) Outgoing() ([]Item, error) {
	var items []Item
	entries, err := os.ReadDir(m.Dir(DirOutbox))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		to := e.Name()
		dir := filepath.Join(m.Dir(DirOutbox), to)
		_ = walkFiles(dir, func(p, rel string, info fs.FileInfo) {
			if now.Sub(info.ModTime()) < StableAge {
				return
			}
			items = append(items, Item{To: to, Path: p, Name: rel, Size: info.Size()})
		})
	}
	return items, nil
}

// Held lists files this node holds for node "to".
func (m *Mailbox) Held(to string) []Item {
	var items []Item
	base := filepath.Join(m.Dir(DirForward), to)
	origins, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	for _, o := range origins {
		if !o.IsDir() || strings.HasPrefix(o.Name(), ".") {
			continue
		}
		origin := o.Name()
		dir := filepath.Join(base, origin)
		_ = walkFiles(dir, func(p, rel string, info fs.FileInfo) {
			it := Item{To: to, Origin: origin, Path: p, Name: rel, Size: info.Size(), Held: true}
			if js, err := os.ReadFile(p + MetaExt); err == nil {
				var meta Meta
				if json.Unmarshal(js, &meta) == nil {
					it.ID, it.Hops, it.ReceivedFrom = meta.ID, meta.Hops, meta.ReceivedFrom
				}
			}
			items = append(items, it)
		})
	}
	return items
}

// HeldAll lists everything in forward/.
func (m *Mailbox) HeldAll() []Item {
	var items []Item
	tos, err := os.ReadDir(m.Dir(DirForward))
	if err != nil {
		return nil
	}
	for _, t := range tos {
		if !t.IsDir() || strings.HasPrefix(t.Name(), ".") {
			continue
		}
		items = append(items, m.Held(t.Name())...)
	}
	return items
}

// moveTo moves an item's file into <kind>/<to>/<name>, keeping it unique.
func (m *Mailbox) moveTo(it Item, kind string) (string, error) {
	sub, base := split(it.Name)
	dir := filepath.Join(m.Dir(kind), it.To, sub)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dest := uniquePath(dir, base)
	if err := os.Rename(it.Path, dest); err != nil {
		// Cross-device or locked: fall back to copy + delete.
		if cerr := copyFile(it.Path, dest); cerr != nil {
			return "", err
		}
		if rerr := os.Remove(it.Path); rerr != nil {
			return "", rerr
		}
	}
	_ = os.Remove(it.Path + MetaExt)
	m.prune(filepath.Dir(it.Path), it)
	return dest, nil
}

// MarkSent moves an outbox item to sent/.
func (m *Mailbox) MarkSent(it Item) (string, error) { return m.moveTo(it, DirSent) }

// Fail moves an item to failed/ and writes the reason beside it.
func (m *Mailbox) Fail(it Item, why string) (string, error) {
	dest, err := m.moveTo(it, DirFailed)
	if err != nil {
		return "", err
	}
	note := fmt.Sprintf("AMail could not deliver %s to %s\n%s\n%s\n", it.Name, it.To, time.Now().Format(time.RFC3339), why)
	_ = os.WriteFile(dest+".error.txt", []byte(note), 0o644)
	return dest, nil
}

// Remove deletes a held item and its sidecar.
func (m *Mailbox) Remove(it Item) error {
	if err := os.Remove(it.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_ = os.Remove(it.Path + MetaExt)
	m.prune(filepath.Dir(it.Path), it)
	return nil
}

// prune removes empty sub-directories left behind under outbox/<to> or
// forward/<to>/<origin>, but never those folders themselves.
func (m *Mailbox) prune(dir string, it Item) {
	stop := filepath.Join(m.Dir(DirOutbox), it.To)
	if it.Held {
		stop = filepath.Join(m.Dir(DirForward), it.To, it.Origin)
	}
	for dir != stop && strings.HasPrefix(dir, stop) {
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
	if it.Held {
		// An origin folder with nothing left can go too.
		_ = os.Remove(stop)
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

// Counts returns how many files sit in inbox, outbox and forward.
func (m *Mailbox) Counts() (inbox, outbox, forward int) {
	count := func(kind string) int {
		n := 0
		_ = walkFiles(m.Dir(kind), func(string, string, fs.FileInfo) { n++ })
		return n
	}
	return count(DirInbox), count(DirOutbox), count(DirForward)
}

// Drop copies a local file into outbox/<to>/ and returns the queued path.
func (m *Mailbox) Drop(to, src string) (string, error) {
	if err := config.ValidID(to); err != nil {
		return "", err
	}
	f, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", fmt.Errorf("%s is a folder; copy it into %s yourself", src, filepath.Join(m.Dir(DirOutbox), to))
	}
	return writeUnique(filepath.Join(m.Dir(DirOutbox), to), filepath.Base(src), f, st.Size())
}
