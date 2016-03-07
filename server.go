package juggler

import (
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

// DiscardLog is a helper no-op function that can be assigned to LogFunc
// to disable logging.
func DiscardLog(f string, args ...interface{}) {}

// Subprotocols is the list of juggler protocol versions supported by this
// package. It should be set as-is on the websocket.Upgrader Subprotocols
// field.
var Subprotocols = []string{
	"juggler.0",
}

func isIn(list []string, v string) bool {
	for _, vv := range list {
		if vv == v {
			return true
		}
	}
	return false
}

// Server is a juggler server. Once a websocket handshake has been
// established with a juggler subprotocol over a standard HTTP server,
// the connections get served by this server.
//
// The fields should not be updated once a server has started
// serving connections.
type Server struct {
	// ReadLimit defines the maximum size, in bytes, of incoming
	// messages. If a client sends a message that exceeds this limit,
	// the connection is closed. The default of 0 means no limit.
	ReadLimit int64

	// ReadTimeout is the timeout to read an incoming message. It is
	// set on the websocket connection with SetReadDeadline before
	// reading each message. The default of 0 means no timeout.
	ReadTimeout time.Duration

	// WriteLimit defines the maximum size, in bytes, of outgoing
	// messages. If a message exceeds this limit, it is dropped and
	// an ERR message is sent to the client instead.
	WriteLimit int64

	// WriteTimeout is the timeout to write an outgoing message. It is
	// set on the websocket connection with SetWriteDeadline before
	// writing each message. The default of 0 means no timeout.
	WriteTimeout time.Duration

	// AcquireWriteLockTimeout is the time to wait for the exclusive
	// write lock for a connection. If the lock cannot be acquired
	// before the timeout, the connection is dropped.
	AcquireWriteLockTimeout time.Duration

	// ConnState specifies an optional callback function that is called
	// when a connection changes state. It is called for Connected and
	// Closing states.
	ConnState func(*Conn, ConnState)

	// ReadHandler is the handler that is called when an incoming
	// message is processed. The ProcessMsg function is called
	// if the default nil value is set. If a custom handler is set,
	// it is assumed that it will call ProcessMsg at some point,
	// or otherwise manually process the messages.
	ReadHandler Handler

	// WriteHandler is the handler that is called when an outgoing
	// message is processed. The ProcessMsg function is called
	// if the default nil value is set. If a custom handler is set,
	// it is assumed that it will call ProcessMsg at some point,
	// or otherwise manually process the messages.
	WriteHandler Handler

	// LogFunc is the function called to log events. By default,
	// it logs using log.Printf. Logging can be disabled by setting
	// LogFunc to DiscardLog.
	LogFunc func(string, ...interface{}) // TODO : normalize calls so that order of args is somewhat predictable

	PubSubBroker PubSubBroker
	RPCBroker    RPCBroker
}

// Upgrade returns an http.Handler that upgrades connections to
// the websocket protocol using upgrader. The websocket connection
// must be upgraded to a supported juggler subprotocol otherwise
// the connection is dropped.
//
// Once connected, the websocket connection is served via srv.
func Upgrade(upgrader *websocket.Upgrader, srv *Server) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// upgrade the HTTP connection to the websocket protocol
		wsConn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer wsConn.Close()

		// the agreed-upon subprotocol must be one of the supported ones.
		if wsConn.Subprotocol() == "" || !isIn(Subprotocols, wsConn.Subprotocol()) {
			logf(srv, "juggler: no supported subprotocol, closing connection")
			return
		}

		wsConn.SetReadLimit(srv.ReadLimit)
		c := newConn(wsConn, srv)
		if srv.ConnState != nil {
			defer func() {
				srv.ConnState(c, Closing)
			}()
		}

		// start lifecycle of the connection
		if srv.ConnState != nil {
			srv.ConnState(c, Connected)
		}

		kill := c.CloseNotify()
		// receive, results loop, pub/sub loop
		go c.receive()
		// TODO : result, pub/sub loop
		<-kill
	})
}

func logf(s *Server, f string, args ...interface{}) {
	if s.LogFunc != nil {
		s.LogFunc(f, args...)
	} else {
		log.Printf(f, args...)
	}
}
