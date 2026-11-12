package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/SherClockHolmes/webpush-go"
	"github.com/gorilla/websocket"
	_ "modernc.org/sqlite"
)

const (
	vapidPublicKey  = "BCsZp31m91k34i6wE1Tj_0j1_M7C4Zl9Q2g4j6m9n8b7v6c5x4z3a2s1d0f9g8h7j6k5l4z3x2c1v0b9n8m7"
	vapidPrivateKey = "x1y2z3a4b5c6d7e8f9g0h1i2j3k4l5m6n7o8p9q0r1s="
	vapidSubscriber = "mailto:admin@virgo.app"
)

type Envelope struct {
	ID        string          `json:"id"`
	From      string          `json:"from"`
	To        string          `json:"to"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	Timestamp int64           `json:"timestamp"`
}

type Client struct {
	ID   string
	Conn *websocket.Conn
	Send chan []byte
}

type Hub struct {
	clients    map[string]*Client
	groups     map[string]map[string]bool
	broadcast  chan []byte
	register   chan *Client
	unregister chan *Client
	mutex      sync.Mutex
	db         *sql.DB
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type GoogleTokenInfo struct {
	Iss           string `json:"iss"`
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified string `json:"email_verified"`
	Aud           string `json:"aud"`
	Error         string `json:"error_description"`
}

type UserProfile struct {
	UserID      string `json:"userId"`
	DisplayName string `json:"displayName"`
	About       string `json:"about"`
	AvatarURL   string `json:"avatarUrl"`
	UpdatedAt   int64  `json:"updatedAt"`
}

func handleGoogleAuth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Credential string `json:"credential"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Credential == "" {
		http.Error(w, "Missing Google credential token", http.StatusBadRequest)
		return
	}

	verifyURL := "https://oauth2.googleapis.com/tokeninfo?id_token=" + url.QueryEscape(req.Credential)
	resp, err := http.Get(verifyURL)
	if err != nil || resp.StatusCode != http.StatusOK {
		http.Error(w, "Failed to verify token with Google", http.StatusUnauthorized)
		return
	}
	defer resp.Body.Close()

	var tokenInfo GoogleTokenInfo
	if err := json.NewDecoder(resp.Body).Decode(&tokenInfo); err != nil {
		http.Error(w, "Malformed token response", http.StatusInternalServerError)
		return
	}

	if tokenInfo.Email == "" || (tokenInfo.EmailVerified != "true" && tokenInfo.EmailVerified != "1") {
		http.Error(w, "Unverified Google account", http.StatusUnauthorized)
		return
	}

	target := strings.ToLower(strings.TrimSpace(tokenInfo.Email))
	token := fmt.Sprintf("tok-%d", time.Now().UnixNano())

	log.Printf("✅ Google Auth Successful for: %s", target)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "authenticated",
		"userId": target,
		"token":  token,
	})
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 50<<20)
	if err := r.ParseMultipartForm(50 << 20); err != nil {
		http.Error(w, "File exceeds max upload size", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "File upload error: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	if err := os.MkdirAll("./uploads", 0755); err != nil {
		http.Error(w, "Failed to create upload directory", http.StatusInternalServerError)
		return
	}

	ext := filepath.Ext(header.Filename)
	if ext == "" {
		contentType := header.Header.Get("Content-Type")
		switch {
		case strings.Contains(contentType, "mp4"):
			ext = ".mp4"
		case strings.Contains(contentType, "webm"):
			ext = ".webm"
		case strings.Contains(contentType, "png"):
			ext = ".png"
		case strings.Contains(contentType, "jpeg"), strings.Contains(contentType, "jpg"):
			ext = ".jpg"
		default:
			ext = ".bin"
		}
	}

	savedName := fmt.Sprintf("%d%s", time.Now().UnixNano(), ext)
	savedPath := filepath.Join("./uploads", savedName)

	dst, err := os.Create(savedPath)
	if err != nil {
		http.Error(w, "File save error", http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	if _, err := io.Copy(dst, file); err != nil {
		http.Error(w, "Error saving file content", http.StatusInternalServerError)
		return
	}

	log.Printf("📦 File saved: %s (size: %d bytes)", savedName, header.Size)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"url": "/uploads/" + savedName,
	})
}

