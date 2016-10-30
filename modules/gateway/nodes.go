package gateway

import (
	"errors"
	"net"
	"time"

	"github.com/NebulousLabs/Sia/build"
	"github.com/NebulousLabs/Sia/crypto"
	"github.com/NebulousLabs/Sia/encoding"
	"github.com/NebulousLabs/Sia/modules"
)

var (
	errNodeExists = errors.New("node already added")
	errNoNodes    = errors.New("no nodes in the node list")
	errOurAddress = errors.New("can't add our own address")
)

// addNode adds an address to the set of nodes on the network.
func (g *Gateway) addNode(addr modules.NetAddress) error {
	if addr == g.myAddr {
		return errOurAddress
	} else if _, exists := g.nodes[addr]; exists {
		return errNodeExists
	} else if addr.IsStdValid() != nil {
		return errors.New("address is not valid: " + string(addr))
	} else if net.ParseIP(addr.Host()) == nil {
		return errors.New("address must be an IP address: " + string(addr))
	}
	g.nodes[addr] = struct{}{}
	return nil
}

// managedAddUntrustedNode adds an address to the set of nodes on the network, but
// first verifies that there is a reachable node at the provided address.
func (g *Gateway) managedAddUntrustedNode(addr modules.NetAddress) error {
	// Performing the ping during testing does not work.
	if build.Release == "testing" {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.addNode(addr)
	}

	// Ping the untrusted node to see whether or not there's acutally a
	// reachable node at the provided address.
	conn, err := g.dial(addr)
	if err != nil {
		return err
	}
	// If connection succeeds, supply an unacceptable version so that we
	// will not be added as a peer.
	//
	// NOTE: this is a somewhat clunky way of specifying that you didn't
	// actually want a connection.
	encoding.WriteObject(conn, "0.0.0")
	var reject string
	err = encoding.ReadObject(conn, &reject, build.MaxEncodedVersionLength)
	if err != nil {
		g.log.Debugln("ERROR: version handshake ping terminated unexpectedly:", err)
	}
	if reject != "reject" {
		g.log.Debugln("WARN: peer does not seem to have correctly rejected our ping:", reject)
	}
	conn.Close()

	g.mu.Lock()
	defer g.mu.Unlock()
	err = g.addNode(addr)
	if err != nil {
		return err
	}
	return g.save()
}

// removeNode will remove a node from the gateway.
func (g *Gateway) removeNode(addr modules.NetAddress) error {
	if _, exists := g.nodes[addr]; !exists {
		return errors.New("no record of that node")
	}
	delete(g.nodes, addr)
	return nil
}

// randomNode returns a random node from the gateway. An error can be returned
// if there are no nodes in the node list.
func (g *Gateway) randomNode() (modules.NetAddress, error) {
	if len(g.nodes) == 0 {
		return "", errNoPeers
	}

	// Select a random peer. Note that the algorithm below is roughly linear in
	// the number of nodes known by the gateway, and this number can approach
	// every node on the network. If the network gets large, this algorithm
	// will either need to be refactored, or more likely a cap on the size of
	// g.nodes will need to be added.
	r, err := crypto.RandIntn(len(g.nodes))
	if err != nil {
		return "", err
	}
	for node := range g.nodes {
		if r <= 0 {
			return node, nil
		}
		r--
	}
	return "", errNoPeers
}

// shareNodes is the receiving end of the ShareNodes RPC. It writes up to 10
// randomly selected nodes to the caller.
func (g *Gateway) shareNodes(conn modules.PeerConn) error {
	conn.SetDeadline(time.Now().Add(connStdDeadline))

	// Assemble a list of nodes to send to the peer.
	var nodes []modules.NetAddress
	func() {
		g.mu.RLock()
		defer g.mu.RUnlock()

		// Create a random permutation of nodes from the gateway to iterate
		// through.
		gnodes := make([]modules.NetAddress, 0, len(g.nodes))
		for node := range g.nodes {
			gnodes = append(gnodes, node)
		}
		perm, err := crypto.Perm(len(g.nodes))
		if err != nil {
			g.log.Severe("Unable to get random permutation for sharing nodes")
		}

		// Iterate through the random permutation of nodes and select the
		// desirable ones.
		remoteNA := modules.NetAddress(conn.RemoteAddr().String())
		for _, i := range perm {
			// Don't share local peers with remote peers. That means that if 'node'
			// is loopback, it will only be shared if the remote peer is also
			// loopback. And if 'node' is private, it will only be shared if the
			// remote peer is either the loopback or is also private.
			node := gnodes[i]
			if node.IsLoopback() && !remoteNA.IsLoopback() {
				continue
			}
			if node.IsLocal() && !remoteNA.IsLocal() {
				continue
			}

			nodes = append(nodes, node)
			if uint64(len(nodes)) == maxSharedNodes {
				break
			}
		}
	}()
	return encoding.WriteObject(conn, nodes)
}

