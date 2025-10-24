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

var minCudaVersions = map[string]string{
	"H100_80GB_HBM3": "12.0",
	"H100_80GB_SXM5": "12.0",
	"H100_NVL":       "12.0",
	"A100_40GB_PCIE": "11.0",
	"A100_80GB_PCIE": "11.0",
	"A100_80GB_SXM4": "11.0",
	"A6000":          "11.0",
	"A5000":          "11.0",
	"A4000":          "11.0",
	"RTX_6000_ADA":   "11.8",
	"RTX_5000_ADA":   "11.8",
	"RTX_4000_ADA":   "11.8",
	"RTX_6000":       "10.0",
	"A10":            "11.0",
	"L40":            "11.8",
	"L40S":           "11.8",
	"B200":           "12.0",
	"GH200":          "12.0",
	"V100_16GB":      "9.0",
	"V100_32GB":      "9.0",
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
