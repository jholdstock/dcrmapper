package crawler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/peer/v2"
	"github.com/decred/dcrd/wire"
)

const (
	// defaultStaleTimeout is the time in which a host is considered stale.
	defaultStaleTimeout = time.Minute * 10

	// dumpAddressInterval is the interval used to dump the address cache to
	// disk for future use.
	dumpAddressInterval = time.Minute * 30

	// peersFilename is the name of the file.
	peersFilename = "nodes.json"

	nodeTimeout = time.Second * 5
)

type Manager struct {
	mtx sync.RWMutex

	netParams *chaincfg.Params
	nodes     map[string]*Node
	goodNodes []string
	peersFile string
}

func New(homeDir string, params *chaincfg.Params, seedPeer []string) (*Manager, error) {
	dataDir := filepath.Join(homeDir, params.Name)
	err := os.MkdirAll(dataDir, 0700)
	if err != nil {
		return nil, err
	}
	amgr := &Manager{
		netParams: params,
		nodes:     make(map[string]*Node),
		peersFile: filepath.Join(dataDir, peersFilename),
	}

	var seedIPs []net.IP
	for _, s := range seedPeer {
		seedIPs = append(seedIPs, net.ParseIP(s))
	}

	err = amgr.deserializePeers()
	if err != nil {
		log.Printf("Failed to parse file %s: %v", amgr.peersFile, err)
		// if it is invalid we nuke the old one unconditionally.
		err = os.Remove(amgr.peersFile)
		if err != nil {
			log.Printf("Failed to remove corrupt peers file %s: %v",
				amgr.peersFile, err)
		}
	}

	amgr.AddAddresses(seedIPs)

	// Initialize good list.
	now := time.Now()
	for k, node := range amgr.nodes {
		if now.Sub(node.LastSuccess) < defaultStaleTimeout {
			amgr.goodNodes = append(amgr.goodNodes, k)
		}
	}

	log.Printf("Initialized with %d nodes, %d good", len(amgr.nodes), len(amgr.goodNodes))

	return amgr, nil
}

func (m *Manager) Start(ctx context.Context, shutdownWg *sync.WaitGroup) {

	m.checkNodes(ctx)
	m.geoIP(ctx)

	shutdownWg.Add(1)
	go func() {
		defer shutdownWg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Minute * 3):
				m.checkNodes(ctx)
				m.geoIP(ctx)
			}
		}
	}()

	go shutdownWg.Add(1)
	go func() {
		defer shutdownWg.Done()
		for {
			select {
			case <-time.After(dumpAddressInterval):
				m.savePeers()
			case <-ctx.Done():
				// App is shutting down.
				// TODO move this so it happens after everything else has
				// finished, rather than happening as soon as shutdown is
				// signalled.
				m.savePeers()
				return
			}
		}
	}()
}

func (m *Manager) testPeer(ctx context.Context, ip string, netParams *chaincfg.Params) {
	onaddr := make(chan struct{}, 1)
	verack := make(chan struct{}, 1)

	config := peer.Config{
		UserAgentName:    "decred-mapper",
		UserAgentVersion: "0.0.1",
		Net:              netParams.Net,
		DisableRelayTx:   true,

		Listeners: peer.MessageListeners{
			OnAddr: func(p *peer.Peer, msg *wire.MsgAddr) {
				n := make([]net.IP, 0, len(msg.AddrList))
				for _, addr := range msg.AddrList {
					n = append(n, addr.IP)
				}
				added := m.AddAddresses(n)
				if added > 0 {
					log.Printf("Received %v new addresses from peer %s",
						added, p.Addr())
				}

				onaddr <- struct{}{}
			},
			OnVerAck: func(p *peer.Peer, msg *wire.MsgVerAck) {
				verack <- struct{}{}
			},
		},
	}

	host := net.JoinHostPort(ip, netParams.DefaultPort)
	p, err := peer.NewOutboundPeer(&config, host)
	if err != nil {
		m.Bad(ip, "outbound peer error", err)
		return
	}

	ctxTimeout, cancel := context.WithTimeout(ctx, nodeTimeout)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctxTimeout, "tcp", p.Addr())
	if err != nil {
		m.Bad(ip, "dial timeout error", err)
		return
	}
	p.AssociateConnection(conn)
	defer p.Disconnect()

	// Wait for the verack message.
	select {
	case <-verack:
		m.Good(p)
		// Ask peer for some addresses.
		p.QueueMessage(wire.NewMsgGetAddr(), nil)
	case <-time.After(nodeTimeout):
		m.Bad(ip, "verack timeout", nil)
		return
	case <-ctx.Done():
		// App shutting down.
		return
	}

	select {
	case <-onaddr:
	case <-time.After(nodeTimeout):
	case <-ctx.Done():
	}
}