func handleProfile(db *sql.DB, hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			return
		}

		if r.Method == http.MethodGet {
			target := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("userId")))
			if target == "" {
				http.Error(w, "userId required", http.StatusBadRequest)
				return
			}

			var p UserProfile
			p.UserID = target
			query := `SELECT display_name, about, avatar_url, updated_at FROM profiles WHERE user_id = ?;`
			err := db.QueryRow(query, target).Scan(&p.DisplayName, &p.About, &p.AvatarURL, &p.UpdatedAt)
			if err != nil {
				p.DisplayName = strings.Split(target, "@")[0]
				p.About = "Hey there! I am using Virgo."
				p.AvatarURL = ""
				p.UpdatedAt = time.Now().UnixMilli()
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(p)
			return
		}

		if r.Method == http.MethodPost {
			var p UserProfile
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			p.UserID = strings.ToLower(strings.TrimSpace(p.UserID))
			if p.UserID == "" {
				http.Error(w, "userId required", http.StatusBadRequest)
				return
			}
			p.UpdatedAt = time.Now().UnixMilli()

			query := `INSERT INTO profiles (user_id, display_name, about, avatar_url, updated_at) 
			          VALUES (?, ?, ?, ?, ?)
			          ON CONFLICT(user_id) DO UPDATE SET 
			          display_name = excluded.display_name,
			          about = excluded.about,
			          avatar_url = excluded.avatar_url,
			          updated_at = excluded.updated_at;`
			_, err := db.Exec(query, p.UserID, p.DisplayName, p.About, p.AvatarURL, p.UpdatedAt)
			if err != nil {
				log.Printf("Profile update failed: %v", err)
				http.Error(w, "Failed to update profile", http.StatusInternalServerError)
				return
			}

			profilePayload, _ := json.Marshal(p)
			hub.broadcast <- fmt.Appendf(nil, `{"type":"profile_update","from":"%s","to":"all","payload":%s}`, p.UserID, string(profilePayload))

			log.Printf("👤 Profile updated for %s", p.UserID)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(p)
			return
		}

		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func newHub(db *sql.DB) *Hub {
	return &Hub{
		clients:    make(map[string]*Client),
		groups:     make(map[string]map[string]bool),
		broadcast:  make(chan []byte, 1024),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		db:         db,
	}
}

func (h *Hub) run() {
	for {
		select {
		case client := <-h.register:
			h.mutex.Lock()
			if oldClient, exists := h.clients[client.ID]; exists {
				close(oldClient.Send)
				oldClient.Conn.Close()
				delete(h.clients, client.ID)
			}
			h.clients[client.ID] = client

			onlineEnv, _ := json.Marshal(Envelope{
				ID:        fmt.Sprintf("pres-%d", time.Now().UnixMilli()),
				From:      client.ID,
				Type:      "presence",
				Payload:   json.RawMessage(`{"status":"online"}`),
				Timestamp: time.Now().UnixMilli(),
			})

			for id, peer := range h.clients {
				if id != client.ID {
					select {
					case peer.Send <- onlineEnv:
					default:
					}
				}
			}
			h.mutex.Unlock()

			log.Printf("🔌 Client registered: %s", client.ID)
			go h.deliverPendingMessages(client)

		case client := <-h.unregister:
			h.mutex.Lock()
			if _, ok := h.clients[client.ID]; ok {
				delete(h.clients, client.ID)
				close(client.Send)
			}
			for _, members := range h.groups {
				delete(members, client.ID)
			}

			offlineEnv, _ := json.Marshal(Envelope{
				ID:        fmt.Sprintf("pres-%d", time.Now().UnixMilli()),
				From:      client.ID,
				Type:      "presence",
				Payload:   json.RawMessage(`{"status":"offline"}`),
				Timestamp: time.Now().UnixMilli(),
			})

			for _, peer := range h.clients {
				select {
				case peer.Send <- offlineEnv:
				default:
				}
			}
			h.mutex.Unlock()

			log.Printf("🔌 Client disconnected: %s", client.ID)

		case message := <-h.broadcast:
			var env Envelope
			if err := json.Unmarshal(message, &env); err != nil {
				continue
			}

			sender := strings.ToLower(strings.TrimSpace(env.From))
			target := strings.ToLower(strings.TrimSpace(env.To))

			h.mutex.Lock()
			if target == "all" {
				for memberID, peer := range h.clients {
					if memberID != sender {
						select {
						case peer.Send <- message:
						default:
						}
					}
				}
			} else if members, isGroup := h.groups[target]; isGroup {
				for memberID := range members {
					if memberID != sender {
						if peer, active := h.clients[memberID]; active {
							select {
							case peer.Send <- message:
							default:
							}
						} else {
							h.storePendingMessage(env.ID, sender, memberID, message)
						}
					}
				}
			} else {
				if recipient, exists := h.clients[target]; exists {
					select {
					case recipient.Send <- message:
						log.Printf("📩 Delivered [%s] from %s to %s", env.Type, sender, target)
					default:
						h.storePendingMessage(env.ID, sender, target, message)
					}
				} else {
					log.Printf("⚠️ Recipient '%s' offline. Storing pending message from %s", target, sender)
					h.storePendingMessage(env.ID, sender, target, message)
				}
			}
			h.mutex.Unlock()
		}
	}
}

