package policy

import "github.com/atoniolo76/gotoni/pkg/client"

type Node struct {
	text            string
	storedInstances []client.RunningInstance
	parent          Node
	left            Node
	right           Node
}

type PrefixTree struct {
	nodes []Node
}
