package kernel

import "testing"

// Register cleanup at construction, including early assertion failures and
// helpers whose caller might otherwise leave Restore's periodic jobs running.
func newTestKernelManager(t testing.TB, baseDir string) *Manager {
	t.Helper()
	m := NewManager(baseDir)
	t.Cleanup(m.Shutdown)
	return m
}
