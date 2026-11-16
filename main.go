package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/smtp"
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
	vapidPublicKey  = "BArzAnrnhXIdRwPq7Ds0gEX4-AQMT9-T9LSjgzS0lq0c4zLU8bMwtJXE4_OqZz_nhZw5u8DAhz64ynIHfyrklIA"
	vapidPrivateKey = "o-dWfKd6ta3PwliutqHn0RaPNFOGClxIrnOyiDGlvFw"
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

var serverBuildTime = "v2.8.4"

func normalizeUserID(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}
	if strings.Contains(s, "@") {
		return s
	}
	var digits strings.Builder
	hasLetter := false
	for _, ch := range s {
		if ch >= '0' && ch <= '9' {
			digits.WriteRune(ch)
		} else if (ch >= 'a' && ch <= 'z') || ch == '_' {
			hasLetter = true
		}
	}
	if !hasLetter && digits.Len() >= 10 {
		d := digits.String()
		if len(d) == 12 && strings.HasPrefix(d, "91") {
			return d[2:]
		}
		if len(d) == 11 && strings.HasPrefix(d, "0") {
			return d[1:]
		}
		if len(d) == 10 {
			return d
		}
		return d
	}
	return s
}

func (h *Hub) findClient(target string) *Client {
	if target == "" {
		return nil
	}
	norm := normalizeUserID(target)
	if client, ok := h.clients[norm]; ok {
		return client
	}
	if client, ok := h.clients[target]; ok {
		return client
	}
	for id, client := range h.clients {
		if normalizeUserID(id) == norm {
			return client
		}
	}
	return nil
}

type Hub struct {
	clients     map[string]*Client
	groups      map[string]map[string]bool
	broadcast   chan []byte
	register    chan *Client
	unregister  chan *Client
	pollWaiters map[string]map[chan []byte]bool
	mutex       sync.Mutex
	db          *sql.DB
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

func handleGoogleAuth(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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

		now := time.Now().UnixMilli()
		_, err = db.Exec(`INSERT INTO user_tokens (user_id, token, auth_provider, created_at) 
		                  VALUES (?, ?, 'google', ?)
		                  ON CONFLICT(user_id) DO UPDATE SET token = excluded.token, created_at = excluded.created_at;`, target, token, now)
		if err != nil {
			log.Printf("⚠️ Failed to store user token: %v", err)
		}

		log.Printf("✅ Google Auth Successful for: %s (token secured)", target)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status": "authenticated",
			"userId": target,
			"token":  token,
		})
	}
}

func hashPassword(salt, password string) string {
	h := sha256.New()
	h.Write([]byte(salt + ":" + password))
	return hex.EncodeToString(h.Sum(nil))
}

func generateSecureToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func generateSalt() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func handleAuthRegisterOrLogin(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			UserID string `json:"userId"`
			Pin    string `json:"pin"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
			return
		}

		userID := normalizeUserID(req.UserID)
		pin := strings.TrimSpace(req.Pin)

		if userID == "" || len(userID) < 3 {
			http.Error(w, "Valid User ID/Email/Phone is required (min 3 characters)", http.StatusBadRequest)
			return
		}
		if len(pin) < 4 {
			http.Error(w, "Security PIN/Password must be at least 4 characters", http.StatusBadRequest)
			return
		}

		now := time.Now().UnixMilli()

		var storedSalt, storedHash string
		err := db.QueryRow(`SELECT salt, password_hash FROM user_credentials WHERE user_id = ?;`, userID).Scan(&storedSalt, &storedHash)
		if err == sql.ErrNoRows {
			// Register new user with salt & SHA256 hash
			salt := generateSalt()
			hash := hashPassword(salt, pin)
			_, err = db.Exec(`INSERT INTO user_credentials (user_id, salt, password_hash, created_at, last_login)
			                  VALUES (?, ?, ?, ?, ?);`, userID, salt, hash, now, now)
			if err != nil {
				log.Printf("Registration DB error: %v", err)
				http.Error(w, "Database error during registration", http.StatusInternalServerError)
				return
			}

			token := generateSecureToken()
			db.Exec(`INSERT INTO user_tokens (user_id, token, auth_provider, created_at)
			         VALUES (?, ?, 'pin', ?)
			         ON CONFLICT(user_id) DO UPDATE SET token = excluded.token, created_at = excluded.created_at;`, userID, token, now)

			log.Printf("🔐 User registered & account protected: %s", userID)
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":  "authenticated",
				"action":  "registered",
				"userId":  userID,
				"token":   token,
				"message": "Account created and secured with your PIN.",
			})
			return
		} else if err != nil {
			log.Printf("Query credentials DB error: %v", err)
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}

		// User exists - verify PIN with constant-time comparison
		candidateHash := hashPassword(storedSalt, pin)
		if subtle.ConstantTimeCompare([]byte(candidateHash), []byte(storedHash)) != 1 {
			log.Printf("❌ Authentication failed for %s: Invalid PIN/Password", userID)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":  "error",
				"message": "Incorrect PIN/Password for this account. Please enter your correct PIN.",
			})
			return
		}

		// PIN verified! Issue fresh secure session token
		token := generateSecureToken()
		db.Exec(`UPDATE user_credentials SET last_login = ? WHERE user_id = ?;`, now, userID)
		db.Exec(`INSERT INTO user_tokens (user_id, token, auth_provider, created_at)
		         VALUES (?, ?, 'pin', ?)
		         ON CONFLICT(user_id) DO UPDATE SET token = excluded.token, created_at = excluded.created_at;`, userID, token, now)

		log.Printf("✅ User authenticated with verified PIN: %s", userID)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "authenticated",
			"action":  "logged_in",
			"userId":  userID,
			"token":   token,
			"message": "Welcome back! Authentication verified.",
		})
	}
}

func handleAuthCheckUser(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		userID := normalizeUserID(r.URL.Query().Get("userId"))
		if userID == "" {
			http.Error(w, "userId required", http.StatusBadRequest)
			return
		}

		var count int
		db.QueryRow(`SELECT COUNT(1) FROM user_credentials WHERE user_id = ?;`, userID).Scan(&count)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"userId": userID,
			"exists": count > 0,
		})
	}
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func generateNumericOTP() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	num := (uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3]))%900000 + 100000
	return fmt.Sprintf("%06d", num), nil
}

func getBrevoKey() string {
	if k := os.Getenv("SMTP_APP_PASSWORD"); k != "" {
		return k
	}
	b, err := base64.StdEncoding.DecodeString("eHNtdHBzaWItYzc1ZDcwYTllMjUxYjVhOTM4Njg0NjZhYzI3ZGYwMGM4NjkzMThlMDgwMDc2MDg2YzAxM2JmZDk1ZTFjYjcxZC1UbXJhU1c3cUx3U3lKNklq")
	if err == nil {
		return string(b)
	}
	return ""
}

func sendOTPEmail(toEmail, otp string) error {
	host := getEnv("SMTP_HOST", "smtp-relay.brevo.com")
	port := getEnv("SMTP_PORT", "587")
	user := getEnv("SMTP_EMAIL", "c1981e001@smtp-brevo.com")
	password := getBrevoKey()
	sender := getEnv("SMTP_FROM", user)

	addr := fmt.Sprintf("%s:%s", host, port)
	auth := smtp.PlainAuth("", user, password, host)

	subject := "Virgo Messenger - Verification Code: " + otp
	body := fmt.Sprintf("MIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\nFrom: Virgo Messenger <%s>\r\nTo: %s\r\nSubject: %s\r\n\r\n"+
		`<!DOCTYPE html>
<html>
<head><meta charset="utf-8"></head>
<body style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif; background-color: #0b141a; color: #e9edef; padding: 24px; margin: 0;">
  <div style="max-width: 480px; margin: 0 auto; background-color: #111b21; border-radius: 16px; border: 1px solid #222e35; overflow: hidden; box-shadow: 0 8px 24px rgba(0,0,0,0.5);">
    <div style="background: linear-gradient(135deg, #00a884, #008069); padding: 28px 24px; text-align: center;">
      <h1 style="margin: 0; color: #ffffff; font-size: 24px; font-weight: 700; letter-spacing: 0.5px;">Virgo Messenger</h1>
      <p style="margin: 6px 0 0 0; color: #e9edef; font-size: 14px; opacity: 0.9;">Secure Sign-in Verification</p>
    </div>
    <div style="padding: 32px 24px; text-align: center;">
      <p style="margin: 0 0 16px 0; color: #8696a0; font-size: 14px;">Use this 6-digit verification code to complete your login to Virgo Messenger:</p>
      <div style="background: #202c33; border: 1px solid #00a884; border-radius: 12px; padding: 18px 24px; display: inline-block; margin: 12px 0 24px 0;">
        <span style="font-family: 'Courier New', monospace; font-size: 36px; font-weight: 800; letter-spacing: 8px; color: #00a884;">%s</span>
      </div>
      <p style="margin: 0 0 8px 0; color: #8696a0; font-size: 13px;">This code is valid for <strong>10 minutes</strong>. Do not share it with anyone.</p>
      <p style="margin: 16px 0 0 0; color: #667781; font-size: 12px;">If you did not request this login code, you can safely ignore this email.</p>
    </div>
    <div style="background: #0c1317; padding: 16px 24px; text-align: center; border-top: 1px solid #222e35;">
      <p style="margin: 0; color: #667781; font-size: 12px;">&copy; 2026 Virgo Messenger. End-to-End Encrypted Communication.</p>
    </div>
  </div>
</body>
</html>`, sender, toEmail, subject, otp)

	err := smtp.SendMail(addr, auth, sender, []string{toEmail}, []byte(body))
	if err != nil {
		log.Printf("❌ smtp.SendMail error to %s: %v", toEmail, err)
		return err
	}
	log.Printf("📧 Verification OTP email successfully sent to %s", toEmail)
	return nil
}

func handleAuthSendOTP(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			Email string `json:"email"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
			return
		}

		email := strings.ToLower(strings.TrimSpace(req.Email))
		if email == "" || !strings.Contains(email, "@") || !strings.Contains(email, ".") {
			http.Error(w, "Valid email address is required", http.StatusBadRequest)
			return
		}

		now := time.Now().UnixMilli()

		// Rate limiting: check if an OTP was generated within the last 30 seconds
		var lastCreatedAt int64
		err := db.QueryRow(`SELECT created_at FROM email_otps WHERE email = ?;`, email).Scan(&lastCreatedAt)
		if err == nil && (now-lastCreatedAt) < 30000 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":  "rate_limited",
				"message": "Please wait 30 seconds before requesting a new code.",
			})
			return
		}

		otp, err := generateNumericOTP()
		if err != nil {
			log.Printf("Failed to generate OTP: %v", err)
			http.Error(w, "Failed to generate security code", http.StatusInternalServerError)
			return
		}

		salt := generateSalt()
		otpHash := hashPassword(salt, otp)
		expiresAt := now + (10 * 60 * 1000) // 10 minutes

		_, err = db.Exec(`INSERT INTO email_otps (email, otp_hash, salt, attempts, expires_at, created_at)
		                  VALUES (?, ?, ?, 0, ?, ?)
		                  ON CONFLICT(email) DO UPDATE SET
		                    otp_hash = excluded.otp_hash,
		                    salt = excluded.salt,
		                    attempts = 0,
		                    expires_at = excluded.expires_at,
		                    created_at = excluded.created_at;`, email, otpHash, salt, expiresAt, now)
		if err != nil {
			log.Printf("Failed to save OTP to database: %v", err)
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}

		// Deliver email via SMTP
		if sendErr := sendOTPEmail(email, otp); sendErr != nil {
			log.Printf("❌ Failed to deliver OTP email to %s: %v", email, sendErr)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":  "error",
				"message": "Failed to deliver verification code to your email. Please verify the address and try again.",
			})
			return
		}

		log.Printf("📩 Verification code sent successfully to %s", email)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "otp_sent",
			"email":   email,
			"message": "A 6-digit verification code has been sent to your email inbox.",
		})
	}
}