// requestNodes is the calling end of the ShareNodes RPC.
func (g *Gateway) requestNodes(conn modules.PeerConn) error {
	conn.SetDeadline(time.Now().Add(connStdDeadline))

	var nodes []modules.NetAddress
	if err := encoding.ReadObject(conn, &nodes, maxSharedNodes*modules.MaxEncodedNetAddressLength); err != nil {
		return err
	}

	g.mu.Lock()
	for _, node := range nodes {
		err := g.addNode(node)
		if err != nil && err != errNodeExists && err != errOurAddress {
			g.log.Printf("WARN: peer '%v' sent the invalid addr '%v'", conn.RPCAddr(), node)
		}
	}
	err := g.save()
	if err != nil {
		g.log.Println("WARN: failed to save nodelist after requesting nodes:", err)
	}
	g.mu.Unlock()
	return nil
}

// permanentNodePurger is a thread that runs throughout the lifetime of the
// gateway, purging unconnectable nodes from the node list in a sustainable
// way.
func (g *Gateway) permanentNodePurger(closeChan chan struct{}) {
	defer close(closeChan)

	for {
		// Start by sleeping for 10 minutes. This means that no nodes will be
		// purged for the first 10 minutes that Sia is running, and that any
		// failures to get nodes will be met by 10 minutes of waiting.
		//
		// At most one node will be contacted every 10 minutes. This minimizes
		// the total amount of keepalive traffic on the network.
		select {
		case <-time.After(nodePurgeDelay):
		case <-g.threads.StopChan():
			// The gateway is shutting down, close out the thread.
			return
		}

		// Get a random node for scanning.
		g.mu.RLock()
		numNodes := len(g.nodes)
		node, err := g.randomNode()
		g.mu.RUnlock()
		if err == errNoNodes {
			// errNoNodes is a common error that will be resovled by the
			// bootstrap process.
			continue
		} else if err != nil {
			// Unusual error, create a logging statement.
			g.log.Println("ERROR: could not pick a random node for uptime check:", err)
			continue
		}
		if numNodes <= pruneNodeListLen {
			// There are not enough nodes in the gateway - pruning more is
			// probably a bad idea, and may affect the user's ability to
			// connect to the network in the future.
			continue
		}
		// Check whether this node is already a peer. If so, no need to dial
		// them.
		g.mu.RLock()
		_, exists := g.peers[node]
		g.mu.RUnlock()
		if exists {
			continue
		}

		// Try connecting to the random node. If the node is not reachable,
		// remove them from the node list.
		conn, err := g.dial(node)
		if err != nil {
			// NOTE: an error may be returned if the dial is cancelled
			// partway through, which would cause the node to be pruned
			// even though it may be a good node. Because nodes are
			// plentiful, that's not a huge problem.
			g.mu.Lock()
			g.removeNode(node)
			g.save()
			g.mu.Unlock()
			g.log.Debugf("INFO: removing node %q because dialing it failed: %v", node, err)
			continue
		}

		// If connection succeeds, supply an unacceptable version so that we
		// will not be added as a peer.
		//
		// NOTE: this is a somewhat clunky way of specifying that you didn't
		// actually want a connection.
		encoding.WriteObject(conn, "0.0.0")
		var reject string
		err = encoding.ReadObject(conn, &reject, build.MaxEncodedVersionLength)
		if err != nil {
			g.log.Debugln("ERROR: version handshake ping terminated unexpectedly:", err)
		}
		if reject != "reject" {
			g.log.Debugln("WARN: peer does not seem to have correctly rejected our ping:", reject)
		}
		conn.Close()
	}
}

// permanentNodeManager tries to keep the Gateway's node list healthy. As long
// as the Gateway has fewer than healthyNodeListLen nodes, it asks a random
// peer for more nodes. It also continually pings nodes in order to establish
// their connectivity. Unresponsive nodes are aggressively removed.
func (g *Gateway) permanentNodeManager(closeChan chan struct{}) {
	defer close(closeChan)

	for {
		// Wait 5 seconds so that a controlled number of node requests are made
		// to peers.
		select {
		case <-time.After(nodeListDelay):
		case <-g.threads.StopChan():
			// Gateway is shutting down, close the thread.
			return
		}

		g.mu.RLock()
		numNodes := len(g.nodes)
		peer, err := g.randomOutboundPeer()
		g.mu.RUnlock()
		if err == errNoPeers {
			// errNoPeers is a common and expected error, there's no need to
			// log it.
			continue
		} else if err != nil {
			g.log.Println("ERROR: could not fetch a random peer:", err)
			continue
		}

		// Determine whether there are a satisfactory number of nodes in the
		// nodelist. If there are not, use the random peer from earlier to
		// expand the node list.
		if numNodes < healthyNodeListLen {
			err := g.managedRPC(peer, "ShareNodes", g.requestNodes)
			if err != nil {
				g.log.Debugf("WARN: RPC ShareNodes failed on peer %q: %v", peer, err)
				continue
			}
		} else {
			// There are enough nodes in the gateway, no need to check for more
			// every 5 seconds. Wait a while before checking again.
			select {
			case <-time.After(wellConnectedDelay):
			case <-g.threads.StopChan():
				// Gateway is shutting down, close the thread.
				return
			}
		}
	}
}
