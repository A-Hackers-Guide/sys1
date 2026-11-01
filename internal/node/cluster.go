package node

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/A-Hackers-Guide/sys1/internal/protocol"
	"github.com/A-Hackers-Guide/sys1/internal/tlsutil"
	"github.com/hashicorp/raft"
)

// Clusters: two or more nodes keep the same state with Raft, so the pool
// survives losing a node. One node leads and does all the work; every
// change it records is copied to a majority of nodes before it counts.
// When the leader disappears the others elect a new one, which picks up
// exactly where the old one stopped. Clients know every node's address and
// move to whichever is leading. See docs/CLUSTER.md.
//
// A witness is a member that only votes: it keeps a copy of the state, as
// every Raft member must, but hands leadership to a full node whenever it
// gets it, and clients never use it. A node, a standby and a witness on any
// third machine survive losing any one of them; two nodes alone can't,
// because neither can tell a dead partner from a broken link.
//
// One node can be preferred (by default the one the cluster started on).
// While another node leads, it hands leadership back once the preferred
// node has been answering steadily, so the pool returns to its main
// computer after an outage.
//
// All nodes share one identity: the cluster's TLS certificate (so the
// fingerprint in join codes covers every node) and its pool key. A new node
// receives both over the pinned, encrypted connection when it joins.

// Member is one node in the cluster.
type Member struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	RaftAddr string   `json:"raftAddr"`
	APIAddrs []string `json:"apiAddrs"`
	Witness  bool     `json:"witness,omitempty"`
}

type cluster struct {
	r       *raft.Raft
	id      string
	epoch   string // this process; tags the entries it proposes
	seq     atomic.Uint64
	active  atomic.Bool // leader, and caught up: may change state
	members map[string]Member
	token   string
	prefer  string // member ID that should lead when it can
	witness bool   // this node only votes
	store   *raftStore

	// leaderSince and peerFailed (by member ID, the last failed heartbeat)
	// tell failback whether the preferred node is steadily reachable.
	leaderSince atomic.Int64
	peerMu      sync.Mutex
	peerFailed  map[string]time.Time
	balanceMu   sync.Mutex
}

// entry wraps an event in the Raft log.
type entry struct {
	Origin string          `json:"o"` // node ID and process epoch that proposed it
	Seq    uint64          `json:"s"`
	Event  json.RawMessage `json:"e,omitempty"`
	Reset  [][]byte        `json:"r,omitempty"` // whole state, replacing what is there
	Member *Member         `json:"m,omitempty"`
	Leave  string          `json:"l,omitempty"`
	Token  string          `json:"t,omitempty"` // cluster join token
	Prefer string          `json:"p,omitempty"` // member ID to lead when it can
}

// ErrStandby is returned for changes asked of a node that isn't leading.
var ErrStandby = errors.New("this node is a standby; the cluster's leader handles changes")

func (n *Node) clustered() bool { return n.cl != nil }

// Clustered reports whether this node is a cluster member.
func (n *Node) Clustered() bool { return n.clustered() }

// leading reports whether this node may change state: always in single-
// node mode; in a cluster, only the leader once it has caught up.
func (n *Node) leading() bool { return n.cl == nil || n.cl.active.Load() }

func nodeID(dataDir string) string {
	p := filepath.Join(dataDir, "node.id")
	if b, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		return strings.TrimSpace(string(b))
	}
	b := make([]byte, 8)
	rand.Read(b)
	id := hex.EncodeToString(b)
	os.WriteFile(p, []byte(id+"\n"), 0o600)
	return id
}

