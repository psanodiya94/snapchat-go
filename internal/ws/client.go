package ws

import (
	"net/http"
	"time"

	"snapchat-go/internal/auth"

	"github.com/gorilla/websocket"
)

// Timing constants for the WebSocket keep-alive mechanism.
// pingPeriod is slightly less than pongWait so a pong always arrives before
// the read deadline fires and closes the connection.
const (
	writeWait  = 10 * time.Second // maximum time to write a message to the peer
	pongWait   = 60 * time.Second // time allowed to read the next pong from the peer
	pingPeriod = (pongWait * 9) / 10 // how often to send a ping; must be < pongWait
	maxMsgSize = 512                  // maximum bytes the server reads from a client frame
)

// upgrader upgrades an HTTP connection to a WebSocket connection.
// CheckOrigin always returns true; add origin validation here for production.
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// Client represents one WebSocket connection for an authenticated user.
// The send channel receives serialised JSON events from the Hub and the
// writePump goroutine forwards them to the underlying connection.
type Client struct {
	// send is a buffered channel of outbound messages.
	// The buffer size of 256 accommodates burst traffic without blocking.
	send chan []byte
}

// ServeWS upgrades an HTTP request to a WebSocket connection and registers
// the resulting Client with the Hub.  The JWT token must be provided either
// as the "Authorization: Bearer <token>" header or as the "token" query
// parameter (required for browser WebSocket APIs).
//
// GET /ws?token=<jwt>
//
// After the upgrade, ServeWS starts writePump in a goroutine and runs
// readPump on the current goroutine; it blocks until the connection closes.
func ServeWS(hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := auth.UserID(r)

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}

		c := &Client{send: make(chan []byte, 256)}
		hub.Register(uid, c)

		// writePump runs in its own goroutine and owns the write side of conn.
		go writePump(conn, c)
		// readPump blocks until the connection closes, then cleans up.
		readPump(conn, c, hub, uid)
	}
}

// readPump reads and discards all frames arriving from the client.
// Its main job is to keep the read deadline alive via the pong handler and
// to detect disconnects so that writePump can be notified through the
// connection closure.
func readPump(conn *websocket.Conn, c *Client, hub *Hub, uid interface{ String() string }) {
	defer func() {
		conn.Close()
	}()
	conn.SetReadLimit(maxMsgSize)
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		// Reset the read deadline each time a pong arrives.
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	// Clients are receive-only; all writes go through the REST API.
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
}

// writePump drains the Client's send channel and writes each message to the
// WebSocket connection.  It also sends periodic WebSocket pings to detect
// stale connections.  writePump must be the sole writer for a given conn
// because gorilla/websocket connections do not support concurrent writes.
func writePump(conn *websocket.Conn, c *Client) {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		conn.Close()
	}()
	for {
		select {
		case msg, ok := <-c.send:
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// Hub closed the channel — send a close frame and exit.
				conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
