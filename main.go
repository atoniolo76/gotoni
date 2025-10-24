package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Response struct {
	Data map[string]struct {
		InstanceType                 InstanceType `json:"instance_type"`
		RegionsWithCapacityAvailable []Region     `json:"regions_with_capacity_available"`
	} `json:"data"`
}

type InstanceType struct {
	Name              string `json:"name"`
	Description       string `json:"description"`
	GPUDescription    string `json:"gpu_description"`
	PriceCentsPerHour int    `json:"price_cents_per_hour"`
	Specs             struct {
		VCPUs      int `json:"vcpus"`
		MemoryGib  int `json:"memory_gib"`
		StorageGib int `json:"storage_gib"`
		GPUs       int `json:"gpus"`
	} `json:"specs"`
}

type Region struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func main() {
	c := http.Client{Timeout: time.Duration(5) * time.Second}
	req, err := http.NewRequest("GET", "https://cloud.lambda.ai/api/v1/instance-types", nil)
	if err != nil {
		fmt.Println("Error:", err)
		return
	}

	req.Header.Set("Authorization", "Bearer secret_gpusnapshot_e0390e0a3cf5471d9e9eae8af354a72d.HrNIhxnX0vd7sPft7nTLAqxQAzHWyBki")

	resp, err := c.Do(req)
	if err != nil {
		fmt.Println("Error:", err)
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Println("Error:", err)
		return
	}

	var response Response
	err = json.Unmarshal(body, &response)
	if err != nil {
		fmt.Println("Error:", err)
		return
	}

	for _, item := range response.Data {
		if len(item.RegionsWithCapacityAvailable) == 0 {
			continue
		}
		fmt.Printf("Instance Type: %s\n", item.InstanceType.Name)
	}
}
