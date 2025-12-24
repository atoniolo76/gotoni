/*
Copyright © 2025 ALESSIO TONIOLO

serve.go contians the logic for load balancing and request routing
*/
package serve

import "github.com/atoniolo76/gotoni/pkg/client"

type Cluster struct {
	Instances []client.RunningInstance
}

var latencyMap = map[string]int{
	"us-us":     0,
	"us-eu":     102,
	"us-asia":   124,
	"us-sa":     137,
	"eu-us":     102,
	"eu-eu":     0,
	"eu-asia":   222,
	"eu-sa":     200,
	"asia-us":   124,
	"asia-eu":   222,
	"asia-asia": 0,
	"asia-sa":   256,
	"sa-us":     137,
	"sa-eu":     200,
	"sa-asia":   257,
	"sa-sa":     0,
}
