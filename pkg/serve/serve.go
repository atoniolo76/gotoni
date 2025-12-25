/*
Copyright © 2025 ALESSIO TONIOLO

serve.go contians the logic for load balancing and request routing
*/
package serve

import (
	"net/http"

	"github.com/atoniolo76/gotoni/pkg/client"
)

func LaunchCluster(httpClient *http.Client, apiToken string) *Cluster {
	provider, _ := client.GetCloudProvider()

	// Get available instance types from the cloud provider
	availableInstances, err := provider.GetAvailableInstanceTypes(httpClient, apiToken)
	if err != nil {
		// Handle error - for now just return empty cluster
		return &Cluster{}
	}

	// Target regions: us-west, us-east, us-south
	targetRegions := []string{"us-west-1", "us-east-1", "us-south-1"}

	// Launch one instance per region using the first available instance type for each region
	for i, targetRegion := range targetRegions {
		// Find the first instance type that has capacity in this region
		for _, instance := range availableInstances {
			// Check if this instance is available in the target region
			for _, region := range instance.RegionsWithCapacityAvailable {
				if region.Name == targetRegion {
					// Launch one instance of this type in this region
					copyRegion := targetRegion
					targetRegions[i] = ""
					go provider.LaunchInstance(httpClient, apiToken, instance.InstanceType.Name, copyRegion, 1, "gotoni-cluster", "", "")
					break
				}
			}
		}
	}

	return &Cluster{}
}

type PrefixTree struct {
	Root *Node
}

type Node struct {
	Prefix          string
	Left            *Node
	Right           *Node
	Parent          *Node
	StoredInstances []*client.RunningInstance
}

func NewPrefixTree() *PrefixTree {
	return &PrefixTree{
		Root: nil,
	}
}

func InsertPrefix(prefixTree *PrefixTree, prefix string, root *Node, instance *client.RunningInstance) {
	if root == nil {
		root = &Node{
			Prefix:          prefix,
			Left:            nil,
			Right:           nil,
			Parent:          nil,
			StoredInstances: []*client.RunningInstance{instance},
		}
		return
	}

	if prefix < root.Prefix {
		InsertPrefix(prefixTree, prefix, root.Left, instance)
	} else if prefix > root.Prefix {
		InsertPrefix(prefixTree, prefix, root.Right, instance)
	} else {
		root.StoredInstances = append(root.StoredInstances, instance)
	}
}

var load = make(map[*client.RunningInstance]int, 3)

func PrefixMatch(prefixTree *PrefixTree, prefix string) *Node {
	root := prefixTree.Root
	current := 0
	match := root
	currentNode := root

	while

}

func prefixTreePolicy(request string, instances []client.RunningInstance) *client.RunningInstance {
	prefixTree := NewPrefixTree()

}