func (h *Hub) storePendingMessage(msgID, from, to string, raw []byte) {
	if msgID == "" {
		msgID = fmt.Sprintf("pending-%d", time.Now().UnixNano())
	}
	query := `INSERT OR REPLACE INTO pending_messages (id, sender_id, recipient_id, payload, timestamp) VALUES (?, ?, ?, ?, ?);`
	_, err := h.db.Exec(query, msgID, from, to, string(raw), time.Now().UnixMilli())
	if err != nil {
		log.Printf("Error storing pending message: %v", err)
	}

	go func(targetUser, fromUser string) {
		var subJSON string
		err := h.db.QueryRow(`SELECT subscription FROM push_subscriptions WHERE user_id = ?;`, targetUser).Scan(&subJSON)
		if err != nil {
			return
		}

		s := &webpush.Subscription{}
		if err := json.Unmarshal([]byte(subJSON), s); err != nil {
			return
		}

		notificationPayload := fmt.Sprintf(`{"title":"Virgo","body":"New message from %s"}`, fromUser)
		res, pushErr := webpush.SendNotification([]byte(notificationPayload), s, &webpush.Options{
			Subscriber:      vapidSubscriber,
			VAPIDPublicKey:  vapidPublicKey,
			VAPIDPrivateKey: vapidPrivateKey,
			TTL:             60,
		})
		if pushErr != nil {
			log.Printf("Push notification error: %v", pushErr)
		} else if res != nil {
			res.Body.Close()
			log.Printf("🚀 Background push dispatched to %s", targetUser)
		}
	}(to, from)
}

func (h *Hub) deliverPendingMessages(c *Client) {
	time.Sleep(500 * time.Millisecond)

	query := `SELECT id, payload FROM pending_messages WHERE recipient_id = ? ORDER BY timestamp ASC;`
	rows, err := h.db.Query(query, c.ID)
	if err != nil {
		log.Printf("Query error fetching offline messages: %v", err)
		return
	}
	defer rows.Close()

	var deliveredIDs []string
	for rows.Next() {
		var id, payload string
		if err := rows.Scan(&id, &payload); err == nil {
			c.Send <- []byte(payload)
			deliveredIDs = append(deliveredIDs, id)
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("Row iteration error: %v", err)
	}

	for _, id := range deliveredIDs {
		h.db.Exec(`DELETE FROM pending_messages WHERE id = ?;`, id)
	}
	if len(deliveredIDs) > 0 {
		log.Printf("📦 Delivered %d offline messages to %s", len(deliveredIDs), c.ID)
	}
}

func (c *Client) readPump(hub *Hub) {
	defer func() {
		hub.unregister <- c
		c.Conn.Close()
	}()

	for {
		_, message, err := c.Conn.ReadMessage()
		if err != nil {
			break
		}

		var env Envelope
		if err := json.Unmarshal(message, &env); err != nil {
			continue
		}

		env.From = c.ID
		target := strings.ToLower(strings.TrimSpace(env.To))
		env.To = target

		if env.Type == "group_join" {
			hub.mutex.Lock()
			if hub.groups[env.To] == nil {
				hub.groups[env.To] = make(map[string]bool)
			}
			hub.groups[env.To][c.ID] = true
			hub.mutex.Unlock()
			continue
		}

		if env.Type == "query_presence" {
			hub.mutex.Lock()
			_, isOnline := hub.clients[env.To]
			hub.mutex.Unlock()

			status := "offline"
			if isOnline {
				status = "online"
			}

			respEnv, _ := json.Marshal(Envelope{
				ID:        fmt.Sprintf("pres-resp-%d", time.Now().UnixMilli()),
				From:      env.To,
				To:        c.ID,
				Type:      "presence",
				Payload:   json.RawMessage(fmt.Sprintf(`{"status":"%s"}`, status)),
				Timestamp: time.Now().UnixMilli(),
			})
			c.Send <- respEnv
			continue
		}

		if env.Type == "chat" || env.Type == "media" {
			ackEnv, _ := json.Marshal(Envelope{
				ID:        env.ID,
				From:      "server",
				To:        c.ID,
				Type:      "ack",
				Payload:   json.RawMessage(`{"status":"received"}`),
				Timestamp: time.Now().UnixMilli(),
			})
			c.Send <- ackEnv
		}

		if normalizedMsg, err := json.Marshal(env); err == nil {
			hub.broadcast <- normalizedMsg
		} else {
			hub.broadcast <- message
		}
	}
}

func (c *Client) writePump() {
	defer c.Conn.Close()
	for message := range c.Send {
		if err := c.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
			break
		}
	}
}

