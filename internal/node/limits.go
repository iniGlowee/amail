package node

import (
	"sync"
	"time"
)

// limiter bounds inbound connections before the TLS handshake:
//
//   - a global cap on concurrent connections,
//   - a per-IP cap on concurrent connections,
//   - a per-IP cap on *failed* handshakes per minute.
//
// Members of the network complete the handshake and are never throttled by
// the third rule, however often they connect. Strangers and scanners fail
// it, accrue strikes, and are dropped at accept() for the rest of the
// minute, which costs the node one closed socket per attempt.
type limiter struct {
	mu         sync.Mutex
	max        int
	maxPerIP   int
	maxStrikes int
	active     int
	activeIP   map[string]int
	strikes    map[string]*ipWindow
	logged     map[string]time.Time
	lastPrune  time.Time
}

type ipWindow struct {
	count int
	start time.Time
}

func newLimiter(max, maxStrikes int) *limiter {
	perIP := max / 4
	if perIP < 4 {
		perIP = 4
	}
	return &limiter{
		max:        max,
		maxPerIP:   perIP,
		maxStrikes: maxStrikes,
		activeIP:   map[string]int{},
		strikes:    map[string]*ipWindow{},
		logged:     map[string]time.Time{},
		lastPrune:  time.Now(),
	}
}

// admit decides whether a connection from ip may proceed. It returns a
// release function to call when the connection ends, or a reason string
// when refused. logIt is true at most once per IP per minute so a flood
// produces one log line, not thousands.
func (l *limiter) admit(ip string) (release func(), reason string, logIt bool) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.lastPrune) > time.Minute {
		for k, w := range l.strikes {
			if now.Sub(w.start) >= time.Minute {
				delete(l.strikes, k)
			}
		}
		for k, t := range l.logged {
			if now.Sub(t) >= time.Minute {
				delete(l.logged, k)
			}
		}
		l.lastPrune = now
	}
	refuse := func(why string) (func(), string, bool) {
		first := now.Sub(l.logged[ip]) >= time.Minute
		if first {
			l.logged[ip] = now
		}
		return nil, why, first
	}
	if w := l.strikes[ip]; w != nil && now.Sub(w.start) < time.Minute && w.count >= l.maxStrikes {
		return refuse("too many failed handshakes this minute")
	}
	if l.activeIP[ip] >= l.maxPerIP {
		return refuse("too many open connections from this address")
	}
	if l.active >= l.max {
		return refuse("node connection limit reached")
	}
	l.active++
	l.activeIP[ip]++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			l.active--
			l.activeIP[ip]--
			if l.activeIP[ip] <= 0 {
				delete(l.activeIP, ip)
			}
			l.mu.Unlock()
		})
	}, "", false
}

// strike records a failed TLS handshake from ip.
func (l *limiter) strike(ip string) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.strikes[ip]
	if w == nil || now.Sub(w.start) >= time.Minute {
		w = &ipWindow{start: now}
		l.strikes[ip] = w
	}
	w.count++
}
