package redisc

import (
	"errors"
	"fmt"
	"sync"

	"github.com/garyburd/redigo/redis"
)

var _ redis.Conn = (*Conn)(nil)

// Conn is a redis cluster connection. When returned by Cluster.Get
// or Cluster.Dial, it is not yet bound to any node in the cluster.
// Only when a call to Do, Send, Receive or Bind is made is a connection
// to a specific node established:
//
//     - if Do or Send is called first, the command's first parameter
//       is assumed to be the key, and its slot is used to find the node
//     - if Receive is called first, or if Do or Send is called first
//       but with no parameter for the command (or no command), a
//       random node is selected in the cluster
//     - if Bind is called first, the node corresponding to the slot of
//       the specified key(s) is selected
//
// Because Cluster.Get and Cluster.Dial return a redis.Conn interface,
// a type assertion must be used to call Bind on this concrete Conn type:
//
//     redisConn := cluster.Get()
//     if conn, ok := redisConn.(*redisc.Conn); ok {
//       if err := conn.Bind("my-key"); err != nil {
//         // handle error
//       }
//     }
//
type Conn struct {
	cluster   *Cluster
	forceDial bool

	// redigo allows concurrent reader and writer (conn.Receive and
	// conn.Send/conn.Flush), a mutex is needed to protect concurrent
	// accesses.
	mu  sync.Mutex
	err error
	rc  redis.Conn
}

// binds the connection to a specific node, the one holding the slot
// or a random node if slot is -1, iff the connection is not broken
// and is not already bound. It returns the redis conn, true if it
// successfully bound to this slot, or any error.
func (c *Conn) bind(slot int) (rc redis.Conn, ok bool, err error) {
	c.mu.Lock()
	rc, err = c.rc, c.err
	if err == nil {
		if rc == nil {
			conn, err2 := c.cluster.getConn(slot)
			if err2 != nil {
				err = err2
			} else {
				c.rc = conn
				ok = true
			}
		}
	}
	c.mu.Unlock()
	return rc, ok, err
}

func cmdSlot(cmd string, args []interface{}) int {
	slot := -1
	if len(args) > 0 {
		key := fmt.Sprintf("%v", args[0])
		slot = Slot(key)
	}
	return slot
}

// Bind binds the connection to the cluster node corresponding to
// the slot of the provided keys. If the keys don't belong to the
// same slot, an error is returned and the connection is not bound.
// If the connection is already bound, an error is returned.
func (c *Conn) Bind(keys ...string) error {
	slot := -1
	for _, k := range keys {
		ks := Slot(k)
		if slot != -1 && ks != slot {
			return errors.New("redisc: keys do not belong to the same slot")
		}
		slot = ks
	}

	_, ok, err := c.bind(slot)
	if err != nil {
		return err
	}
	if !ok {
		// was already bound
		return errors.New("redisc: connection already bound to a slot")
	}
	return nil
}

// Do sends a command to the server and returns the received reply.
// If the connection is not yet bound to a cluster node, it will be
// after this call, based on the rules documented in the Conn type.
func (c *Conn) Do(cmd string, args ...interface{}) (interface{}, error) {
	rc, _, err := c.bind(cmdSlot(cmd, args))
	if err != nil {
		return nil, err
	}
	return rc.Do(cmd, args...)
}

// Send writes the command to the client's output buffer. If the
// connection is not yet bound to a cluster node, it will be after
// this call, based on the rules documented in the Conn type.
func (c *Conn) Send(cmd string, args ...interface{}) error {
	rc, _, err := c.bind(cmdSlot(cmd, args))
	if err != nil {
		return err
	}
	return rc.Send(cmd, args...)
}

// Receive receives a single reply from the server. If the connection
// is not yet bound to a cluster node, it will be after this call,
// based on the rules documented in the Conn type.
func (c *Conn) Receive() (interface{}, error) {
	rc, _, err := c.bind(-1)
	if err != nil {
		return nil, err
	}
	return rc.Receive()
}

// Flush flushes the output buffer to the server.
func (c *Conn) Flush() error {
	c.mu.Lock()
	err := c.err
	if err == nil && c.rc != nil {
		err = c.rc.Flush()
	}
	c.mu.Unlock()
	return err
}

// Err returns a non-nil value if the connection is broken. Applications
// should close broken connections.
func (c *Conn) Err() error {
	c.mu.Lock()
	err := c.err
	if err == nil && c.rc != nil {
		err = c.rc.Err()
	}
	c.mu.Unlock()
	return err
}

// Close closes the connection.
func (c *Conn) Close() error {
	c.mu.Lock()
	err := c.err
	if err == nil {
		c.err = errors.New("redisc: closed")
		if c.rc != nil {
			err = c.rc.Close()
		}
	}
	c.mu.Unlock()
	return err
}
