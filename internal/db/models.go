package db

import "time"

type ChatSession struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}

type Message struct {
	ID          string    `json:"id"`
	SessionID   string    `json:"session_id"`
	Content     *string   `json:"content"`
	MessageType string    `json:"message_type"`
	FileURL     *string   `json:"file_url"`
	SenderName  *string   `json:"sender_name"`
	CreatedAt   time.Time `json:"created_at"`
}