func (m *Manager) checkNodes(ctx context.Context) {
	for {
		ips := m.StaleAddresses()
		if len(ips) == 0 {
			log.Println("No stale addresses")
			return
		}

		log.Printf("Checking %d stale addresses", len(ips))

		// Limit 1000 goroutines to run concurrently.
		limit := gccm(1000)

		for _, ip := range ips {
			limit.Wait()

			go func(ip string) {
				m.testPeer(ctx, ip, m.netParams)
				limit.Done()
			}(ip)
		}
		limit.WaitAllDone()
		log.Printf("Done checking %d addresses, %d good", len(m.nodes), len(m.goodNodes))
	}
}

func (m *Manager) AddAddresses(addrs []net.IP) int {
	var count int

	m.mtx.Lock()
	for _, addr := range addrs {
		if !isRoutable(addr) {
			continue
		}
		addrStr := addr.String()

		_, exists := m.nodes[addrStr]
		if exists {
			continue
		}
		node := Node{
			IP: addr,
		}
		m.nodes[addrStr] = &node

		count++
	}
	m.mtx.Unlock()

	return count
}

// StaleAddresses returns IPs that need to be tested again.
func (m *Manager) StaleAddresses() []string {
	addrs := make([]string, 0)
	now := time.Now()

	m.mtx.RLock()
	for _, node := range m.nodes {
		if now.Sub(node.LastAttempt) < defaultStaleTimeout {
			continue
		}

		addrs = append(addrs, node.IP.String())
	}
	m.mtx.RUnlock()

	return addrs
}

func (m *Manager) Bad(ip, reason string, err error) {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	m.nodes[ip].LastAttempt = time.Now()

	for i, n := range m.goodNodes {
		if n == ip {
			m.goodNodes[i] = m.goodNodes[len(m.goodNodes)-1]
			m.goodNodes = m.goodNodes[:len(m.goodNodes)-1]
			log.Printf("Removed bad peer, reason: %q, IP %s, err: %v\n", reason, ip, err)
			return
		}
	}
}

func (m *Manager) Good(p *peer.Peer) {
	m.mtx.Lock()
	defer m.mtx.Unlock()

	peerIP := p.NA().IP.String()

	node, exists := m.nodes[peerIP]
	if !exists {
		panic("unknown peer passed into Good")
	}

	node.ProtocolVersion = p.ProtocolVersion()
	node.Services = p.Services()
	node.UserAgent = p.UserAgent()
	node.LastSuccess = time.Now()
	node.LastAttempt = time.Now()

	// Add to good list if not already there
	for _, n := range m.goodNodes {
		if n == peerIP {
			return
		}
	}

	m.goodNodes = append(m.goodNodes, peerIP)
}

func min(x, y int) int {
	if x < y {
		return x
	}
	return y
}

func (m *Manager) AllGoodNodes() []*Node {
	m.mtx.RLock()
	defer m.mtx.RUnlock()
	var goodNodes []*Node
	for _, ip := range m.goodNodes {
		goodNodes = append(goodNodes, m.nodes[ip])
	}
	return goodNodes
}

func (m *Manager) GetNode(ip string) (*Node, bool, bool) {
	m.mtx.RLock()
	defer m.mtx.RUnlock()

	node, ok := m.nodes[ip]
	if !ok {
		return nil, false, false
	}

	var good bool
	for _, goodIP := range m.goodNodes {
		if goodIP == ip {
			good = true
			break
		}
	}

	return node, good, true
}

type Count struct {
	Value      string
	Count      int
	AbsPercent int
	RelPercent int
}

type Summary struct {
	AppName       string
	IP4           int
	IP6           int
	GoodCount     int
	UserAgents    []Count
	AS            []Count
	CountryCounts []Count
}

