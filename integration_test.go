package juggler_test

import (
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/context"

	"github.com/PuerkitoBio/exp/juggler"
	"github.com/PuerkitoBio/exp/juggler/broker/redisbroker"
	"github.com/PuerkitoBio/exp/juggler/callee"
	"github.com/PuerkitoBio/exp/juggler/internal/jugglertest"
	"github.com/PuerkitoBio/exp/juggler/internal/redistest"
	"github.com/PuerkitoBio/exp/juggler/msg"
	"github.com/gorilla/websocket"
)

// IntgConfig holds the configuration of an integration test execution.
type IntgConfig struct {
	RedisPoolMaxActive   int
	RedisPoolMaxIdle     int
	RedisPoolIdleTimeout time.Duration

	BrokerBlockingTimeout time.Duration
	BrokerCallCap         int
	BrokerResultCap       int

	ServerReadLimit               int64
	ServerReadTimeout             time.Duration
	ServerWriteLimit              int64
	ServerWriteTimeout            time.Duration
	ServerAcquireWriteLockTimeout time.Duration

	NCallees          int // number of callees listening on the URIs
	NWorkersPerCallee int // number of workers per callee
	NClients          int // number of clients generating calls

	NURIs         int           // number of different URIs to pick from at random
	NChannels     int           // number of different channels to pick from at random
	Duration      time.Duration // duration of the test
	ClientMsgRate time.Duration // send a message at this rate
	ServerPubRate time.Duration // publish a server-side event at this rate
	ThunkDelay    time.Duration // artificial delay of the call
}

type runStats struct {
	Call    int64
	Pub     int64
	Sub     int64
	Unsb    int64
	Exp     int64
	OK      int64
	Err     int64
	Res     int64
	Evnt    int64
	Unknown int64
}

func TestIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration tests don't run with the -short flag")
	}
	runIntegrationTest(t, &IntgConfig{}) // TODO : parse flags into IntgConfig
}

func incStats(stats *runStats, m msg.Msg) {
	switch m.Type() {
	case msg.CallMsg:
		atomic.AddInt64(&stats.Call, 1)
	case msg.PubMsg:
		atomic.AddInt64(&stats.Pub, 1)
	case msg.SubMsg:
		atomic.AddInt64(&stats.Sub, 1)
	case msg.UnsbMsg:
		atomic.AddInt64(&stats.Unsb, 1)
	case msg.ExpMsg:
		atomic.AddInt64(&stats.Exp, 1)
	case msg.OKMsg:
		atomic.AddInt64(&stats.OK, 1)
	case msg.ErrMsg:
		atomic.AddInt64(&stats.Err, 1)
	case msg.ResMsg:
		atomic.AddInt64(&stats.Res, 1)
	case msg.EvntMsg:
		atomic.AddInt64(&stats.Evnt, 1)
	default:
		atomic.AddInt64(&stats.Unknown, 1)
	}
}

func serverStatsHandler(stats *runStats) juggler.Handler {
	return juggler.HandlerFunc(func(ctx context.Context, c *juggler.Conn, m msg.Msg) {
		incStats(stats, m)
		juggler.ProcessMsg(ctx, c, m)
	})
}

func clientStatsHandler(stats *runStats) juggler.ClientHandler {
	return juggler.ClientHandlerFunc(func(ctx context.Context, c *juggler.Client, m msg.Msg) {
		incStats(stats, m)
	})
}

func runIntegrationTest(t *testing.T, conf *IntgConfig) {
	dbgl := &jugglertest.DebugLog{T: t}

	// start/create:
	// 1. redis-server
	// 2. redis pool and broker
	// 3. juggler server
	// 4. m callees
	// 5. n clients

	// 1. redis-server
	cmd, port := redistest.StartServer(t, nil)
	defer cmd.Process.Kill()

	// 2. create the redis pool and broker
	pool := redistest.NewPool(t, ":"+port)
	pool.MaxActive = conf.RedisPoolMaxActive
	pool.MaxIdle = conf.RedisPoolMaxIdle
	pool.IdleTimeout = conf.RedisPoolIdleTimeout
	brk := &redisbroker.Broker{
		Pool:    pool,
		Dial:    pool.Dial,
		LogFunc: dbgl.Printf,

		BlockingTimeout: conf.BrokerBlockingTimeout,
		CallCap:         conf.BrokerCallCap,
		ResultCap:       conf.BrokerResultCap,
	}

	// 3. create the juggler server
	var srvStats runStats
	srv := &juggler.Server{
		CallerBroker: brk,
		PubSubBroker: brk,
		LogFunc:      dbgl.Printf,

		ReadHandler:  serverStatsHandler(&srvStats),
		WriteHandler: serverStatsHandler(&srvStats),

		ReadLimit:               conf.ServerReadLimit,
		ReadTimeout:             conf.ServerReadTimeout,
		WriteLimit:              conf.ServerWriteLimit,
		WriteTimeout:            conf.ServerWriteTimeout,
		AcquireWriteLockTimeout: conf.ServerAcquireWriteLockTimeout,
	}
	upg := &websocket.Upgrader{Subprotocols: juggler.Subprotocols}
	httpsrv := httptest.NewServer(juggler.Upgrade(upg, srv))
	defer httpsrv.Close()

	// TODO : get URIs
	uris := []string{}
	thunk := func(cp *msg.CallPayload) (interface{}, error) {
		time.Sleep(conf.ThunkDelay)
		return "ok", nil
	}

	// 4. start m callees
	calleeStarted := make(chan struct{})
	for i := 0; i < conf.NCallees; i++ {
		go func() {
			cle := callee.Callee{
				Broker:  brk,
				LogFunc: dbgl.Printf,
			}

			conn, err := brk.Calls(uris...)
			if err != nil {
				t.Fatalf("failed to get CallsConn: %v", err)
			}
			defer conn.Close()
			ch := conn.Calls()

			for j := 0; j < conf.NWorkersPerCallee; j++ {
				go func() {
					calleeStarted <- struct{}{}
					for cp := range ch {
						if err := cle.InvokeAndStoreResult(cp, thunk); err != nil {
							t.Fatalf("InvokeAndStoreResult failed: %v", err)
						}
					}
				}()
			}
		}()
	}

	// 5. start n clients
	var clientStats runStats
	clientStarted := make(chan struct{})
	wg := sync.WaitGroup{}
	wg.Add(conf.NClients)

	for i := 0; i < conf.NClients; i++ {
		go func() {
			defer wg.Done()

			cli, err := juggler.Dial(&websocket.Dialer{}, strings.Replace(httpsrv.URL, "http:", "ws:", 1), nil,
				juggler.SetHandler(clientStatsHandler(&clientStats)))
			if err != nil {
				t.Fatalf("Dial failed: %v", err)
			}
			_ = cli

			// TODO : run predetermined requests...
			clientStarted <- struct{}{}
		}()
	}

	// wait for callees to come online
	for i, cnt := 0, conf.NCallees*conf.NWorkersPerCallee; i < cnt; i++ {
		<-calleeStarted
	}
	// start clients
	for i := 0; i < conf.NClients; i++ {
		<-clientStarted
	}
	// wait for completion
	wg.Wait()
}
