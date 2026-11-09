package main

import (
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type Hub struct {
	clients   map[*websocket.Conn]bool
	broadcast chan string
	mu        sync.Mutex
}

func NewHub() *Hub {
	return &Hub{
		clients:   make(map[*websocket.Conn]bool),
		broadcast: make(chan string),
	}
}

func (h *Hub) Run() {
	for {
		msg := <-h.broadcast
		h.mu.Lock()
		for client := range h.clients {
			err := client.WriteMessage(websocket.TextMessage, []byte(msg))
			if err != nil {
				client.Close()
				delete(h.clients, client)
			}
		}
		h.mu.Unlock()
	}
}

func (h *Hub) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("WebSocket Upgrade Error:", err)
		return
	}

	h.mu.Lock()
	h.clients[conn] = true
	h.mu.Unlock()

	log.Println("New Kitchen Display connected.")
}

func (h *Hub) Broadcast(msg string) {
	h.broadcast <- msg
}
