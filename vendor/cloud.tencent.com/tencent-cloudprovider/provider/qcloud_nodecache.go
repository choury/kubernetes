package qcloud

import (
	"k8s.io/api/core/v1"
	"sync"
)

type cachedNode struct {
	// The cached state of the cluster
	state *v1.Node
}

type nodeCache struct {
	mu      sync.Mutex // protects clusterMap
	nodeMap map[string]*cachedNode
}

// ListKeys implements the interface required by DeltaFIFO to list the keys we
// already know about.
func (s *nodeCache) ListKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.nodeMap))
	for k := range s.nodeMap {
		keys = append(keys, k)
	}
	return keys
}

// GetByKey returns the value stored in the clusterMap under the given key
func (s *nodeCache) GetByKey(key string) (interface{}, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.nodeMap[key]; ok {
		return v, true, nil
	}
	return nil, false, nil
}

// ListKeys implements the interface required by DeltaFIFO to list the keys we
// already know about.
func (s *nodeCache) allNodes() []*v1.Node {
	s.mu.Lock()
	defer s.mu.Unlock()
	nodes := make([]*v1.Node, 0, len(s.nodeMap))
	for _, v := range s.nodeMap {
		nodes = append(nodes, v.state)
	}
	return nodes
}

func (s *nodeCache) get(nodeName string) (*cachedNode, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	node, ok := s.nodeMap[nodeName]
	return node, ok
}

func (s *nodeCache) Exist(nodeName string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.nodeMap[nodeName]
	return ok
}

func (s *nodeCache) getOrCreate(node *v1.Node) *cachedNode {
	s.mu.Lock()
	defer s.mu.Unlock()
	nodeC, ok := s.nodeMap[node.Name]
	if !ok {
		nodeC = &cachedNode{state: node}
		s.nodeMap[node.Name] = nodeC
	}
	return nodeC
}

func (s *nodeCache) set(nodeName string, node *cachedNode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nodeMap[nodeName] = node
}

func (s *nodeCache) delete(nodeName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.nodeMap, nodeName)
}
