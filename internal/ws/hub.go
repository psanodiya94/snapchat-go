// Package ws implements the WebSocket layer for real-time push notifications.
//
// # Architecture
//
// The Hub is a thread-safe registry that maps each user UUID to the set of
// active WebSocket connections they have open (a user may have multiple tabs
// or devices connected simultaneously).
//
// Flow for a new message:
//  1. The REST handler in the message package calls hub.Send(recipientID, event).
//  2. The Hub looks up all Client connections registered for that user.
//  3. Each Client's send channel receives the serialised JSON event.
//  4. The writePump goroutine (in client.go) drains the channel and forwards
//     the bytes to the WebSocket connection.
//
// Clients connect via GET /ws?token=<jwt> and receive events but do not
// send anything — all mutations go through the REST API.
package ws

import (
	"encoding/json"
	"sync"

	"github.com/google/uuid"
)

// Event is the envelope pushed to connected clients over the WebSocket.
//
// Known event types:
//   - "new_message"  — a message was sent to the user; Payload is a Message JSON object.
//   - "message_read" — one of the user's sent messages was read; Payload contains
//     message_id and expires_at so the sender knows when it will be deleted.
type Event struct {
	// Type identifies the kind of event (e.g. "new_message", "message_read").
	Type string `json:"type"`
	// Payload carries the event-specific data as a raw JSON value, allowing
	// callers to embed any struct without a second marshal step.
	Payload json.RawMessage `json:"payload"`
}

// Hub is the central registry of active WebSocket clients, keyed by user UUID.
// It is safe for concurrent use by multiple goroutines.
type Hub struct {
	mu      sync.RWMutex
	clients map[uuid.UUID][]*Client
}

// NewHub creates and returns an empty Hub ready for use.
func NewHub() *Hub {
	return &Hub{clients: make(map[uuid.UUID][]*Client)}
}

// Register adds client c to the list of connections for the given user.
// Called by ServeWS when a WebSocket upgrade completes successfully.
func (h *Hub) Register(userID uuid.UUID, c *Client) {
	h.mu.Lock()
	h.clients[userID] = append(h.clients[userID], c)
	h.mu.Unlock()
}

// Unregister removes client c from the connection list for the given user.
// When the list becomes empty the map entry is deleted to avoid memory leaks.
// Called by readPump when the WebSocket connection closes.
func (h *Hub) Unregister(userID uuid.UUID, c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	list := h.clients[userID]
	for i, existing := range list {
		if existing == c {
			h.clients[userID] = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(h.clients[userID]) == 0 {
		delete(h.clients, userID)
	}
}

// Send serialises evt and writes it to every active connection of the given user.
// If a client's send buffer is full the connection is considered dead and its
// channel is closed, which causes writePump to clean up.
func (h *Hub) Send(userID uuid.UUID, evt Event) {
	h.mu.RLock()
	clients := h.clients[userID]
	h.mu.RUnlock()

	data, _ := json.Marshal(evt)
	for _, c := range clients {
		select {
		case c.send <- data:
		default:
			// Buffer full — the connection is too slow or already gone.
			close(c.send)
		}
	}
}

// Online reports whether the given user has at least one active WebSocket
// connection.  Can be used to decide whether a push notification is needed
// as a fallback (e.g. mobile push).
func (h *Hub) Online(userID uuid.UUID) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients[userID]) > 0
}
