package main

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"toni/gotoni/pkg/client"
)

func main() {
	apiToken := os.Getenv("LAMBDA_API_KEY")
	c := http.Client{Timeout: time.Duration(5) * time.Second}

	instances, err := client.GetAvailableInstanceTypes(&c, apiToken)
	if err != nil {
		fmt.Println("Error getting available instance types: ", err)
		return
	}
	fmt.Println("Available instance types: ", instances)
}