// startCluster brings up Raft. bootstrap makes a new one-node cluster from
// the state this node already has.
func (n *Node) startCluster(ctx context.Context, bootstrap bool) error {
	cfg := n.cfg
	ln, err := net.Listen("tcp", cfg.RaftAddr)
	if err != nil {
		return err
	}
	advertise := ln.Addr().(*net.TCPAddr)
	if advertise.IP.IsUnspecified() {
		advertise = &net.TCPAddr{IP: net.ParseIP(lanIP()), Port: advertise.Port}
	}
	stream := &tlsStream{ln: ln, addr: advertise, cert: n.cert, fp: n.fp}
	trans := raft.NewNetworkTransport(stream, 3, 10*time.Second, io.Discard)
	dir := filepath.Join(cfg.DataDir, "raft")
	os.MkdirAll(dir, 0o700)
	store, err := openRaftStore(filepath.Join(dir, "raft.journal"))
	if err != nil {
		return err
	}
	snaps, err := raft.NewFileSnapshotStore(dir, 2, io.Discard)
	if err != nil {
		return err
	}
	b := make([]byte, 8)
	rand.Read(b)
	c := &cluster{id: nodeID(cfg.DataDir), epoch: hex.EncodeToString(b), members: map[string]Member{}, store: store, peerFailed: map[string]time.Time{}}
	if st, err := readJoinState(cfg.DataDir); err == nil {
		c.witness = st.Witness
	}
	rc := raft.DefaultConfig()
	rc.LocalID = raft.ServerID(c.id)
	rc.LogOutput = io.Discard
	if cfg.FastRaft { // tests
		rc.HeartbeatTimeout, rc.ElectionTimeout, rc.LeaderLeaseTimeout = 200*time.Millisecond, 200*time.Millisecond, 100*time.Millisecond
		rc.CommitTimeout = 10 * time.Millisecond
	}
	rc.SnapshotThreshold = 4096
	n.mu.Lock()
	n.cl = c
	n.mu.Unlock()
	r, err := raft.NewRaft(rc, (*fsm)(n), store, store, snaps, trans)
	if err != nil {
		return err
	}
	c.r = r
	if bootstrap {
		has, _ := raft.HasExistingState(store, store, snaps)
		if !has {
			r.BootstrapCluster(raft.Configuration{Servers: []raft.Server{{ID: rc.LocalID, Address: trans.LocalAddr()}}})
			n.mu.Lock()
			n.pendingSeed = true // seed the log with today's state once leading
			n.mu.Unlock()
		}
	}
	n.mu.Lock()
	n.raftAddr = string(trans.LocalAddr())
	n.mu.Unlock()
	obs := make(chan raft.Observation, 64)
	r.RegisterObserver(raft.NewObserver(obs, false, func(o *raft.Observation) bool {
		switch o.Data.(type) {
		case raft.FailedHeartbeatObservation, raft.ResumedHeartbeatObservation:
			return true
		}
		return false
	}))
	go n.watchPeers(ctx, obs)
	go n.watchLeadership(ctx)
	go n.failback(ctx)
	if !bootstrap {
		go n.requestMembership(ctx)
	}
	return nil
}

// watchLeadership turns leading on only once every earlier entry has been
// applied, and off the moment leadership is lost.
func (n *Node) watchLeadership(ctx context.Context) {
	c := n.cl
	ch := c.r.LeaderCh()
	for {
		select {
		case <-ctx.Done():
			c.active.Store(false)
			return
		case lead := <-ch:
			if !lead {
				c.active.Store(false)
				c.leaderSince.Store(0)
				n.log.Info("cluster: now a standby")
				continue
			}
			if c.witness {
				go n.handOff(ctx)
				continue
			}
			if err := c.r.Barrier(30 * time.Second).Error(); err != nil {
				n.log.Warn("cluster: could not catch up after election", "err", err)
				continue
			}
			n.mu.Lock()
			// When machines last reported isn't replicated, so after a leader
			// change every machine gets the same grace period as after a
			// node restart before its work is moved.
			for _, a := range n.agents {
				a.LastSeen = time.Now()
			}
			c.active.Store(true)
			c.leaderSince.Store(time.Now().UnixNano())
			if n.pendingSeed {
				n.pendingSeed = false
				recs := n.recordsLocked()
				n.proposeLocked(entry{Reset: recs})
				n.proposeLocked(entry{Prefer: c.id})
			}
			if c.token == "" {
				n.proposeLocked(entry{Token: randomToken()})
			}
			n.proposeLocked(entry{Member: &Member{ID: c.id, Name: hostName(), RaftAddr: n.raftAddr, APIAddrs: n.ownAddrsLocked()}})
			n.mu.Unlock()
			n.log.Info("cluster: now leading")
			n.saveJoinCode()
		}
	}
}

