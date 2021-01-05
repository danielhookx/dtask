package ws

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/gorilla/websocket"
)

const (
	defaultWSWriteChanCapacity = 1000
	defaultWSWriteWait         = 10 * time.Second
	defaultWSReadWait          = 30 * time.Second
	defaultWSPingPeriod        = (defaultWSReadWait * 9) / 10
	defaultReadLimit           = 1024 * 8 //8M
)

// A single websocket connection contains listener id, underlying ws
// connection, and the event switch for subscribing to events.
//
// In case of an error, the connection is stopped.
type WsConnection struct {
	BaseService

	remoteAddr string
	baseConn   *websocket.Conn
	writeChan  chan interface{}

	// extra info that app injected into WsConnection, which is maybe needed in the msgRecvCb or msgSendCb
	args interface{}

	// maximum size for a message read from the peer
	readLimit int64

	// write channel capacity
	writeChanCapacity int

	// each write times out after this.
	writeWait time.Duration

	// Connection times out if we haven't received *anything* in this long, not even pings.
	readWait time.Duration

	// Send pings to server with this period. Must be less than readWait, but greater than zero.
	pingPeriod time.Duration

	msgRecvCb func(ctx context.Context, wsc *WsConnection, msg []byte)
	msgSendCb func(wsc *WsConnection, message interface{}) ([]byte, error)
	closeCb   func(wsc *WsConnection)

	closeCode int
	closeMsg  string
}

// NewWSConnection wraps websocket.Conn.
//
// See the commentary on the func(*wsConnection) functions for a detailed
// description of how to configure ping period and pong wait time. NOTE: if the
// write buffer is full, pongs may be dropped, which may cause clients to
// disconnect. see https://github.com/gorilla/websocket/issues/97
func NewWSConnection(baseConn *websocket.Conn, args interface{}, options ...func(*WsConnection)) *WsConnection {
	wsc := &WsConnection{
		remoteAddr:        baseConn.RemoteAddr().String(),
		baseConn:          baseConn,
		args:              args,
		readLimit:         defaultReadLimit,
		writeWait:         defaultWSWriteWait,
		writeChanCapacity: defaultWSWriteChanCapacity,
		readWait:          defaultWSReadWait,
		pingPeriod:        defaultWSPingPeriod,
	}
	for _, option := range options {
		option(wsc)
	}
	wsc.BaseService = *NewBaseService(nil, "WsConnection", wsc)
	return wsc
}

// MsgRecvCb sets the callback on msg received from websocket connection.
// It should only be used in the constructor - not Goroutine-safe.
func MsgRecvCb(cb func(ctx context.Context, wsc *WsConnection, msg []byte)) func(*WsConnection) {
	return func(wsc *WsConnection) {
		wsc.msgRecvCb = cb
	}
}

// MsgSendCb sets the callback on msg to send to websocket connection.
// It should only be used in the constructor - not Goroutine-safe.
func MsgSendCb(cb func(wsc *WsConnection, message interface{}) ([]byte, error)) func(*WsConnection) {
	return func(wsc *WsConnection) {
		wsc.msgSendCb = cb
	}
}

// CloseCb sets the callback on the connection closed.
// It should only be used in the constructor - not Goroutine-safe.
func CloseCb(cb func(wsc *WsConnection)) func(*WsConnection) {
	return func(wsc *WsConnection) {
		wsc.closeCb = cb
	}
}

func WriteReadLimit(limit int64) func(*WsConnection) {
	return func(wsc *WsConnection) {
		wsc.readLimit = limit
	}
}

// WriteWait sets the amount of time to wait before a websocket write times out.
// It should only be used in the constructor - not Goroutine-safe.
func WriteWait(writeWait time.Duration) func(*WsConnection) {
	return func(wsc *WsConnection) {
		wsc.writeWait = writeWait
	}
}

// WriteChanCapacity sets the capacity of the websocket write channel.
// It should only be used in the constructor - not Goroutine-safe.
func WriteChanCapacity(cap int) func(*WsConnection) {
	return func(wsc *WsConnection) {
		wsc.writeChanCapacity = cap
	}
}

// ReadWait sets the amount of time to wait before a websocket read times out.
// It should only be used in the constructor - not Goroutine-safe.
func ReadWait(readWait time.Duration) func(*WsConnection) {
	return func(wsc *WsConnection) {
		wsc.readWait = readWait
	}
}

// PingPeriod sets the duration for sending websocket pings.
// It should only be used in the constructor - not Goroutine-safe.
func PingPeriod(pingPeriod time.Duration) func(*WsConnection) {
	return func(wsc *WsConnection) {
		wsc.pingPeriod = pingPeriod
	}
}

// Args return extra info injected into wsc
func (wsc *WsConnection) Args() interface{} {
	return wsc.args
}

// OnStart implements cmn.Service by starting the read and write routines. It
// blocks until the connection closes.
func (wsc *WsConnection) OnStart() error {
	wsc.writeChan = make(chan interface{}, wsc.writeChanCapacity)

	// Read subscriptions/unsubscriptions to events
	go wsc.readRoutine()
	// Write responses, BLOCKING.
	wsc.writeRoutine()

	return nil
}

