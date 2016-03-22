// Package redisc implements a redis cluster client on top of
// the redigo client package. It supports all commands that can
// be executed on a redis cluster, including pub-sub and scripts.
//
// Cluster
//
// The Cluster type manages a redis cluster and offers an
// interface compatible with redigo's redis.Pool:
//
//     - Get() redis.Conn
//     - Close() error
//
// If the Cluster.CreatePool function field is set, then a
// redis.Pool is created to manage connections to each of the
// cluster's nodes. A call to Cluster.Get then returns a connection
// from this pool.
//
// The Cluster.Dial method, on the other hand, guarantees that
// the returned connection will not be managed by a pool, even if
// Cluster.CreatePool is set. It calls redigo's redis.Dial function
// to create the unpooled connection. If the cluster's
// CreatePool field is nil, Cluster.Get behaves the same as
// Cluster.Dial.
//
// A cluster must be closed once it is no longer used to release
// its resources.
//
// Connection
//
// The connection returned from Get or Dial is a redigo's redis.Conn
// interface, with a concrete type of *Conn. In addition to the interface's
// required methods, *Conn adds the Bind method.
//
// The returned connection is not yet connected to any node; it is
// "bound" to a specific node only when a call to Do, Send, Receive
// or Bind is made. For Do, Send and Receive, to select the right
// cluster node, it uses the first parameter of the command, and
// computes the hash slot assuming that first parameter is a key.
// It then binds the connection to the node corresponding to that
// slot. If there are no parameters for the command, or if there is
// no command (e.g. in a call to Receive), a random node is selected.
//
// Bind is different, it gives explicit control to the caller over
// which node to select by specifying a list of keys that the caller
// wishes to handle with the connection. All keys must belong to the
// same slot, and the connection must not be already bound to a node,
// otherwise an error is returned. On success, the connection is
// bound the the node holding the slot of the specified keys.
//
// Because the connection is returned as a redis.Conn interface, a
// type assertion must be used to access the underlying *Conn and
// to be able to call Bind:
//
//     redisConn := cluster.Get()
//     if conn, ok := redisConn.(*redisc.Conn); ok {
//       if err := conn.Bind("my-key"); err != nil {
//         // handle error
//       }
//     }
//
// The connection must be closed after use, to release the underlying
// resources.
//
// Redirections
//
// The redis cluster may return MOVED and ASK errors when the node
// that received the command doesn't currently hold the slot corresponding
// to the key. The package cannot reliably handle those redirections
// automatically because the redirection error may be returned for
// a pipeline of commands, some of which may have succeeded.
//
// However, a connection can be wrapped by a call to RetryConn, which
// returns a redis.Conn interface where only calls to Do, Close and Err
// succeed. That means pipelining is not supported, and only a single
// command can be executed at a time, but it will automatically handle
// MOVED and ASK replies, up to Cluster.MaxAttempts. It also supports
// script execution by passing this connection to redigo's redis.Script.Do,
// which only calls the Do method of the connection.
//
// Note that even if RetryConn is not used, the cluster always updates
// its mapping of slots to nodes automatically by keeping track of
// MOVED replies.
//
package redisc
