package main

import (
	"encoding/json"
	"fmt"
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
	fmt.Println("Testing Live Presence Sync on Render:", wsBase)

	// 1. Connect Client A (e.g. Browser)
	connA, _, err := websocket.DefaultDialer.Dial(wsBase+"?userId=test_user_a", nil)
	if err != nil {
		fmt.Printf("FAIL: connA: %v\n", err)
		os.Exit(1)
	}
	defer connA.Close()
	fmt.Println("✅ Client A connected")

	var mu sync.Mutex
	var gotOnlineNotice, gotInitOnlineNotice, gotQueryOnlineResponse bool

	// Read loop for Client A
	go func() {
		for {
			_, msg, err := connA.ReadMessage()
			if err != nil {
				break
			}
			var env Envelope
			json.Unmarshal(msg, &env)
			fmt.Printf("📱 A received: [%s] from: %s\n", env.Type, env.From)
			if env.Type == "presence" && env.From == "7861941270" {
				var pl struct {
					Status string `json:"status"`
				}
				json.Unmarshal(env.Payload, &pl)
				if pl.Status == "online" {
					mu.Lock()
					gotOnlineNotice = true
					mu.Unlock()
				}
			}
			if env.Type == "presence" && env.ID != "" && len(env.ID) > 9 && env.ID[:9] == "pres-resp" {
				mu.Lock()
				gotQueryOnlineResponse = true
				mu.Unlock()
			}
		}
	}()

	time.Sleep(1 * time.Second)

	// 2. Connect Client B (e.g. Phone 7861941270)
	connB, _, err := websocket.DefaultDialer.Dial(wsBase+"?userId=7861941270", nil)
	if err != nil {
		fmt.Printf("FAIL: connB: %v\n", err)
		os.Exit(1)
	}
	defer connB.Close()
	fmt.Println("✅ Client B connected as 7861941270")

	time.Sleep(2 * time.Second)

	// 3. Client A queries presence for 7861941270
	qMsg, _ := json.Marshal(Envelope{
		From:      "test_user_a",
		To:        "7861941270",
		Type:      "query_presence",
		Timestamp: time.Now().UnixMilli(),
	})
	connA.WriteMessage(websocket.TextMessage, qMsg)

	time.Sleep(2 * time.Second)

	// 4. Now connect Client C (New user joining while B is ALREADY online)
	connC, _, err := websocket.DefaultDialer.Dial(wsBase+"?userId=test_user_c", nil)
	if err != nil {
		fmt.Printf("FAIL: connC: %v\n", err)
		os.Exit(1)
	}
	defer connC.Close()
	fmt.Println("✅ Client C connected (joined after B)")

	// Read loop for Client C to catch the initial presence packet
	go func() {
		for {
			_, msg, err := connC.ReadMessage()
			if err != nil {
				break
			}
			var env Envelope
			json.Unmarshal(msg, &env)
			fmt.Printf("💻 C received: [%s] from: %s, id: %s\n", env.Type, env.From, env.ID)
			if env.Type == "presence" && env.From == "7861941270" {
				mu.Lock()
				gotInitOnlineNotice = true
				mu.Unlock()
			}
		}
	}()

	time.Sleep(2 * time.Second)

	mu.Lock()
	defer mu.Unlock()

	fmt.Println("\n================ PRESENCE VERIFICATION REPORT ================")
	fmt.Printf("1. Real-time online broadcast to connected peers:  %v\n", gotOnlineNotice)
	fmt.Printf("2. query_presence response (online):               %v\n", gotQueryOnlineResponse)
	fmt.Printf("3. New connecting client gets active peer list:    %v\n", gotInitOnlineNotice)

	if gotOnlineNotice && gotQueryOnlineResponse && gotInitOnlineNotice {
		fmt.Println("🎉 ALL PRESENCE TESTS PASSED! Contacts will instantly show 'Online'!")
	} else {
		fmt.Println("⚠️ Some checks did not pass.")
	}
}