// OnStop implements cmn.Service by unsubscribing remoteAddr from all subscriptions.
func (wsc *WsConnection) OnStop() {
	// Both read and write loops close the websocket connection when they exit their loops.
	// The writeChan is never closed
	wsc.closeCb(wsc)
}

// GetRemoteAddr returns the remote address of the underlying connection.
// It implements WSRPCConnection
func (wsc *WsConnection) GetRemoteAddr() string {
	return wsc.remoteAddr
}

// WriteResponse pushes a response to the writeChan, and blocks until it is accepted.
// It implements WSRPCConnection. It is Goroutine-safe.
func (wsc *WsConnection) WriteResponse(resp interface{}) {
	select {
	case <-wsc.Quit():
		return
	case wsc.writeChan <- resp:
	}
}

// TryWriteResponse attempts to push a response to the writeChan, but does not block.
// It implements WSRPCConnection. It is Goroutine-safe
func (wsc *WsConnection) TryWriteResponse(resp interface{}) bool {
	select {
	case <-wsc.Quit():
		return false
	case wsc.writeChan <- resp:
		return true
	default:
		return false
	}
}

// Read from the socket and subscribe to or unsubscribe from events
func (wsc *WsConnection) readRoutine() {
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		//链接断开，取消收消息的后续操作
		cancel()
		if r := recover(); r != nil {
			err, ok := r.(error)
			if !ok {
				err = fmt.Errorf("WSJSONRPC: %v", r)
			}
			wsLog.Error("Panic in WSJSONRPC handler", "err", err, "stack", string(debug.Stack()))
			// todo
			// wsc.WriteResponse(types.RPCInternalError("unknown", err))
			go wsc.readRoutine()
		} else {
			wsc.baseConn.Close() // nolint: errcheck
		}
	}()

	wsc.baseConn.SetPongHandler(func(m string) error {
		return wsc.baseConn.SetReadDeadline(time.Now().Add(wsc.readWait))
	})

	for {
		select {
		case <-wsc.Quit():
			return
		default:
			// reset deadline for every type of message (control or data)
			if err := wsc.baseConn.SetReadDeadline(time.Now().Add(wsc.readWait)); err != nil {
				wsLog.Error("failed to set read deadline", "err", err)
			}
			var msg []byte
			_, msg, err := wsc.baseConn.ReadMessage()
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
					wsLog.Info("Client closed the connection")
				} else {
					wsLog.Error("Failed to read request", "err", err)
				}
				err := wsc.Stop()
				if err != nil {
					wsLog.Error("Failed to Stop", "err", err)
				}
				return
			}

			// logic code
			wsc.msgRecvCb(ctx, wsc, msg)
		}
	}
}

// receives on a write channel and writes out on the socket
func (wsc *WsConnection) writeRoutine() {
	pingTicker := time.NewTicker(wsc.pingPeriod)
	defer func() {
		pingTicker.Stop()
		if err := wsc.baseConn.Close(); err != nil {
			wsLog.Error("Error closing connection", "err", err)
		}
	}()

	// https://github.com/gorilla/websocket/issues/97
	pongs := make(chan string, 1)
	wsc.baseConn.SetPingHandler(func(m string) error {
		select {
		case pongs <- m:
		default:
		}
		return nil
	})

	for {
		select {
		case m := <-pongs:
			err := wsc.writeMessageWithDeadline(websocket.PongMessage, []byte(m))
			if err != nil {
				wsLog.Info("Failed to write pong (client may disconnect)", "err", err)
			}
		case <-pingTicker.C:
			err := wsc.writeMessageWithDeadline(websocket.PingMessage, []byte{})
			if err != nil {
				wsLog.Error("Failed to write ping", "err", err)
				err := wsc.Stop()
				if err != nil {
					wsLog.Error("Failed to Stop", "err", err)
				}
				return
			}
		case msg := <-wsc.writeChan:
			// logic
			msgBytes, err := wsc.msgSendCb(wsc, msg)
			if err != nil {
				return
			}
			if err := wsc.writeMessageWithDeadline(websocket.TextMessage, msgBytes); err != nil {
				wsc.Logger.Error("Failed to write response", "err", err)
				err := wsc.Stop()
				if err != nil {
					wsLog.Error("Failed to Stop wsc", "err", err)
				}
				return
			}
		case <-wsc.Quit():
			return
		}
	}
}

// All writes to the websocket must (re)set the write deadline.
// If some writes don't set it while others do, they may timeout incorrectly (https://github.com/tendermint/tendermint/issues/553)
func (wsc *WsConnection) writeMessageWithDeadline(msgType int, msg []byte) error {
	if err := wsc.baseConn.SetWriteDeadline(time.Now().Add(wsc.writeWait)); err != nil {
		return err
	}
	return wsc.baseConn.WriteMessage(msgType, msg)
}

func (wsc *WsConnection) Close(args ...interface{}) error {
	//check args
	if len(args) == 2 {
		if code, ok := args[0].(int); ok {
			wsc.closeCode = code
		}

		if msg, ok := args[1].(string); ok {
			wsc.closeMsg = msg
		}
	}

	if wsc.IsRunning() {
		return wsc.Stop()
	}
	return wsc.baseConn.Close()
}
