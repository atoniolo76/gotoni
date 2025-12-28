package policy

import (
	"github.com/atoniolo76/gotoni/pkg/client"
)

type Node struct {
	prefix          string
	storedInstances []*client.RunningInstance
	isEndOfWord     bool
	low             *Node
	equal           *Node
	high            *Node
}

type TernaryTree struct {
	root *Node
}

func Insert(prefix string, instance *client.RunningInstance, root *Node) {
	currentPrefix := prefix
	currentRoot := root
	for i := 0; i < len(prefix); i++ {
		if currentRoot == nil {
			currentRoot = &Node{
				prefix:          currentPrefix,
				storedInstances: []*client.RunningInstance{instance},
				isEndOfWord:     true,
				low:             nil,
				equal:           nil,
				high:            nil,
			}
		}

		if currentRoot.prefix[i] > currentPrefix[i] {
			currentPrefix = currentPrefix[:i]
			currentRoot = currentRoot.low

		} else if currentRoot.prefix[i] < currentPrefix[i] {
			currentPrefix = currentPrefix[:i]
			currentRoot = currentRoot.high

		} else {
			if i == len(prefix)-1 {
				currentRoot.storedInstances = append(currentRoot.storedInstances, instance)
				currentRoot.isEndOfWord = true
			} else {
				currentPrefix = currentPrefix[:i+1]
				currentRoot = currentRoot.equal
			}
		}
	}
}
