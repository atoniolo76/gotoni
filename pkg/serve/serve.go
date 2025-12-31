/*
Copyright © 2025 ALESSIO TONIOLO

serve.go contains trie data structures and load balancing logic
*/
package serve

import (
	"github.com/atoniolo76/gotoni/pkg/remote"
)

// Ternary Search Trie implementation (from pkg/policy/trie.go)

type TernaryNode struct {
	data            byte
	storedInstances []*remote.RunningInstance
	low             *TernaryNode
	equal           *TernaryNode
	high            *TernaryNode
}

func TernaryInsert(prefix string, instance *remote.RunningInstance, root **TernaryNode) {
	if *root == nil {
		*root = &TernaryNode{data: prefix[0]}
	}
	currentRoot := *root
	for i := 0; i < len(prefix); i++ {
		char := prefix[i]

		if currentRoot.data > char {
			if currentRoot.low == nil {
				currentRoot.low = &TernaryNode{data: char}
			}
			currentRoot = currentRoot.low

		} else if currentRoot.data < char {
			if currentRoot.high == nil {
				currentRoot.high = &TernaryNode{data: char}
			}
			currentRoot = currentRoot.high

		} else {
			if i == len(prefix)-1 {
				currentRoot.storedInstances = append(currentRoot.storedInstances, instance)
			} else {
				if currentRoot.equal == nil {
					currentRoot.equal = &TernaryNode{data: prefix[i+1]}
				}
				currentRoot = currentRoot.equal
			}
		}
	}
}

func TernarySearch(prefix string, root *TernaryNode) []*remote.RunningInstance {
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

// Prefix Tree implementation (from pkg/serve/cluster.go)

type PrefixTree struct {
	Root *PrefixNode
}

type PrefixNode struct {
	Prefix          string
	Left            *PrefixNode
	Right           *PrefixNode
	Parent          *PrefixNode
	StoredInstances []*remote.RunningInstance
}

func NewPrefixTree() *PrefixTree {
	return &PrefixTree{
		Root: nil,
	}
}

func InsertPrefix(prefixTree *PrefixTree, prefix string, root *PrefixNode, instance *remote.RunningInstance) *PrefixNode {
	if root == nil {
		root = &PrefixNode{
			Prefix:          prefix,
			Left:            nil,
			Right:           nil,
			Parent:          nil,
			StoredInstances: []*remote.RunningInstance{instance},
		}
		if prefixTree.Root == nil {
			prefixTree.Root = root
		}
		return root
	}

	if prefix < root.Prefix {
		root.Left = InsertPrefix(prefixTree, prefix, root.Left, instance)
		if root.Left != nil {
			root.Left.Parent = root
		}
	} else if prefix > root.Prefix {
		root.Right = InsertPrefix(prefixTree, prefix, root.Right, instance)
		if root.Right != nil {
			root.Right.Parent = root
		}
	} else {
		root.StoredInstances = append(root.StoredInstances, instance)
	}
	return root
}

var load = make(map[*remote.RunningInstance]int, 3)

func PrefixMatch(prefixTree *PrefixTree, prefix string) *PrefixNode {
	root := prefixTree.Root
	match := root
	currentNode := root
	current := 0

	for currentNode != nil && current < len(prefix) {
		if current >= len(currentNode.Prefix) {
			break
		}
		if prefix[current] < currentNode.Prefix[current] {
			currentNode = currentNode.Left
		} else if prefix[current] > currentNode.Prefix[current] {
			currentNode = currentNode.Right
		} else {
			match = currentNode
			current++
			if current == len(prefix) {
				break
			}
			// Continue with next character
		}
	}

	return match
}

func PrefixTreePolicy(request string, instances []remote.RunningInstance) *remote.RunningInstance {
	prefixTree := NewPrefixTree()

	// Build prefix tree from instances
	for _, instance := range instances {
		// Use instance ID as prefix for simplicity
		prefixTree.Root = InsertPrefix(prefixTree, instance.ID, prefixTree.Root, &instance)
	}

	// Find matching instance based on request
	node := PrefixMatch(prefixTree, request)
	if node != nil && len(node.StoredInstances) > 0 {
		// Return first instance (could implement load balancing here)
		return node.StoredInstances[0]
	}

	return nil
}
