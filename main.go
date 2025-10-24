package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Response struct {
	Data map[string]Instance `json:"data"`
}

type Instance struct {
	InstanceType                 InstanceType `json:"instance_type"`
	RegionsWithCapacityAvailable []Region     `json:"regions_with_capacity_available"`
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
	instances, err := getAvailableInstanceTypes("secret_gpusnapshot_e0390e0a3cf5471d9e9eae8af354a72d.HrNIhxnX0vd7sPft7nTLAqxQAzHWyBki")
	if err != nil {
		fmt.Println("Error getting available instance types: ", err)
		return
	}

	fmt.Printf("Instances: %+v\n", instances)
}

func getAvailableInstanceTypes(apiToken string) ([]Instance, error) {
	c := http.Client{Timeout: time.Duration(5) * time.Second}
	req, err := http.NewRequest("GET", "https://cloud.lambda.ai/api/v1/instance-types", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiToken))

	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API request failed with status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var response Response
	err = json.Unmarshal(body, &response)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	var instances []Instance
	for _, instance := range response.Data {
		if len(instance.RegionsWithCapacityAvailable) == 0 {
			continue
		}
		instances = append(instances, instance)
	}

	return instances, nil
}
