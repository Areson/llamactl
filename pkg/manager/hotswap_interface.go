package manager

// HotSwapManager is the interface for the hot-swap HTTP handler.
// Implemented by instanceManager on Windows. On other platforms the
// type assertion fails and the handler returns 501.
type HotSwapManager interface {
	HotSwap(binaryPath string) error
	SignalHotSwapExit()
}