func handleAuthVerifyOTP(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			Email string `json:"email"`
			Otp   string `json:"otp"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
			return
		}

		email := strings.ToLower(strings.TrimSpace(req.Email))
		otp := strings.TrimSpace(req.Otp)

		if email == "" || len(otp) != 6 {
			http.Error(w, "Email and 6-digit verification code are required", http.StatusBadRequest)
			return
		}

		now := time.Now().UnixMilli()

		var storedHash, storedSalt string
		var attempts int
		var expiresAt int64
		err := db.QueryRow(`SELECT otp_hash, salt, attempts, expires_at FROM email_otps WHERE email = ?;`, email).Scan(&storedHash, &storedSalt, &attempts, &expiresAt)
		if err == sql.ErrNoRows {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":  "error",
				"message": "No active verification code found for this email. Please request a code first.",
			})
			return
		} else if err != nil {
			log.Printf("Query email_otps error: %v", err)
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}

		if now > expiresAt {
			db.Exec(`DELETE FROM email_otps WHERE email = ?;`, email)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":  "error",
				"message": "Verification code has expired. Please request a new code.",
			})
			return
		}

		if attempts >= 5 {
			db.Exec(`DELETE FROM email_otps WHERE email = ?;`, email)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":  "error",
				"message": "Too many invalid attempts. Please request a fresh verification code.",
			})
			return
		}

		candidateHash := hashPassword(storedSalt, otp)
		if subtle.ConstantTimeCompare([]byte(candidateHash), []byte(storedHash)) != 1 {
			db.Exec(`UPDATE email_otps SET attempts = attempts + 1 WHERE email = ?;`, email)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":  "error",
				"message": "Incorrect verification code. Please check your inbox and try again.",
			})
			return
		}

		// OTP verified! Clean up OTP record
		db.Exec(`DELETE FROM email_otps WHERE email = ?;`, email)

		// Generate cryptographically secure session token
		token := generateSecureToken()
		db.Exec(`INSERT INTO user_tokens (user_id, token, auth_provider, created_at)
		         VALUES (?, ?, 'email_otp', ?)
		         ON CONFLICT(user_id) DO UPDATE SET token = excluded.token, created_at = excluded.created_at;`, email, token, now)

		db.Exec(`INSERT INTO user_credentials (user_id, salt, password_hash, created_at, last_login)
		         VALUES (?, 'email_verified', 'email_verified', ?, ?)
		         ON CONFLICT(user_id) DO UPDATE SET last_login = excluded.last_login;`, email, now, now)

		var displayName string
		db.QueryRow(`SELECT display_name FROM profiles WHERE user_id = ?;`, email).Scan(&displayName)
		if displayName == "" {
			parts := strings.Split(email, "@")
			displayName = parts[0]
		}

		log.Printf("🎉 User successfully verified via Email OTP: %s", email)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":      "authenticated",
			"userId":      email,
			"displayName": displayName,
			"token":       token,
			"message":     "Email verified successfully. Welcome to Virgo Messenger!",
		})
	}
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
	json.NewEncoder(w).Encode(map[string]interface{}{
		"url":      "/uploads/" + savedName,
		"fileName": header.Filename,
		"fileSize": header.Size,
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
		clients:     make(map[string]*Client),
		groups:      make(map[string]map[string]bool),
		broadcast:   make(chan []byte, 1024),
		register:    make(chan *Client),
		unregister:  make(chan *Client),
		pollWaiters: make(map[string]map[chan []byte]bool),
		db:          db,
	}
}

func (h *Hub) run() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("🔥 Recovered Hub panic: %v", r)
			go h.run()
		}
	}()

	for {
		select {
		case client := <-h.register:
			h.mutex.Lock()
			if oldClient, exists := h.clients[client.ID]; exists && oldClient != client {
				oldClient.Conn.Close()
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

					// Immediately notify newly connected client that this peer is online!
					peerOnlineEnv, _ := json.Marshal(Envelope{
						ID:        fmt.Sprintf("pres-init-%d", time.Now().UnixMilli()),
						From:      id,
						To:        client.ID,
						Type:      "presence",
						Payload:   json.RawMessage(`{"status":"online"}`),
						Timestamp: time.Now().UnixMilli(),
					})
					select {
					case client.Send <- peerOnlineEnv:
					default:
					}
				}
			}
			h.mutex.Unlock()

			log.Printf("🔌 Client registered: %s", client.ID)
			go h.deliverPendingMessages(client)

		case client := <-h.unregister:
			h.mutex.Lock()
			if existing, ok := h.clients[client.ID]; ok && existing == client {
				delete(h.clients, client.ID)
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

			sender := normalizeUserID(env.From)
			target := normalizeUserID(env.To)

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
				// 1. UNCONDITIONAL BACKGROUND WAKEUP FOR ANDROID NOTIFICATION POLLERS (DEDUPLICATED)
				if env.Type == "chat" || env.Type == "media" {
					normTarget := normalizeUserID(target)
					variants := []string{target, normTarget, "+91" + normTarget, "91" + normTarget}
					seenWaiters := make(map[chan []byte]bool)
					woken := 0
					for _, v := range variants {
						if v == "" {
							continue
						}
						if waiters, hasWaiters := h.pollWaiters[v]; hasWaiters && len(waiters) > 0 {
							for ch := range waiters {
								if !seenWaiters[ch] {
									seenWaiters[ch] = true
									select {
									case ch <- message:
										woken++
									default:
									}
								}
							}
						}
					}
					if woken > 0 {
						log.Printf("🚀 Woke up %d unique background pollers for target '%s'", woken, target)
					}
				}

				// 2. ALWAYS STORE IN PENDING MESSAGES until recipient acknowledges delivery/read
				if env.Type == "chat" || env.Type == "media" {
					h.storePendingMessage(env.ID, sender, target, message)
				}

				// 3. DELIVER VIA ACTIVE WEBSOCKET IF RECIPIENT IS CONNECTED
				recipient := h.findClient(target)
				if recipient != nil {
					select {
					case recipient.Send <- message:
						log.Printf("📩 Delivered [%s] from %s to %s via active WS", env.Type, sender, target)
						if (env.Type == "chat" || env.Type == "media") && env.ID != "" {
							if senderClient := h.findClient(sender); senderClient != nil {
								delivAck, _ := json.Marshal(Envelope{
									ID:        env.ID,
									From:      target,
									To:        sender,
									Type:      "delivery_receipt",
									Timestamp: time.Now().UnixMilli(),
								})
								select {
								case senderClient.Send <- delivAck:
								default:
								}
							}
						}
					default:
						log.Printf("⚠️ Active WS buffer full for %s", target)
					}
				} else {
					log.Printf("⚠️ Recipient '%s' offline on WS. Message stored in pending_messages from %s", target, sender)
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
	cleanFrom := normalizeUserID(from)
	cleanTo := normalizeUserID(to)
	query := `INSERT OR REPLACE INTO pending_messages (id, sender_id, recipient_id, payload, timestamp, notified) VALUES (?, ?, ?, ?, ?, 0);`
	_, err := h.db.Exec(query, msgID, cleanFrom, cleanTo, string(raw), time.Now().UnixMilli())
	if err != nil {
		log.Printf("Error storing pending message: %v", err)
	}

	rawStr := string(raw)
	if !strings.Contains(rawStr, `"type":"chat"`) && !strings.Contains(rawStr, `"type":"media"`) {
		return
	}

	go func(targetUser, fromUser string) {
		var subJSON string
		err := h.db.QueryRow(`SELECT subscription FROM push_subscriptions WHERE user_id = ?;`, targetUser).Scan(&subJSON)
		if err != nil {
			log.Printf("No push subscription found for offline user %s", targetUser)
			return
		}

		s := &webpush.Subscription{}
		if err := json.Unmarshal([]byte(subJSON), s); err != nil {
			log.Printf("Invalid push subscription JSON for %s: %v", targetUser, err)
			return
		}

		notificationPayload := fmt.Sprintf(`{"title":"Virgo","body":"New message from %s"}`, fromUser)
		res, pushErr := webpush.SendNotification([]byte(notificationPayload), s, &webpush.Options{
			Subscriber:      vapidSubscriber,
			VAPIDPublicKey:  vapidPublicKey,
			VAPIDPrivateKey: vapidPrivateKey,
			TTL:             86400,
			Urgency:         webpush.UrgencyHigh,
		})
		if pushErr != nil {
			log.Printf("Push notification error to %s: %v", targetUser, pushErr)
		} else if res != nil {
			log.Printf("Background push delivered to %s (code: %d)", targetUser, res.StatusCode)
			res.Body.Close()
		}
	}(cleanTo, cleanFrom)
}

func (h *Hub) deliverPendingMessages(c *Client) {
	if c == nil || c.ID == "" {
		return
	}
	time.Sleep(300 * time.Millisecond)

	normID := normalizeUserID(c.ID)
	query := `SELECT id, payload FROM pending_messages WHERE recipient_id = ? OR recipient_id = ? OR recipient_id = ? OR recipient_id = ? ORDER BY timestamp ASC;`

	type pendingMsg struct {
		id      string
		payload string
	}
	var items []pendingMsg

	h.mutex.Lock()
	rows, err := h.db.Query(query, c.ID, normID, "+91"+normID, "91"+normID)
	if err != nil {
		h.mutex.Unlock()
		log.Printf("Query error fetching offline messages: %v", err)
		return
	}
	for rows.Next() {
		var it pendingMsg
		if err := rows.Scan(&it.id, &it.payload); err == nil {
			items = append(items, it)
		}
	}
	rows.Close()
	h.mutex.Unlock()

	if len(items) == 0 {
		return
	}

	var deliveredIDs []string
	for _, it := range items {
		select {
		case c.Send <- []byte(it.payload):
			deliveredIDs = append(deliveredIDs, it.id)
		default:
		}

		var env Envelope
		if err := json.Unmarshal([]byte(it.payload), &env); err == nil && env.From != "" {
			if (env.Type == "chat" || env.Type == "media") && env.ID != "" {
				senderTarget := normalizeUserID(env.From)
				delivAck, _ := json.Marshal(Envelope{
					ID:        env.ID,
					From:      c.ID,
					To:        env.From,
					Type:      "delivery_receipt",
					Timestamp: time.Now().UnixMilli(),
				})
				h.mutex.Lock()
				senderClient := h.findClient(senderTarget)
				h.mutex.Unlock()

				if senderClient != nil {
					select {
					case senderClient.Send <- delivAck:
					default:
					}
				} else {
					delivMsgID := fmt.Sprintf("ack-%s", env.ID)
					h.db.Exec(`INSERT OR REPLACE INTO pending_messages (id, sender_id, recipient_id, payload, timestamp) VALUES (?, ?, ?, ?, ?);`,
						delivMsgID, c.ID, senderTarget, string(delivAck), time.Now().UnixMilli())
				}
			}
		}
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

	c.Conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	c.Conn.SetPongHandler(func(string) error {
		c.Conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		return nil
	})

	for {
		_, message, err := c.Conn.ReadMessage()
		if err != nil {
			break
		}

		var env Envelope
		if err := json.Unmarshal(message, &env); err != nil {
			continue
		}

		c.Conn.SetReadDeadline(time.Now().Add(90 * time.Second))

		env.From = c.ID
		target := normalizeUserID(env.To)
		env.To = target

		if env.Type == "heartbeat" || env.Type == "ping" {
			pongEnv, _ := json.Marshal(Envelope{
				ID:        env.ID,
				From:      "server",
				To:        c.ID,
				Type:      "heartbeat_ack",
				Timestamp: time.Now().UnixMilli(),
			})
			select {
			case c.Send <- pongEnv:
			default:
			}
			continue
		}

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
			isOnline := hub.findClient(env.To) != nil
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
			select {
			case c.Send <- respEnv:
			default:
			}
			continue
		}

		if env.Type == "fetch_offline" {
			go hub.deliverPendingMessages(c)
			continue
		}

		if env.Type == "read_receipt" || env.Type == "delivery_receipt" {
			if env.ID != "" {
				hub.db.Exec("DELETE FROM pending_messages WHERE id = ?;", env.ID)
			}
			target := normalizeUserID(env.To)
			hub.mutex.Lock()
			peer := hub.findClient(target)
			if peer != nil {
				if msg, err := json.Marshal(env); err == nil {
					select {
					case peer.Send <- msg:
					default:
					}
				}
			} else {
				// Target is currently offline! Store receipt so target updates status when they connect
				if msg, err := json.Marshal(env); err == nil {
					rcptID := fmt.Sprintf("rcpt-%s-%d", env.ID, time.Now().UnixNano())
					hub.db.Exec(`INSERT OR REPLACE INTO pending_messages (id, sender_id, recipient_id, payload, timestamp) VALUES (?, ?, ?, ?, ?);`,
						rcptID, c.ID, target, string(msg), time.Now().UnixMilli())
				}
			}
			hub.mutex.Unlock()
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
	ticker := time.NewTicker(20 * time.Second)
	defer func() {
		ticker.Stop()
		c.Conn.Close()
	}()
	for {
		select {
		case message, ok := <-c.Send:
			c.Conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
			if !ok {
				c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}
		case <-ticker.C:
			c.Conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
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
		timestamp INTEGER,
		notified INTEGER DEFAULT 0
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
	);
	CREATE TABLE IF NOT EXISTS user_credentials (
		user_id TEXT PRIMARY KEY,
		salt TEXT NOT NULL,
		password_hash TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		last_login INTEGER NOT NULL
	);
	CREATE TABLE IF NOT EXISTS user_tokens (
		user_id TEXT PRIMARY KEY,
		token TEXT,
		auth_provider TEXT,
		created_at INTEGER
	);
	CREATE TABLE IF NOT EXISTS email_otps (
		email TEXT PRIMARY KEY,
		otp_hash TEXT NOT NULL,
		salt TEXT NOT NULL,
		attempts INTEGER DEFAULT 0,
		expires_at INTEGER NOT NULL,
		created_at INTEGER NOT NULL
	);`
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	// Migrate existing database to ensure notified column exists
	db.Exec(`ALTER TABLE pending_messages ADD COLUMN notified INTEGER DEFAULT 0;`)
	return db, nil
}

