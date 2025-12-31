package handlers

import (
	"chat-quick-chat-server/internal/db"
	"chat-quick-chat-server/internal/realtime"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type Handler struct {
	DB         *db.Database
	StorageDir string
	Hub        *realtime.Hub
}

func New(database *db.Database, storageDir string, hub *realtime.Hub) *Handler {
	return &Handler{
		DB:         database,
		StorageDir: storageDir,
		Hub:        hub,
	}
}

func extractEqValue(s string) string {
	if strings.HasPrefix(s, "eq.") {
		return s[3:]
	}
	return s
}

// firstFileFromMultipart returns the first file part regardless of field name (even name="").
func firstFileFromMultipart(r *http.Request) (io.ReadCloser, string, error) {
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return nil, "", err
	}
	boundary, ok := params["boundary"]
	if !ok {
		return nil, "", fmt.Errorf("missing multipart boundary")
	}

	mr := multipart.NewReader(r.Body, boundary)
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return nil, "", fmt.Errorf("no file in multipart form")
		}
		if err != nil {
			return nil, "", err
		}
		if part.FileName() == "" {
			continue
		}
		return part, part.FileName(), nil
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// CORS
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	path := r.URL.Path
	if strings.HasPrefix(path, "/rest/v1/chat_sessions") {
		h.handleChatSessions(w, r)
	} else if strings.HasPrefix(path, "/rest/v1/messages") {
		h.handleMessages(w, r)
	} else if strings.HasPrefix(path, "/storage/v1/object/chat-media/") {
		h.handleStorageUpload(w, r)
	} else if strings.HasPrefix(path, "/storage/v1/object/public/chat-media/") {
		h.handleStorageServe(w, r)
	} else if strings.HasPrefix(path, "/realtime/v1/websocket") {
		realtime.ServeWs(h.Hub, w, r)
	} else {
		http.NotFound(w, r)
	}
}

func (h *Handler) handleChatSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		// Create session
		session, err := h.DB.CreateSession()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(session)
		return
	}

	if r.Method == "GET" {
		// Check session exists
		// Query: id=eq.{sessionId}
		idParam := r.URL.Query().Get("id")
		if idParam == "" {
			http.Error(w, "Missing id parameter", http.StatusBadRequest)
			return
		}
		id := extractEqValue(idParam)

		session, err := h.DB.GetSession(id)
		if err != nil {
			// Supabase returns null or empty list depending on query, but .single() expects one.
			// If not found, .single() in client throws error.
			// We can return 404 or empty body.
			// Supabase REST: [] if not found (without single), or 406 Not Acceptable with single?
			// Let's return 404 or null.
			// The client code: .eq('id', sessionId).single()
			// If we return [], .single() fails.
			// If we return null, .single() might fail.
			// Let's return 404 for simplicity, or empty array if they didn't ask for single representation?
			// Actually, Supabase REST returns [] by default.
			// But if the client uses .single(), the client library handles the [] -> error conversion if empty.
			// So we should return [].
			// BUT, if the client sends `Accept: application/vnd.pgrst.object+json`, we should return object.
			// Let's just return the object if found, or 404 if not found, which is standard REST.
			// Wait, Supabase returns 200 OK with [] if not found usually.
			// Let's try returning the array with one element if found.
			w.Header().Set("Content-Type", "application/json")
			// If we want to support .single() properly, we should check headers.
			// But for this mock, let's return the object directly if found?
			// No, standard PostgREST returns array unless header is set.
			// However, the prompt says "compatible with supabase url".
			// Let's return an array containing the session.
			json.NewEncoder(w).Encode([]*db.ChatSession{session})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		// Return array
		json.NewEncoder(w).Encode(session)
	}
}

func (h *Handler) handleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		var msg db.Message
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		createdMsg, err := h.DB.CreateMessage(msg)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		type columnInfo struct {
			Name string `json:"name"`
			Type string `json:"type"`
		}
		// Broadcast
		payload := map[string]interface{}{
			"schema":           "public",
			"table":            "messages",
			"commit_timestamp": createdMsg.CreatedAt,
			"type":             "INSERT",
			"record":           createdMsg,
			"old":              map[string]interface{}{},
			"errors":           nil,
			"columns": []columnInfo{
				{Name: "session_id", Type: "uuid"},
				{Name: "content", Type: "text"},
				{Name: "message_type", Type: "text"},
				{Name: "file_url", Type: "text"},
				{Name: "sender_name", Type: "text"},
				{Name: "created_at", Type: "timestamptz"},
			},
		}

		data := map[string]interface{}{
			"data": payload,
			"ids":  []interface{}{},
		}
		h.Hub.Broadcast("realtime:messages:"+createdMsg.SessionID, "postgres_changes", data)

		w.WriteHeader(http.StatusCreated)
		// If Prefer: return=representation is set (it usually is by default in supabase-js insert), return the object.
		// We'll just always return it to be safe.
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]*db.Message{createdMsg})
		return
	}

	if r.Method == "GET" {
		// session_id=eq.{sessionId}
		sessionIDParam := r.URL.Query().Get("session_id")
		if sessionIDParam == "" {
			http.Error(w, "Missing session_id parameter", http.StatusBadRequest)
			return
		}
		sessionID := extractEqValue(sessionIDParam)

		messages, err := h.DB.GetMessages(sessionID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(messages)
	}
}

func (h *Handler) handleStorageUpload(w http.ResponseWriter, r *http.Request) {
	// Path: /storage/v1/object/chat-media/{fileName}
	// The fileName is the rest of the path.
	prefix := "/storage/v1/object/chat-media/"
	fileName := strings.TrimPrefix(r.URL.Path, prefix)

	if fileName == "" {
		http.Error(w, "Filename required", http.StatusBadRequest)
		return
	}

	// Ensure storage dir exists
	fullPath := filepath.Join(h.StorageDir, fileName)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Create file
	dst, err := os.Create(fullPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	// Copy body to file
	// Supabase upload sends the file in the body.
	// It might be multipart/form-data or raw binary.
	// supabase-js upload() uses FormData if in browser, or raw body?
	// "If the file is a Blob, File, or Buffer, it is sent as the body of the request."
	// If it's FormData, we need to parse it.
	// Let's check Content-Type.
	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		file, _, err := firstFileFromMultipart(r)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to read multipart file: %v", err), http.StatusBadRequest)
			return
		}
		defer file.Close()
		if _, err := io.Copy(dst, file); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		// Raw body
		if _, err := io.Copy(dst, r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// Return success
	// Supabase returns: { "Key": "chat-media/filename" }
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"Key": "chat-media/" + fileName,
	})
}

func (h *Handler) handleStorageServe(w http.ResponseWriter, r *http.Request) {
	// Path: /storage/v1/object/public/chat-media/{fileName}
	prefix := "/storage/v1/object/public/chat-media/"
	fileName := strings.TrimPrefix(r.URL.Path, prefix)

	if fileName == "" {
		http.NotFound(w, r)
		return
	}

	fullPath := filepath.Join(h.StorageDir, fileName)
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		http.NotFound(w, r)
		return
	}

	http.ServeFile(w, r, fullPath)
}
