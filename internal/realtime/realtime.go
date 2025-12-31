package realtime

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// Time allowed to write a message to the peer.
	writeWait = 10 * time.Second

	// Time allowed to read the next pong message from the peer.
	pongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = (pongWait * 9) / 10

	// Maximum message size allowed from peer.
	maxMessageSize = 512
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type IncomingMessage struct {
	Topic   string          `json:"topic"`
	Event   string          `json:"event"`
	Payload json.RawMessage `json:"payload"`
	Ref     string          `json:"ref"`
}

type OutgoingMessage struct {
	Topic   string      `json:"topic"`
	Event   string      `json:"event"`
	Payload interface{} `json:"payload"`
	Ref     string      `json:"ref,omitempty"`
}

type Client struct {
	hub    *Hub
	conn   *websocket.Conn
	send   chan []byte
	topics map[string]bool
}

type Hub struct {
	clients    map[*Client]bool
	broadcast  chan *BroadcastMessage
	register   chan *Client
	unregister chan *Client
	topics     map[string]map[*Client]bool
	mu         sync.RWMutex
}

type BroadcastMessage struct {
	Topic string
	Msg   *OutgoingMessage
	Ref   *int
}

func NewHub() *Hub {
	return &Hub{
		broadcast:  make(chan *BroadcastMessage),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		clients:    make(map[*Client]bool),
		topics:     make(map[string]map[*Client]bool),
	}
}

func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
				for topic := range client.topics {
					if clients, ok := h.topics[topic]; ok {
						delete(clients, client)
						if len(clients) == 0 {
							delete(h.topics, topic)
						}
					}
				}
			}
			h.mu.Unlock()
		case message := <-h.broadcast:
			h.mu.RLock()
			if clients, ok := h.topics[message.Topic]; ok {
				data, err := json.Marshal(message.Msg)
				if err == nil {
					for client := range clients {
						select {
						case client.send <- data:
						default:
							close(client.send)
							delete(h.clients, client)
						}
					}
				}
			}
			h.mu.RUnlock()
		}
	}
}

func (h *Hub) Broadcast(topic string, event string, payload interface{}) {
	msg := &OutgoingMessage{
		Topic:   topic,
		Event:   event,
		Payload: payload,
	}
	h.broadcast <- &BroadcastMessage{
		Topic: topic,
		Msg:   msg,
		Ref:   nil,
	}
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()
	//c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error { c.conn.SetReadDeadline(time.Now().Add(pongWait)); return nil })
	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("error: %v", err)
			}
			break
		}

		var msg IncomingMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			log.Printf("error unmarshalling message: %v", err)
			continue
		}

		c.handleMessage(msg)
	}
}

func (c *Client) handleMessage(msg IncomingMessage) {
	switch msg.Event {
	case "phx_join":
		c.hub.mu.Lock()
		if c.hub.topics[msg.Topic] == nil {
			c.hub.topics[msg.Topic] = make(map[*Client]bool)
		}
		c.hub.topics[msg.Topic][c] = true
		c.topics[msg.Topic] = true
		c.hub.mu.Unlock()

		sessionID := msg.Topic[len("realtime:messages:"):]
		reply := OutgoingMessage{
			Topic: msg.Topic,
			Event: "phx_reply",
			Ref:   msg.Ref,
			Payload: map[string]interface{}{
				"status": "ok",
				"response": map[string]any{
					"event":  "INSERT",
					"filter": fmt.Sprintf("session_id=eq.%s", sessionID),
					"schema": "public",
					"table":  "messages",
				},
			},
		}
		c.sendJSON(reply)

		channel := msg.Topic[len("realtime:"):]
		reply2 := OutgoingMessage{
			Topic: msg.Topic,
			Event: "system",
			Payload: map[string]interface{}{
				"channel":   channel,
				"message":   "Subscribed to PostgreSQ",
				"extension": "postgres_changes",
				"status":    "ok",
			},
		}
		c.sendJSON(reply2)
	case "heartbeat":
		reply := OutgoingMessage{
			Topic: "phoenix",
			Event: "phx_reply",
			Ref:   msg.Ref,
			Payload: map[string]interface{}{
				"status":   "ok",
				"response": map[string]string{},
			},
		}
		c.sendJSON(reply)

	case "phx_leave":
		c.hub.mu.Lock()
		if clients, ok := c.hub.topics[msg.Topic]; ok {
			delete(clients, c)
			if len(clients) == 0 {
				delete(c.hub.topics, msg.Topic)
			}
		}
		delete(c.topics, msg.Topic)
		c.hub.mu.Unlock()

		reply := OutgoingMessage{
			Topic: msg.Topic,
			Event: "phx_reply",
			Ref:   msg.Ref,
			Payload: map[string]interface{}{
				"status":   "ok",
				"response": map[string]string{},
			},
		}
		c.sendJSON(reply)
	}
}

func (c *Client) sendJSON(v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	c.send <- data
}

func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// The hub closed the channel.
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			// w, err := c.conn.NextWriter(websocket.TextMessage)
			// if err != nil {
			// 	return
			// }
			c.conn.WriteMessage(websocket.TextMessage, message)

			// Add queued chat messages to the current websocket message.
			n := len(c.send)
			for i := 0; i < n; i++ {
				msg := <-c.send
				c.conn.WriteMessage(websocket.TextMessage, msg)
			}

			// if err := w.Close(); err != nil {
			// 	return
			//}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func ServeWs(hub *Hub, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}
	client := &Client{hub: hub, conn: conn, send: make(chan []byte, 256), topics: make(map[string]bool)}
	client.hub.register <- client

	// Allow collection of memory referenced by the caller by doing all work in
	// new goroutines.
	go client.writePump()
	go client.readPump()
}
