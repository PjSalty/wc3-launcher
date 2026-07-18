// Package relay is the server-side native-host relay daemon. It runs beside
// pvpgn on the server: each host player's launcher opens one outbound tunnel,
// the relay allocates a public port from a NAT'd pool, proxies the host's realm
// (BNCS) connection to pvpgn so pvpgn advertises the relay's IP as the game
// host, and fans joiner TCP connections down the tunnel to the host's local
// Warcraft III listener.
package relay

import (
	"fmt"
	"sync"
)

// Pool hands out public game ports from a fixed range. Every port in the range
// must be forwarded from the internet to this host, so the range is bounded by
// whatever the router forwards (default 6200-6299). One port per hosted game.
type Pool struct {
	mu   sync.Mutex
	free []uint16
	used map[uint16]bool
}

// NewPool builds a pool over the inclusive range [lo, hi].
func NewPool(lo, hi uint16) *Pool {
	p := &Pool{used: make(map[uint16]bool)}
	for port := int(lo); port <= int(hi); port++ {
		p.free = append(p.free, uint16(port))
	}
	return p
}

// Alloc reserves the next free port, or errors when the pool is exhausted.
func (p *Pool) Alloc() (uint16, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.free) == 0 {
		return 0, fmt.Errorf("relay: port pool exhausted")
	}
	port := p.free[0]
	p.free = p.free[1:]
	p.used[port] = true
	return port, nil
}

// Release returns a port to the pool. Releasing a port that is not allocated is
// a no-op.
func (p *Pool) Release(port uint16) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.used[port] {
		delete(p.used, port)
		// shortcut: immediate reuse; add a release cooldown timer if a freed
		// port's lingering joiner sockets ever collide with the next session.
		p.free = append(p.free, port)
	}
}

// Available reports how many ports are currently free (for logging/metrics).
func (p *Pool) Available() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.free)
}
