package main

import (
	"fmt"
	"log"

	"github.com/atoniolo76/gotoni/pkg/db"
)

func main() {
	fmt.Println("Initializing DB...")
	database, err := db.InitDB()
	if err != nil {
		log.Fatalf("Failed to init db: %v", err)
	}
	defer database.Close()
	fmt.Println("DB initialized successfully!")
}

