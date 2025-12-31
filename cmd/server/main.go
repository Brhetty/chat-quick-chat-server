package main

import (
	"chat-quick-chat-server/internal/db"
	"chat-quick-chat-server/internal/handlers"
	"chat-quick-chat-server/internal/realtime"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

func main() {
	// Directories
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	dataDir := filepath.Join(cwd, "data")
	storageDir := filepath.Join(cwd, "storage", "chat-media")

	// Ensure directories exist
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(storageDir, 0755); err != nil {
		log.Fatal(err)
	}

	// Initialize DB
	database := db.New(dataDir)
	if err := database.Load(); err != nil {
		log.Printf("Warning: Failed to load database: %v", err)
	}

	// Initialize Realtime Hub
	hub := realtime.NewHub()
	go hub.Run()

	// Initialize Handlers
	handler := handlers.New(database, storageDir, hub)

	// Server
	port := "8000"
	if envPort := os.Getenv("PORT"); envPort != "" {
		port = envPort
	}

	fmt.Printf("Server starting on port %s...\n", port)
	fmt.Printf("Data directory: %s\n", dataDir)
	fmt.Printf("Storage directory: %s\n", storageDir)

	if err := http.ListenAndServe(":"+port, handler); err != nil {
		log.Fatal(err)
	}
}
