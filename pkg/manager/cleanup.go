//go:build windows

package manager

import (
	"log"
	"os"
	"path/filepath"
	"time"

	"llamactl/pkg/instance"
)

// cleanupOldBinary is B's background goroutine: it polls A's PID for up to
// 60 seconds, and when A dies, deletes the old binary (llamactl.exe.old).
func (im *instanceManager) cleanupOldBinary(aPID int) {
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	oldPath := exePath + ".old"

	for i := 0; i < 60; i++ {
		if !instance.PIDAlive(aPID) {
			if err := os.Remove(oldPath); err != nil {
				log.Printf("Cleanup: failed to remove old binary %s: %v", oldPath, err)
			} else {
				log.Printf("Cleanup: removed old binary %s", oldPath)
			}
			return
		}
		time.Sleep(1 * time.Second)
	}
	log.Printf("Cleanup: A (PID %d) still alive after 60s; leaving %s for startup sweep", aPID, oldPath)
}

// startupSweep checks for stale old binaries and runtime state files
// left behind by a prior llamactl generation. Called on every startup.
func (im *instanceManager) startupSweep() {
	exePath, err := os.Executable()
	if err != nil {
		return
	}
	oldPath := exePath + ".old"

	if _, err := os.Stat(oldPath); os.IsNotExist(err) {
		return // No old binary to clean up.
	}

	// Check if there's a handoff state with a live PID.
	dataDir := im.globalConfig.DataDir
	phase, aPID, _ := readHandoffState(dataDir)
	if phase != "complete" && aPID > 0 && instance.PIDAlive(aPID) {
		log.Printf("Sweep: old binary found but A (PID %d) is still alive; leaving it", aPID)
		return
	}

	if err := os.Remove(oldPath); err != nil {
		log.Printf("Sweep: failed to remove stale old binary %s: %v", oldPath, err)
	} else {
		log.Printf("Sweep: removed stale old binary %s", oldPath)
	}

	// Also clean up stale runtime state files for instances whose PID is dead.
	instancesDir := filepath.Join(dataDir, "instances")
	entries, err := os.ReadDir(instancesDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		instDir := filepath.Join(instancesDir, entry.Name())
		rstate, err := instance.ReadRuntimeState(instDir)
		if err != nil || rstate == nil {
			continue
		}
		if !instance.PIDAlive(rstate.PID) {
			instance.RemoveRuntimeState(instDir)
			log.Printf("Sweep: removed stale runtime state for %s (PID %d dead)", entry.Name(), rstate.PID)
		}
	}
}