func hostName() string {
	h, _ := os.Hostname()
	return h
}

// proposeLocked records e in the Raft log and waits for a majority to have
// it. The leader already changed its memory; its own entries are skipped
// when applied (see fsm.Apply).
func (n *Node) proposeLocked(e entry) {
	c := n.cl
	e.Origin, e.Seq = c.id+"/"+c.epoch, c.seq.Add(1)
	// Apply the changes this entry carries that live outside the event
	// stream right away, as the leader does for events.
	switch {
	case e.Member != nil:
		c.members[e.Member.ID] = *e.Member
	case e.Leave != "":
		delete(c.members, e.Leave)
	case e.Token != "":
		c.token = e.Token
	case e.Prefer != "":
		c.prefer = e.Prefer
	}
	b, _ := json.Marshal(e)
	if err := c.r.Apply(b, 10*time.Second).Error(); err != nil {
		n.log.Error("cluster: change not replicated", "err", err)
	}
}

// fsm applies the Raft log to the node's state.
type fsm Node

func (f *fsm) Apply(l *raft.Log) interface{} {
	n := (*Node)(f)
	var e entry
	if json.Unmarshal(l.Data, &e) != nil {
		return nil
	}
	c := n.cl
	if e.Origin == c.id+"/"+c.epoch {
		return nil // proposed by this very process, which already applied it
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	switch {
	case e.Reset != nil:
		n.resetLocked(e.Reset)
	case e.Event != nil:
		n.replay(e.Event)
	case e.Member != nil:
		c.members[e.Member.ID] = *e.Member
	case e.Leave != "":
		delete(c.members, e.Leave)
	case e.Token != "":
		c.token = e.Token
	case e.Prefer != "":
		c.prefer = e.Prefer
	}
	return nil
}

// Snapshot captures the whole state. It must not wait for the node lock
// for long, since the leader may hold it while waiting on Raft; if the
// lock is busy, Raft simply tries again later.
func (f *fsm) Snapshot() (raft.FSMSnapshot, error) {
	n := (*Node)(f)
	for i := 0; i < 50; i++ {
		if n.mu.TryLock() {
			recs := n.recordsLocked()
			c := n.cl
			members := make([]Member, 0, len(c.members))
			for _, m := range c.members {
				members = append(members, m)
			}
			token, prefer := c.token, c.prefer
			n.mu.Unlock()
			b, _ := json.Marshal(snapshotDoc{Records: recs, Members: members, Token: token, Prefer: prefer})
			return snapshotBytes(b), nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, errors.New("node busy; snapshot later")
}

type snapshotDoc struct {
	Records [][]byte `json:"records"`
	Members []Member `json:"members"`
	Token   string   `json:"token"`
	Prefer  string   `json:"prefer,omitempty"`
}

type snapshotBytes []byte

func (s snapshotBytes) Persist(sink raft.SnapshotSink) error {
	if _, err := sink.Write(s); err != nil {
		sink.Cancel()
		return err
	}
	return sink.Close()
}

func (s snapshotBytes) Release() {}

func (f *fsm) Restore(rc io.ReadCloser) error {
	n := (*Node)(f)
	defer rc.Close()
	var doc snapshotDoc
	if err := json.NewDecoder(rc).Decode(&doc); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.resetLocked(doc.Records)
	n.cl.members = map[string]Member{}
	for _, m := range doc.Members {
		n.cl.members[m.ID] = m
	}
	n.cl.token, n.cl.prefer = doc.Token, doc.Prefer
	return nil
}

// resetLocked replaces all replicated state with recs.
func (n *Node) resetLocked(recs [][]byte) {
	n.agents, n.tasks, n.jobs = map[string]*Agent{}, map[string]*Task{}, map[string]*Job{}
	p := n.pool
	p.snapshots, p.backups, p.restores = map[string]*Snapshot{}, map[string]*Backup{}, map[string]*Restore{}
	for _, r := range recs {
		n.replay(r)
	}
	for _, a := range n.agents {
		a.LastSeen = time.Now()
	}
}

// ---- joining ----

// ClusterCode is what a new node needs to join: where to reach the cluster,
// its fingerprint, and the cluster join token.
func (n *Node) ClusterCode() (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.clustered() {
		return "", errors.New("this node isn't a cluster; start it with --cluster")
	}
	if n.cl.token == "" {
		return "", ErrStandby
	}
	addrs := n.addrsLocked()
	code := protocol.JoinCode{Addr: addrs[0], More: addrs[1:], Fingerprint: n.fp, Token: n.cl.token}
	return "sys1-cluster:" + strings.TrimPrefix(code.String(), "sys1-join:"), nil
}

// parseClusterCode reads a cluster code; it has the same shape as a join
// code, with the cluster join token instead.
func parseClusterCode(s string) (protocol.JoinCode, error) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(s), "sys1-cluster:")
	if !ok {
		return protocol.JoinCode{}, errors.New("not a cluster code (it starts with sys1-cluster:)")
	}
	j, err := protocol.ParseJoinCode("sys1-join:" + rest)
	if err != nil {
		return j, errors.New("cluster code is damaged; copy it again")
	}
	return j, nil
}

type joinRequest struct {
	Token    string   `json:"token"`
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	RaftAddr string   `json:"raftAddr"`
	APIAddrs []string `json:"apiAddrs"`
	Witness  bool     `json:"witness,omitempty"`
}

type joinResponse struct {
	CertPEM []byte `json:"certPem"`
	KeyPEM  []byte `json:"keyPem"`
	PoolKey []byte `json:"poolKey"`
}

// handleClusterJoin gives a new node the cluster's identity. It doesn't
// make it a member yet: that waits until the new node runs (see
// handleClusterAdd), so the cluster never counts a voter that is down.
func (n *Node) handleClusterJoin(w http.ResponseWriter, r *http.Request) {
	var req joinRequest
	if !readJSON(w, r, 1<<16, &req) || !n.clusterTokenOK(w, req.Token) {
		return
	}
	certPEM, _ := os.ReadFile(filepath.Join(n.cfg.DataDir, "node-cert.pem"))
	keyPEM, _ := os.ReadFile(filepath.Join(n.cfg.DataDir, "node-key.pem"))
	n.mu.Lock()
	key := append([]byte(nil), n.pool.key...)
	n.mu.Unlock()
	writeJSON(w, joinResponse{CertPEM: certPEM, KeyPEM: keyPEM, PoolKey: key})
}

// handleClusterAdd makes a running node a voting member.
func (n *Node) handleClusterAdd(w http.ResponseWriter, r *http.Request) {
	var req joinRequest
	if !readJSON(w, r, 1<<16, &req) || !n.clusterTokenOK(w, req.Token) {
		return
	}
	if req.ID == "" || req.RaftAddr == "" {
		http.Error(w, "missing node id or address", http.StatusBadRequest)
		return
	}
	// Every node joins without a vote and catches up; balanceVoters then
	// gives it one if that keeps the number of voters odd.
	if err := n.cl.r.AddNonvoter(raft.ServerID(req.ID), raft.ServerAddress(req.RaftAddr), 0, 10*time.Second).Error(); err != nil {
		http.Error(w, "could not add the node: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	n.mu.Lock()
	n.proposeLocked(entry{Member: &Member{ID: req.ID, Name: req.Name, RaftAddr: req.RaftAddr, APIAddrs: req.APIAddrs, Witness: req.Witness}})
	n.mu.Unlock()
	n.balanceVoters()
	n.saveJoinCode()
	n.log.Info("cluster: node joined", "name", req.Name)
	w.WriteHeader(http.StatusOK)
}

func (n *Node) clusterTokenOK(w http.ResponseWriter, token string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.clustered() || !n.leading() || n.cl.token == "" {
		http.Error(w, ErrStandby.Error(), http.StatusMisdirectedRequest)
		return false
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(n.cl.token)) != 1 {
		http.Error(w, "wrong cluster code", http.StatusForbidden)
		return false
	}
	return true
}

// joinState is what `sys1 cluster join` leaves for the node's next start.
type joinState struct {
	RaftAddr string   `json:"raftAddr"`
	APIAddrs []string `json:"apiAddrs"`
	Token    string   `json:"token"`
	Cluster  []string `json:"cluster"` // addresses to ask for membership
	Member   bool     `json:"member"`  // membership granted
	Witness  bool     `json:"witness,omitempty"`
}

func joinStatePath(dataDir string) string { return filepath.Join(dataDir, "cluster") }

func readJoinState(dataDir string) (joinState, error) {
	var st joinState
	b, err := os.ReadFile(joinStatePath(dataDir))
	if err == nil {
		err = json.Unmarshal(b, &st)
	}
	return st, err
}

// clusterPost sends req to path on any of addrs, trying each (and again,
// while an election is under way) until one accepts it.
func clusterPost(ctx context.Context, fp string, addrs []string, path string, req any, out any) error {
	body, _ := json.Marshal(req)
	hc := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsutil.PinnedConfig(fp)}}
	var lastErr error
	for round := 0; round < 30; round++ {
		for _, addr := range addrs {
			hreq, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+addr+path, strings.NewReader(string(body)))
			resp, err := hc.Do(hreq)
			if err != nil {
				lastErr = err
				continue
			}
			if resp.StatusCode == http.StatusOK {
				defer resp.Body.Close()
				if out == nil {
					return nil
				}
				return json.NewDecoder(resp.Body).Decode(out)
			}
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			lastErr = fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
			if resp.StatusCode == http.StatusForbidden {
				return lastErr
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("could not reach the cluster's leader: %w", lastErr)
}

// JoinCluster prepares the node in dataDir to join the cluster in code: it
// adopts the cluster's identity now, and becomes a member when it next
// starts with --cluster. A witness only votes (see the top of this file).
func JoinCluster(ctx context.Context, dataDir, code, raftAddr string, apiAddrs []string, witness bool) error {
	jc, err := parseClusterCode(code)
	if err != nil {
		return err
	}
	host, port, err := net.SplitHostPort(raftAddr)
	if err != nil {
		return fmt.Errorf("raft address: %w", err)
	}
	if ip := net.ParseIP(host); host == "" || ip.IsUnspecified() {
		raftAddr = net.JoinHostPort(lanIP(), port)
	}
	os.MkdirAll(dataDir, 0o700)
	var out joinResponse
	if err := clusterPost(ctx, jc.Fingerprint, jc.Addrs(), "/api/v1/cluster/join", joinRequest{Token: jc.Token}, &out); err != nil {
		return err
	}
	for name, data := range map[string][]byte{"node-cert.pem": out.CertPEM, "node-key.pem": out.KeyPEM, "pool.key": out.PoolKey} {
		if err := os.WriteFile(filepath.Join(dataDir, name), data, 0o600); err != nil {
			return err
		}
	}
	st, _ := json.Marshal(joinState{RaftAddr: raftAddr, APIAddrs: apiAddrs, Token: jc.Token, Cluster: jc.Addrs(), Witness: witness})
	return os.WriteFile(joinStatePath(dataDir), st, 0o600)
}

// requestMembership asks the leader to make this (now running) node a
// voting member, once.
func (n *Node) requestMembership(ctx context.Context) {
	path := joinStatePath(n.cfg.DataDir)
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var st joinState
	if json.Unmarshal(b, &st) != nil || st.Member {
		return
	}
	req := joinRequest{Token: st.Token, ID: n.cl.id, Name: hostName(), RaftAddr: st.RaftAddr, APIAddrs: st.APIAddrs, Witness: st.Witness}
	for ctx.Err() == nil {
		err := clusterPost(ctx, n.fp, st.Cluster, "/api/v1/cluster/add", req, nil)
		if err == nil {
			st.Member = true
			b, _ := json.Marshal(st)
			os.WriteFile(path, b, 0o600)
			n.log.Info("cluster: joined as a member")
			return
		}
		n.log.Warn("cluster: still asking to join", "err", err)
	}
}

// LeaveCluster removes a node from the cluster (from the leader).
func (n *Node) LeaveCluster(id string) error {
	if !n.clustered() || !n.leading() {
		return ErrStandby
	}
	if id == n.cl.id {
		return errors.New("move leadership away from this node first (stop it; another node takes over)")
	}
	if err := n.cl.r.RemoveServer(raft.ServerID(id), 0, 10*time.Second).Error(); err != nil {
		return err
	}
	n.mu.Lock()
	n.proposeLocked(entry{Leave: id})
	n.mu.Unlock()
	n.saveJoinCode()
	return nil
}

// ClusterStatus describes the cluster for the dashboard.
type ClusterStatus struct {
	Enabled     bool           `json:"enabled"`
	Leading     bool           `json:"leading"`
	Witness     bool           `json:"witness"`     // this node only votes
	AutoStandby bool           `json:"autoStandby"` // picks its own standby and witness
	Preferred   string         `json:"preferred"`   // member ID that leads when it can
	Self        string         `json:"self"`
	Leader      string         `json:"leader"`
	Members     []MemberStatus `json:"members"`
}

type MemberStatus struct {
	Member
	Voter  bool `json:"voter"`
	Leader bool `json:"leader"`
}

func (n *Node) ClusterStatus() ClusterStatus {
	if !n.clustered() {
		return ClusterStatus{}
	}
	st := ClusterStatus{Enabled: true, Leading: n.leading(), Self: n.cl.id, Witness: n.cl.witness}
	_, leaderID := n.cl.r.LeaderWithID()
	st.Leader = string(leaderID)
	voters := map[string]bool{}
	if f := n.cl.r.GetConfiguration(); f.Error() == nil {
		for _, s := range f.Configuration().Servers {
			voters[string(s.ID)] = s.Suffrage == raft.Voter
		}
	}
	n.mu.Lock()
	st.Preferred = n.cl.prefer
	st.AutoStandby = !n.settings.NoAutoStandby
	for _, m := range n.cl.members {
		st.Members = append(st.Members, MemberStatus{Member: m, Voter: voters[m.ID], Leader: m.ID == st.Leader})
	}
	n.mu.Unlock()
	sort.Slice(st.Members, func(i, j int) bool { return st.Members[i].Name < st.Members[j].Name })
	return st
}

// ---- transport ----

// tlsStream carries Raft over TLS. Every member presents the cluster's
// certificate and accepts only peers presenting the same one.
type tlsStream struct {
	ln   net.Listener
	addr net.Addr
	cert tls.Certificate
	fp   string
}

func (s *tlsStream) config() *tls.Config {
	verify := func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 || subtle.ConstantTimeCompare([]byte(tlsutil.Fingerprint(cs.PeerCertificates[0].Raw)), []byte(s.fp)) != 1 {
			return errors.New("peer is not a member of this cluster")
		}
		return nil
	}
	return &tls.Config{
		Certificates: []tls.Certificate{s.cert}, MinVersion: tls.VersionTLS13,
		ClientAuth: tls.RequireAnyClientCert, InsecureSkipVerify: true, VerifyConnection: verify,
	}
}

func (s *tlsStream) Accept() (net.Conn, error) {
	c, err := s.ln.Accept()
	if err != nil {
		return nil, err
	}
	return tls.Server(c, s.config()), nil
}

func (s *tlsStream) Close() error   { return s.ln.Close() }
func (s *tlsStream) Addr() net.Addr { return s.addr }

func (s *tlsStream) Dial(addr raft.ServerAddress, timeout time.Duration) (net.Conn, error) {
	return tls.DialWithDialer(&net.Dialer{Timeout: timeout}, "tcp", string(addr), s.config())
}

// stopCluster shuts Raft down and closes its store.
func (n *Node) stopCluster() {
	if c := n.cl; c != nil && c.r != nil {
		c.active.Store(false)
		c.r.Shutdown().Error()
		c.store.Close()
	}
}
