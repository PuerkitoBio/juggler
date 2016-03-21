// Package redistest provides test helpers to manage a redis server.
package redistest

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/garyburd/redigo/redis"
	"github.com/stretchr/testify/require"
)

// ClusterConfig is the configuration to use for servers started in
// redis-cluster mode. The value must contain a single reference to
// a string placeholder (%s), the port number.
var ClusterConfig = `
port %s
cluster-enabled yes
cluster-config-file nodes.%[1]s.conf
cluster-node-timeout 5000
appendonly yes
`

// StartServer starts a redis-server instance on a free port.
// It returns the started *exec.Cmd and the port used. The caller
// should make sure to stop the command. If the redis-server
// command is not found in the PATH, the test is skipped.
//
// If w is not nil, both stdout and stderr of the server are
// written to it. If a configuration is specified, it is supplied
// to the server via stdin.
func StartServer(t *testing.T, w io.Writer, conf string) (*exec.Cmd, string) {
	if _, err := exec.LookPath("redis-server"); err != nil {
		t.Skip("redis-server not found in $PATH")
	}

	port := getFreePort(t)
	return startServerWithConfig(t, port, w, conf), port
}

// StartCluster starts a redis cluster of 3 nodes using the
// ClusterConfig variable as configuration. If w is not nil,
// stdout and stderr of each node will be written to it.
//
// It returns a function that should be called after the test
// (typically in a defer), and the list of ports for all nodes
// in the cluster.
func StartCluster(t *testing.T, w io.Writer) (func(), []string) {
	if _, err := exec.LookPath("redis-server"); err != nil {
		t.Skip("redis-server not found in $PATH")
	}

	const (
		numNodes  = 3
		hashSlots = 16384
		maxPort   = 55535
	)

	cmds := make([]*exec.Cmd, numNodes)
	ports := make([]string, numNodes)
	slotsPerNode := hashSlots / numNodes

	for i := 0; i < numNodes; i++ {
		// the port number in a redis-cluster must be below 55535 because
		// the nodes communicate with others on port p+10000. Try to get
		// lucky and subtract 10000 from the random port received if it
		// is too high.
		port := getFreePort(t)
		if n, _ := strconv.Atoi(port); n >= maxPort {
			port = strconv.Itoa(n - 10000)
		}
		cmd := startServerWithConfig(t, port, w, fmt.Sprintf(ClusterConfig, port))
		cmds[i], ports[i] = cmd, port

		// configure the cluster - add the slots
		conn, err := redis.Dial("tcp", ":"+port)
		require.NoError(t, err, "Dial to node %d", i)
		args := redis.Args{"ADDSLOTS"}
		for j := i * slotsPerNode; j < ((i + 1) * slotsPerNode); j++ {
			args = args.Add(j)
		}
		if i == numNodes-1 {
			// add all missing slots to the last node
			for j := slotsPerNode * numNodes; j < hashSlots; j++ {
				args = args.Add(j)
			}
		}

		_, err = conn.Do("CLUSTER", args...)
		require.NoError(t, err, "CLUSTER ADDSLOTS for %d", i)

		if i != 0 {
			// join the cluster
			_, err = conn.Do("CLUSTER", "MEET", "127.0.0.1", ports[i-1])
			require.NoError(t, err, "CLUSTER MEET for %d", i)
		}
		conn.Close()
	}

	// wait for the cluster to catch up
	var ok bool
	conn, err := redis.Dial("tcp", ":"+ports[0])
	require.NoError(t, err, "Dial to node %s", ports[0])
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s, err := redis.Values(conn.Do("CLUSTER", "SLOTS"))
		require.NoError(t, err, "CLUSTER SLOTS")
		if len(s) >= numNodes {
			ok = true
			break
		}
	}
	conn.Close()
	require.True(t, ok, "wait for cluster to form")

	return func() {
		for _, c := range cmds {
			c.Process.Kill()
		}
	}, ports
}

func startServerWithConfig(t *testing.T, port string, w io.Writer, conf string) *exec.Cmd {
	var args []string
	if conf == "" {
		args = []string{"--port", port}
	} else {
		args = []string{"-"}
	}
	c := exec.Command("redis-server", args...)
	c.Dir = os.TempDir()

	if w != nil {
		c.Stderr = w
		c.Stdout = w
	}
	if conf != "" {
		c.Stdin = strings.NewReader(conf)
	}

	require.NoError(t, c.Start(), "start redis-server")

	// wait for the server to start accepting connections
	var ok bool
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", ":"+port, time.Second)
		if err == nil {
			ok = true
			conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.True(t, ok, "wait for redis-server to start")

	t.Logf("redis-server started on port %s", port)
	return c
}

func getFreePort(t *testing.T) string {
	l, err := net.Listen("tcp", ":0")
	require.NoError(t, err, "listen on port 0")
	defer l.Close()
	_, p, err := net.SplitHostPort(l.Addr().String())
	require.NoError(t, err, "parse host and port")
	return p
}

// NewPool creates a redis pool to return connections on the specified
// addr.
func NewPool(t *testing.T, addr string) *redis.Pool {
	return &redis.Pool{
		MaxIdle:     2,
		MaxActive:   10,
		IdleTimeout: time.Minute,
		Dial: func() (redis.Conn, error) {
			return redis.Dial("tcp", addr)
		},
		TestOnBorrow: func(c redis.Conn, t time.Time) error {
			_, err := c.Do("PING")
			return err
		},
	}
}
