package redisbroker

import (
	"encoding/json"
	"expvar"
	"fmt"
	"sync"
	"time"

	"github.com/mna/juggler/broker"
	"github.com/mna/juggler/message"
	"github.com/garyburd/redigo/redis"
)

var _ broker.CallsConn = (*callsConn)(nil)

// script to delete the key and return its TTL in ms
var delAndPTTLScript = redis.NewScript(1, `
	local res = redis.call("PTTL", KEYS[1])
	redis.call("DEL", KEYS[1])
	return res
`)

type callsConn struct {
	c       redis.Conn
	pool    Pool
	uris    []string
	timeout time.Duration
	logFn   func(string, ...interface{})
	vars    *expvar.Map

	// once makes sure only the first call to Calls starts the goroutine.
	once sync.Once
	ch   chan *message.CallPayload

	// errmu protects access to err.
	errmu sync.Mutex
	err   error
}

// Close closes the connection.
func (c *callsConn) Close() error {
	return c.c.Close()
}

// CallsErr returns the error that caused the Calls channel to close.
func (c *callsConn) CallsErr() error {
	c.errmu.Lock()
	err := c.err
	c.errmu.Unlock()
	return err
}

// Calls returns a stream of call requests for the URIs specified when
// creating the callsConn. For use in a redis cluster, all URIs must
// belong to the same cluster slot.
func (c *callsConn) Calls() <-chan *message.CallPayload {
	c.once.Do(func() {
		c.ch = make(chan *message.CallPayload)

		// compute all keys and timeout
		keys := make([]string, len(c.uris))
		for i, uri := range c.uris {
			keys[i] = fmt.Sprintf(callKey, uri)
		}
		to := int(c.timeout / time.Second)
		args := redis.Args{}.AddFlat(keys).Add(to)

		// make the poll connection cluster-aware if running in a cluster
		rc := clusterifyConn(c.c, keys...)

		go c.pollCalls(rc, args)
	})

	return c.ch
}

func (c *callsConn) pollCalls(pollConn redis.Conn, pollArgs redis.Args) {
	defer close(c.ch)

	wg := sync.WaitGroup{}
	for {
		// BRPOP returns array with [0]: key name, [1]: payload.
		v, err := redis.Values(pollConn.Do("BRPOP", pollArgs...))
		if err != nil {
			if err == redis.ErrNil {
				// no available value
				continue
			}

			// possibly a closed connection, in any case stop
			// the loop.
			c.errmu.Lock()
			c.err = err
			c.errmu.Unlock()
			wg.Wait()
			return
		}

		wg.Add(1)
		go c.sendCall(v, &wg)
	}
}

// receives the raw value retured from BRPOP.
func (c *callsConn) sendCall(v []interface{}, wg *sync.WaitGroup) {
	defer wg.Done()

	// unmarshal the payload
	var cp message.CallPayload
	if err := unmarshalBRPOPValue(&cp, v); err != nil {
		if c.vars != nil {
			c.vars.Add("FailedCallPayloadUnmarshals", 1)
		}
		logf(c.logFn, "Calls: BRPOP failed to unmarshal call payload: %v", err)
		return
	}

	// check if call is expired
	k := fmt.Sprintf(callTimeoutKey, cp.URI, cp.MsgUUID)

	rc := c.pool.Get()
	defer rc.Close()
	rc = clusterifyConn(rc, k)

	pttl, err := redis.Int(delAndPTTLScript.Do(rc, k))
	if err != nil {
		if c.vars != nil {
			c.vars.Add("FailedPTTLCalls", 1)
		}
		logf(c.logFn, "Calls: DEL/PTTL failed: %v", err)
		return
	}
	if pttl <= 0 {
		if c.vars != nil {
			c.vars.Add("ExpiredCalls", 1)
		}
		logf(c.logFn, "Calls: message %v expired, dropping call", cp.MsgUUID)
		return
	}

	cp.ReadTimestamp = time.Now().UTC()
	cp.TTLAfterRead = time.Duration(pttl) * time.Millisecond
	c.ch <- &cp
	if c.vars != nil {
		c.vars.Add("Calls", 1)
	}
}

func unmarshalBRPOPValue(dst interface{}, src []interface{}) error {
	var p []byte
	if _, err := redis.Scan(src, nil, &p); err != nil {
		return err
	}
	if err := json.Unmarshal(p, dst); err != nil {
		return err
	}
	return nil
}