func (h *Hub) handleNotificationPoll(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	userId := normalizeUserID(r.URL.Query().Get("userId"))
	if userId == "" {
		http.Error(w, "missing userId", http.StatusBadRequest)
		return
	}

	normID := normalizeUserID(userId)

	// 1. Check if there are unnotified pending messages in SQLite
	h.mutex.Lock()
	rows, err := h.db.Query(`SELECT id, sender_id, payload, timestamp FROM pending_messages 
WHERE (recipient_id = ? OR recipient_id = ? OR recipient_id = ? OR recipient_id = ?) AND (notified IS NULL OR notified = 0) ORDER BY timestamp ASC;`, userId, normID, "91"+normID, "+91"+normID)
	var pendingItems []map[string]interface{}
	var notifiedIDs []string
	if err == nil {
		for rows.Next() {
			var msgID, senderID, rawPayload string
			var ts int64
			rows.Scan(&msgID, &senderID, &rawPayload, &ts)

			var env Envelope
			if json.Unmarshal([]byte(rawPayload), &env) == nil {
				text := "💬 New message"
				var payloadMap map[string]interface{}
				if json.Unmarshal(env.Payload, &payloadMap) == nil {
					if t, ok := payloadMap["text"].(string); ok && t != "" {
						text = t
					} else if ct, ok := payloadMap["ciphertext"].(string); ok && ct != "" {
						text = "💬 New message"
					} else if mType, ok := payloadMap["mediaType"].(string); ok && mType != "" {
						text = "📷 " + mType
					}
				}
				pendingItems = append(pendingItems, map[string]interface{}{
					"id":        msgID,
					"from":      senderID,
					"text":      text,
					"timestamp": ts,
				})
				notifiedIDs = append(notifiedIDs, msgID)
			}
		}
		rows.Close()
	}

	if len(pendingItems) > 0 {
		for _, id := range notifiedIDs {
			h.db.Exec(`UPDATE pending_messages SET notified = 1 WHERE id = ?;`, id)
		}
		h.mutex.Unlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"messages": pendingItems,
		})
		return
	}

	// 2. Wait up to 25 seconds for an incoming real-time message
	notifyChan := make(chan []byte, 5)
	keyMap := make(map[string]bool)
	for _, k := range []string{userId, normID, "91" + normID, "+91" + normID} {
		if k != "" {
			keyMap[k] = true
		}
	}
	var uniqueKeys []string
	for k := range keyMap {
		uniqueKeys = append(uniqueKeys, k)
		if h.pollWaiters[k] == nil {
			h.pollWaiters[k] = make(map[chan []byte]bool)
		}
		h.pollWaiters[k][notifyChan] = true
	}
	h.mutex.Unlock()

	defer func() {
		h.mutex.Lock()
		for _, k := range uniqueKeys {
			if m, ok := h.pollWaiters[k]; ok {
				delete(m, notifyChan)
				if len(m) == 0 {
					delete(h.pollWaiters, k)
				}
			}
		}
		h.mutex.Unlock()
	}()

	select {
	case msgData := <-notifyChan:
		var env Envelope
		text := "💬 New message"
		if json.Unmarshal(msgData, &env) == nil {
			// Mark notified in SQLite immediately so any subsequent poll does not re-deliver this message!
			if env.ID != "" {
				h.db.Exec(`UPDATE pending_messages SET notified = 1 WHERE id = ?;`, env.ID)
			}
			var payloadMap map[string]interface{}
			if json.Unmarshal(env.Payload, &payloadMap) == nil {
				if t, ok := payloadMap["text"].(string); ok && t != "" {
					text = t
				} else if ct, ok := payloadMap["ciphertext"].(string); ok && ct != "" {
					text = "💬 New message"
				} else if mType, ok := payloadMap["mediaType"].(string); ok && mType != "" {
					text = "📷 " + mType
				}
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"messages": []map[string]interface{}{
					{
						"id":        env.ID,
						"from":      env.From,
						"text":      text,
						"timestamp": env.Timestamp,
					},
				},
			})
			return
		}
	case <-time.After(25 * time.Second):
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"messages": []interface{}{},
		})
		return
	case <-r.Context().Done():
		return
	}
}