func initDatabase() (*sql.DB, error) {
	db, err := sql.Open("sqlite", "./virgo.db")
	if err != nil {
		return nil, err
	}

	schema := `
	CREATE TABLE IF NOT EXISTS pending_messages (
		id TEXT PRIMARY KEY,
		sender_id TEXT,
		recipient_id TEXT,
		payload TEXT,
		timestamp INTEGER
	);
	CREATE TABLE IF NOT EXISTS push_subscriptions (
		user_id TEXT PRIMARY KEY,
		subscription TEXT
	);
	CREATE TABLE IF NOT EXISTS profiles (
		user_id TEXT PRIMARY KEY,
		display_name TEXT,
		about TEXT,
		avatar_url TEXT,
		updated_at INTEGER
	);`
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	return db, nil
}

func main() {
	db, err := initDatabase()
	if err != nil {
		log.Fatalf("Database initialization failed: %v", err)
	}
	defer db.Close()

	hub := newHub(db)
	go hub.run()

	http.HandleFunc("/auth/google", handleGoogleAuth)
	http.HandleFunc("/upload", handleUpload)
	http.HandleFunc("/api/profile", handleProfile(db, hub))

	http.HandleFunc("/api/push/subscribe", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, ngrok-skip-browser-warning")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			UserID       string          `json:"userId"`
			Subscription json.RawMessage `json:"subscription"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		target := strings.ToLower(strings.TrimSpace(req.UserID))
		_, err := db.Exec(`INSERT OR REPLACE INTO push_subscriptions (user_id, subscription) VALUES (?, ?);`, target, string(req.Subscription))
		if err != nil {
			log.Printf("Failed to save push subscription: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		log.Printf("📲 Saved push subscription for %s", target)
		w.WriteHeader(http.StatusOK)
	})

	uploadsHandler := http.StripPrefix("/uploads/", http.FileServer(http.Dir("./uploads")))
	http.HandleFunc("/uploads/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		uploadsHandler.ServeHTTP(w, r)
	})

	fileServer := http.FileServer(http.Dir("."))
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.URL.Path == "/" || r.URL.Path == "/index.html" || r.URL.Path == "/sw.js" {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			w.Header().Set("Pragma", "no-cache")
			w.Header().Set("Expires", "0")
			if r.URL.Path == "/sw.js" {
				http.ServeFile(w, r, "sw.js")
				return
			}
			http.ServeFile(w, r, "index.html")
			return
		}
		fileServer.ServeHTTP(w, r)
	})

	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		userId := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("userId")))
		if userId == "" {
			userId = strings.ToLower(strings.TrimSpace(r.URL.Query().Get("user")))
		}
		if userId == "" {
			log.Println("❌ WebSocket rejected: missing userId/user query param")
			http.Error(w, "userId required", http.StatusBadRequest)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("Upgrade error: %v", err)
			return
		}

		client := &Client{ID: userId, Conn: conn, Send: make(chan []byte, 256)}
		hub.register <- client

		go client.writePump()
		go client.readPump(hub)
	})

	// Automatic 24/7 Keep-Alive for Render and Cloud Hosting
	extURL := os.Getenv("RENDER_EXTERNAL_URL")
	if extURL == "" {
		extURL = os.Getenv("KEEP_ALIVE_URL")
	}
	if extURL != "" {
		log.Printf("24/7 Cloud keep-alive active for: %s", extURL)
		go func() {
			ticker := time.NewTicker(10 * time.Minute)
			for range ticker.C {
				resp, err := http.Get(extURL + "/")
				if err == nil {
					resp.Body.Close()
					log.Printf("Keep-alive ping sent to %s", extURL)
				}
			}
		}()
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("Virgo Gateway server listening on port :%s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
