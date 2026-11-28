package ws

import "sync"

func (s *Server) nodeLifecycleLock(nodeID int64) *sync.Mutex {
	return &s.nodeLifecycle[uint64(nodeID)%uint64(len(s.nodeLifecycle))]
}

// IfNodeDisconnected serializes an offline reconciliation with agent session
// registration and teardown. The callback may update the database and broadcast
// the result, but must not register or disconnect a node itself.
func (s *Server) IfNodeDisconnected(nodeID int64, apply func() error) (bool, error) {
	lifecycle := s.nodeLifecycleLock(nodeID)
	lifecycle.Lock()
	defer lifecycle.Unlock()
	if s.IsNodeOnline(nodeID) {
		return false, nil
	}
	return true, apply()
}
