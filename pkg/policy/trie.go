package policy

import (
	"github.com/atoniolo76/gotoni/pkg/client"
)

type Node struct {
	data            byte
	storedInstances []*client.RunningInstance
	low             *Node
	equal           *Node
	high            *Node
}

func Insert(prefix string, instance *client.RunningInstance, root **Node) {
	if *root == nil {
		*root = &Node{data: prefix[0]}
	}
	currentRoot := *root
	for i := 0; i < len(prefix); i++ {
		char := prefix[i]

		if currentRoot.data > char {
			currentRoot = currentRoot.low

		} else if currentRoot.data < char {
			currentRoot = currentRoot.high

		} else {
			if i == len(prefix)-1 {
				currentRoot.storedInstances = append(currentRoot.storedInstances, instance)
			} else {
				if currentRoot.equal == nil {
					currentRoot.equal = &Node{data: prefix[i+1]}
				}
				currentRoot = currentRoot.equal
			}
		}
	}
}

func Search(prefix string, root *Node) []*client.RunningInstance {
	currentRoot := root
	for i := 0; i < len(prefix); i++ {
		if currentRoot == nil {
			return nil
		}

		char := prefix[i]
		if currentRoot.data > char {
			currentRoot = currentRoot.low

		} else if currentRoot.data < char {
			currentRoot = currentRoot.high

		} else {
			if i == len(prefix)-1 {
				return currentRoot.storedInstances
			} else {
				currentRoot = currentRoot.equal
			}
		}
	}
	return nil
}
