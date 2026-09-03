//go:build !windows

package manager

func (im *instanceManager) startupSweep() {}

func (im *instanceManager) cleanupOldBinary(aPID int) {}
