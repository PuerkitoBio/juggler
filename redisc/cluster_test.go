package redisc

import (
	"testing"

	"github.com/PuerkitoBio/juggler/internal/redistest"
	"github.com/stretchr/testify/assert"
)

func TestRefreshMappingNonCluster(t *testing.T) {
	cmd, port := redistest.StartServer(t, nil, "")
	defer cmd.Process.Kill()

	c := &Cluster{
		StartupNodes: []string{":" + port},
	}
	err := c.RefreshMapping()
	if assert.Error(t, err, "RefreshMapping") {
		assert.Contains(t, err.Error(), "redisc: all nodes failed", "expected error message")
	}
}

func TestRefreshMappingCluster(t *testing.T) {
	fn, ports := redistest.StartCluster(t, nil)
	defer fn()

	for i, p := range ports {
		ports[i] = ":" + p
	}
	c := &Cluster{
		StartupNodes: ports,
	}

	err := c.RefreshMapping()
	assert.NoError(t, err, "RefreshMapping")
}