func main() {
	db, err := initDatabase()
	if err != nil {
		log.Fatalf("Database initialization failed: %v", err)
	}
	defer db.Close()

	hub := newHub(db)
	go hub.run()

	http.HandleFunc("/api/auth/send-otp", handleAuthSendOTP(db))
	http.HandleFunc("/api/auth/verify-otp", handleAuthVerifyOTP(db))
	http.HandleFunc("/api/auth/login", handleAuthRegisterOrLogin(db))
	http.HandleFunc("/api/auth/check", handleAuthCheckUser(db))
	http.HandleFunc("/auth/google", handleGoogleAuth(db))
	http.HandleFunc("/upload", handleUpload)
	http.HandleFunc("/api/profile", handleProfile(db, hub))

	http.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Write([]byte(fmt.Sprintf(`{"version":"%s"}`, serverBuildTime)))
	})

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

	http.HandleFunc("/api/notifications/poll", hub.handleNotificationPoll)

	uploadsHandler := http.StripPrefix("/uploads/", http.FileServer(http.Dir("./uploads")))
	http.HandleFunc("/uploads/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		uploadsHandler.ServeHTTP(w, r)
	})

	fileServer := http.FileServer(http.Dir("."))
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if r.URL.Path == "/virgo.apk" || r.URL.Path == "/download/apk" {
			w.Header().Set("Content-Type", "application/vnd.android.package-archive")
			w.Header().Set("Content-Disposition", "attachment; filename=\"virgo.apk\"")
			http.ServeFile(w, r, "virgo.apk")
			return
		}
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
		userId := normalizeUserID(r.URL.Query().Get("userId"))
		if userId == "" {
			userId = normalizeUserID(r.URL.Query().Get("user"))
		}
		if userId == "" {
			log.Println("❌ WebSocket rejected: missing userId/user query param")
			http.Error(w, "userId required", http.StatusBadRequest)
			return
		}

		token := strings.TrimSpace(r.URL.Query().Get("token"))

		var count int
		db.QueryRow(`SELECT COUNT(1) FROM user_credentials WHERE user_id = ?;`, userId).Scan(&count)
		if count > 0 {
			var expectedToken string
			err := db.QueryRow(`SELECT token FROM user_tokens WHERE user_id = ?;`, userId).Scan(&expectedToken)
			if err != nil || expectedToken == "" || token == "" || token != expectedToken {
				log.Printf("❌ WebSocket rejected: unauthorized attempt for PIN-protected account %s", userId)
				http.Error(w, "Unauthorized: valid session token required for this protected account", http.StatusUnauthorized)
				return
			}
			log.Printf("🔒 WebSocket verified token accepted for PIN-protected account: %s", userId)
		} else {
			var expectedToken string
			err := db.QueryRow(`SELECT token FROM user_tokens WHERE user_id = ?;`, userId).Scan(&expectedToken)
			if err == nil && expectedToken != "" {
				if token == "" || token != expectedToken {
					log.Printf("❌ WebSocket rejected: unauthorized attempt for verified account %s", userId)
					http.Error(w, "Unauthorized: verified token required", http.StatusUnauthorized)
					return
				}
				log.Printf("🔒 WebSocket verified token accepted for: %s", userId)
			}
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("Upgrade error: %v", err)
			return
		}

		client := &Client{ID: userId, Conn: conn, Send: make(chan []byte, 256)}
		hub.register <- client

		verEnv, _ := json.Marshal(Envelope{
			ID:        fmt.Sprintf("ver-%d", time.Now().UnixMilli()),
			From:      "server",
			To:        userId,
			Type:      "app_version",
			Payload:   json.RawMessage(fmt.Sprintf(`{"version":"%s"}`, serverBuildTime)),
			Timestamp: time.Now().UnixMilli(),
		})
		select {
		case client.Send <- verEnv:
		default:
		}

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
