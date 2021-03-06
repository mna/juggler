package juggler_test

import (
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/context"

	"github.com/mna/juggler"
	"github.com/mna/juggler/broker/redisbroker"
	"github.com/mna/juggler/client"
	"github.com/mna/juggler/internal/wstest"
	"github.com/mna/juggler/message"
	"github.com/mna/redisc/redistest"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServerServe(t *testing.T) {
	cmd, port := redistest.StartServer(t, nil, "")
	defer cmd.Process.Kill()

	done := make(chan bool, 1)
	srv := wstest.StartRecordingServer(t, done, ioutil.Discard)
	defer srv.Close()

	pool := redistest.NewPool(t, ":"+port)
	broker := &redisbroker.Broker{
		Pool: pool,
		Dial: pool.Dial,
	}

	conn := wstest.Dial(t, srv.URL)
	defer conn.Close()

	state := make(chan juggler.ConnState)
	fn := func(c *juggler.Conn, cs juggler.ConnState) {
		select {
		case state <- cs:
		case <-time.After(100 * time.Millisecond):
			assert.Fail(t, "could not send state %v", cs)
		}
	}
	server := &juggler.Server{ConnState: fn, CallerBroker: broker, PubSubBroker: broker}

	go server.ServeConn(conn)

	var got juggler.ConnState
	select {
	case got = <-state:
		assert.Equal(t, juggler.Accepting, got, "received accepting connection state")
	case <-time.After(100 * time.Millisecond):
		assert.Fail(t, "no accepting state received")
	}

	select {
	case got = <-state:
		assert.Equal(t, juggler.Connected, got, "received connected connection state")
	case <-time.After(100 * time.Millisecond):
		assert.Fail(t, "no connected state received")
	}

	// closing the underlying websocket connection causes the juggler connection
	// to close too.
	conn.Close()

	select {
	case got = <-state:
		assert.Equal(t, juggler.Closed, got, "received closed connection state")
	case <-time.After(100 * time.Millisecond):
		assert.Fail(t, "no closed state received")
	}
}

func TestUpgrade(t *testing.T) {
	cmd, port := redistest.StartServer(t, nil, "")
	defer cmd.Process.Kill()

	pool := redistest.NewPool(t, ":"+port)
	broker := &redisbroker.Broker{
		Pool: pool,
		Dial: pool.Dial,
	}

	server := &juggler.Server{CallerBroker: broker, PubSubBroker: broker}
	upg := &websocket.Upgrader{Subprotocols: juggler.Subprotocols}
	srv := httptest.NewServer(juggler.Upgrade(upg, server))
	srv.URL = strings.Replace(srv.URL, "http:", "ws:", 1)
	defer srv.Close()

	h := client.HandlerFunc(func(ctx context.Context, m message.Msg) {})

	// ******* DIAL #1 ********
	// valid subprotocol
	// ******* DIAL #1 ********
	cli, err := client.Dial(&websocket.Dialer{Subprotocols: juggler.Subprotocols}, srv.URL, nil, client.SetHandler(h))
	require.NoError(t, err, "Dial 1")
	cli.Close()
	select {
	case <-cli.CloseNotify():
	case <-time.After(100 * time.Millisecond):
		assert.Fail(t, "no close signal received for Dial 1")
	}

	// ******* DIAL #2 ********
	// invalid subprotocol, websocket connection will be closed
	// ******* DIAL #2 ********
	cli, err = client.Dial(&websocket.Dialer{}, srv.URL, http.Header{"Sec-WebSocket-Protocol": {"test"}}, client.SetHandler(h))
	require.NoError(t, err, "Dial 2")
	// no need to call Close, Upgrade will refuse the connection
	select {
	case <-cli.CloseNotify():
	case <-time.After(100 * time.Millisecond):
		assert.Fail(t, "no close signal received for Dial 2")
	}
	cli.Close()

	// ******* DIAL #3 ********
	// call with a restricted list of allowed messages
	// ******* DIAL #3 ********
	cli, err = client.Dial(&websocket.Dialer{Subprotocols: juggler.Subprotocols}, srv.URL, http.Header{"Juggler-Allowed-Messages": {"call, pub"}}, client.SetHandler(h))
	require.NoError(t, err, "Dial 3")

	// make a call, should work
	_, err = cli.Call("u", "c1", time.Second)
	assert.NoError(t, err, "Call is allowed")
	select {
	case <-cli.CloseNotify():
		assert.Fail(t, "Call caused the connection to close")
	case <-time.After(100 * time.Millisecond):
	}

	// make a pub, should work
	_, err = cli.Pub("c", "p1")
	assert.NoError(t, err, "Pub is allowed")
	select {
	case <-cli.CloseNotify():
		assert.Fail(t, "Pub caused the connection to close")
	case <-time.After(100 * time.Millisecond):
	}

	// subscribe should not work, and should close the client (may not immediately return an error though)
	cli.Sub("c", false)
	select {
	case <-cli.CloseNotify():
	case <-time.After(100 * time.Millisecond):
		assert.Fail(t, "no close signal received for Dial 3")
	}
	cli.Close()
}