func (m *Manager) GetSummary() Summary {
	m.mtx.RLock()
	defer m.mtx.RUnlock()

	var ip4, ip6 int
	ua := make(map[string]int)
	as := make(map[string]int)
	country := make(map[string]int)

	for _, ip := range m.goodNodes {
		node := m.nodes[ip]
		if node.IP.To4() != nil {
			ip4++
		} else {
			ip6++
		}

		// Count countries
		if node.GeoData != nil {
			country[node.GeoData.Country] = country[node.GeoData.Country] + 1
		}

		// Count AS
		if node.GeoData != nil {
			as[node.GeoData.ASName] = as[node.GeoData.ASName] + 1
		}

		// Count UserAgents
		ua[node.UserAgent] = ua[node.UserAgent] + 1
	}

	// Sort UserAgents
	var sortedUA []Count
	for k, v := range ua {
		sortedUA = append(sortedUA, Count{
			Value: k,
			Count: v,
		})
	}
	sort.Slice(sortedUA, func(i, j int) bool {
		return sortedUA[i].Count > sortedUA[j].Count
	})

	// Sort AS
	var sortedAS []Count
	for k, v := range as {
		sortedAS = append(sortedAS, Count{
			Value:      k,
			Count:      v,
			AbsPercent: 100 * v / len(m.goodNodes),
		})
	}
	sort.Slice(sortedAS, func(i, j int) bool {
		return sortedAS[i].Count > sortedAS[j].Count
	})

	// Sort Countries
	var maxCountries int
	for _, v := range country {
		if v > maxCountries {
			maxCountries = v
		}
	}
	var sortedCountry []Count
	for k, v := range country {
		sortedCountry = append(sortedCountry, Count{
			Value:      k,
			Count:      v,
			RelPercent: 100 * v / maxCountries,
			AbsPercent: 100 * v / len(m.goodNodes),
		})
	}
	sort.Slice(sortedCountry, func(i, j int) bool {
		return sortedCountry[i].Count > sortedCountry[j].Count
	})

	return Summary{
		IP4:           ip4,
		IP6:           ip6,
		GoodCount:     len(m.goodNodes),
		UserAgents:    sortedUA,
		AS:            sortedAS,
		CountryCounts: sortedCountry,
	}

}

func (m *Manager) PageOfNodes(first, last int) (int, []*Node) {
	m.mtx.RLock()
	defer m.mtx.RUnlock()

	var toReturn []*Node
	count := len(m.goodNodes)

	if count == 0 {
		return count, toReturn
	}

	keys := m.goodNodes[first:min(last, count)]
	for _, key := range keys {
		toReturn = append(toReturn, m.nodes[key])
	}

	return count, toReturn
}

func (m *Manager) deserializePeers() error {
	filePath := m.peersFile
	_, err := os.Stat(filePath)
	if os.IsNotExist(err) {
		return nil
	}
	r, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("%s error opening file: %v", filePath, err)
	}
	defer r.Close()

	var nodes map[string]*Node
	dec := json.NewDecoder(r)
	err = dec.Decode(&nodes)
	if err != nil {
		return fmt.Errorf("error reading %s: %v", filePath, err)
	}

	l := len(nodes)

	m.mtx.Lock()
	m.nodes = nodes
	m.mtx.Unlock()

	log.Printf("%d nodes loaded from %s", l, filePath)
	return nil
}

func (m *Manager) savePeers() {
	m.mtx.RLock()
	defer m.mtx.RUnlock()

	// Write temporary peers file and then move it into place.
	tmpfile := m.peersFile + ".new"
	w, err := os.Create(tmpfile)
	if err != nil {
		log.Printf("Error opening file %s: %v", tmpfile, err)
		return
	}
	enc := json.NewEncoder(w)
	if err := enc.Encode(&m.nodes); err != nil {
		log.Printf("Failed to encode file %s: %v", tmpfile, err)
		return
	}
	if err := w.Close(); err != nil {
		log.Printf("Error closing file %s: %v", tmpfile, err)
		return
	}
	if err := os.Rename(tmpfile, m.peersFile); err != nil {
		log.Printf("Error writing file %s: %v", m.peersFile, err)
		return
	}

	log.Printf("%d nodes saved to %s", len(m.nodes), m.peersFile)
}
