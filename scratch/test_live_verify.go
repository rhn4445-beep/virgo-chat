package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type Envelope struct {
	ID        string          `json:"id"`
	From      string          `json:"from"`
	To        string          `json:"to"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	Timestamp int64           `json:"timestamp"`
}

func main() {
	wsBase := "wss://virgo-chat.onrender.com/ws"
	fmt.Println("Connecting to Render Live WebSocket:", wsBase)

	// 1. Connect App registered as 10-digit number
	appConn, respApp, err := websocket.DefaultDialer.Dial(wsBase+"?userId=9537272106", nil)
	if err != nil {
		if respApp != nil {
			body, _ := io.ReadAll(respApp.Body)
			fmt.Printf("FAIL: Cannot connect App: %v (Status: %d, Body: %s)\n", err, respApp.StatusCode, string(body))
		} else {
			fmt.Printf("FAIL: Cannot connect App: %v\n", err)
		}
		os.Exit(1)
	}
	defer appConn.Close()
	fmt.Println("✅ App connected as 9537272106")

	// 2. Connect Browser registered as rohan_browser
	browserConn, resp, err := websocket.DefaultDialer.Dial(wsBase+"?userId=rohan_browser", nil)
	if err != nil {
		if resp != nil {
			body, _ := io.ReadAll(resp.Body)
			fmt.Printf("FAIL: Cannot connect Browser: %v (Status: %d, Body: %s)\n", err, resp.StatusCode, string(body))
		} else {
			fmt.Printf("FAIL: Cannot connect Browser: %v\n", err)
		}
		os.Exit(1)
	}
	defer browserConn.Close()
	fmt.Println("✅ Browser connected as rohan_browser")

	var wg sync.WaitGroup
	var appGotChat, browserGotAck, browserGotReply, heartbeatOk bool
	var mu sync.Mutex

	// Read loop for App
	go func() {
		for {
			_, msg, err := appConn.ReadMessage()
			if err != nil {
				break
			}
			var env Envelope
			json.Unmarshal(msg, &env)
			fmt.Printf("📱 APP received: [%s] from: %s, to: %s, id: %s\n", env.Type, env.From, env.To, env.ID)
			if env.Type == "chat" && env.ID == "b2a-101" {
				mu.Lock()
				appGotChat = true
				mu.Unlock()
			}
			if env.Type == "heartbeat_ack" {
				mu.Lock()
				heartbeatOk = true
				mu.Unlock()
			}
		}
	}()

	// Read loop for Browser
	go func() {
		for {
			_, msg, err := browserConn.ReadMessage()
			if err != nil {
				break
			}
			var env Envelope
			json.Unmarshal(msg, &env)
			fmt.Printf("💻 BROWSER received: [%s] from: %s, to: %s, id: %s\n", env.Type, env.From, env.To, env.ID)
			if env.Type == "ack" && env.ID == "b2a-101" {
				mu.Lock()
				browserGotAck = true
				mu.Unlock()
			}
			if env.Type == "chat" && env.ID == "a2b-202" {
				mu.Lock()
				browserGotReply = true
				mu.Unlock()
			}
		}
	}()

	time.Sleep(1 * time.Second)

	// Send Heartbeat from App
	fmt.Println("\n--- Sending Heartbeat from App ---")
	hb, _ := json.Marshal(Envelope{
		From: "9537272106",
		Type: "heartbeat",
	})
	appConn.WriteMessage(websocket.TextMessage, hb)

	time.Sleep(1 * time.Second)

	// Browser sends message to +919537272106
	fmt.Println("\n--- Browser sends chat to +919537272106 ---")
	chatMsg, _ := json.Marshal(Envelope{
		ID:        "b2a-101",
		From:      "rohan_browser",
		To:        "+919537272106",
		Type:      "chat",
		Payload:   json.RawMessage(`{"text":"Hello from browser to Indian phone format!"}`),
		Timestamp: time.Now().UnixMilli(),
	})
	browserConn.WriteMessage(websocket.TextMessage, chatMsg)

	time.Sleep(2 * time.Second)

	// App sends reply to rohan_browser
	fmt.Println("\n--- App sends reply to rohan_browser ---")
	replyMsg, _ := json.Marshal(Envelope{
		ID:        "a2b-202",
		From:      "9537272106",
		To:        "rohan_browser",
		Type:      "chat",
		Payload:   json.RawMessage(`{"text":"Hello Browser, I received your message on my App!"}`),
		Timestamp: time.Now().UnixMilli(),
	})
	appConn.WriteMessage(websocket.TextMessage, replyMsg)

	time.Sleep(2 * time.Second)

	mu.Lock()
	defer mu.Unlock()
	_ = wg

	fmt.Println("\n================ FINAL VERIFICATION REPORT ================")
	fmt.Printf("1. Heartbeat Ping/Pong acknowledged:       %v\n", heartbeatOk)
	fmt.Printf("2. Browser received Server Ack:            %v\n", browserGotAck)
	fmt.Printf("3. App received Browser chat (+91 match):  %v\n", appGotChat)
	fmt.Printf("4. Browser received App reply:             %v\n", browserGotReply)
	if heartbeatOk && browserGotAck && appGotChat && browserGotReply {
		fmt.Println("🎉 ALL TESTS PASSED! Bi-directional messaging is 100% operational!")
	} else {
		fmt.Println("⚠️ Some checks did not pass.")
	}
}
