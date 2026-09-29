//go:build windows

package instance

import (
	"fmt"
	"log"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32Stop  = syscall.NewLazyDLL("kernel32.dll")
	procOpenProc  = kernel32Stop.NewProc("OpenProcess")
	procTerminate = kernel32Stop.NewProc("TerminateProcess")
	procCloseHnd  = kernel32Stop.NewProc("CloseHandle")
)

const (
	processTerminate = 0x0001 // PROCESS_TERMINATE
)

// stopProcessByPID terminates the process with the given PID.
// On Windows, this uses TerminateProcess (equivalent to a hard kill).
// The llama-server backend handles its own cleanup on exit.
func stopProcessByPID(pid int) error {
	if err := terminatePID(pid); err != nil {
		return err
	}
	// Give the process a moment to release its port.
	time.Sleep(500 * time.Millisecond)
	return nil
}

// terminatePID issues TerminateProcess with no post-sleep.
func terminatePID(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid PID: %d", pid)
	}

	handle, _, err := procOpenProc.Call(
		uintptr(processTerminate),
		0, // bInheritHandle = FALSE
		uintptr(pid),
	)
	if handle == 0 {
		return fmt.Errorf("OpenProcess(%d) failed: %w", pid, err)
	}
	defer procCloseHnd.Call(handle)

	r, _, callErr := procTerminate.Call(handle, 0 /* exit code */)
	if r == 0 {
		return fmt.Errorf("TerminateProcess(%d) failed: %v", pid, callErr)
	}
	return nil
}

// descendantPIDs returns rootPID plus all of its descendants (Toolhelp snapshot).
// Order is BFS: root first, then children, then grandchildren, …
func descendantPIDs(rootPID int) ([]int, error) {
	if rootPID <= 0 {
		return nil, fmt.Errorf("invalid root PID: %d", rootPID)
	}

	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("CreateToolhelp32Snapshot: %w", err)
	}
	defer windows.CloseHandle(snap)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snap, &entry); err != nil {
		return nil, fmt.Errorf("Process32First: %w", err)
	}

	// parentPID -> child PIDs
	children := make(map[uint32][]uint32)
	for {
		ppid := entry.ParentProcessID
		pid := entry.ProcessID
		if pid != 0 {
			children[ppid] = append(children[ppid], pid)
		}
		if err := windows.Process32Next(snap, &entry); err != nil {
			break
		}
	}

	seen := map[int]bool{rootPID: true}
	out := []int{rootPID}
	queue := []uint32{uint32(rootPID)}
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, child := range children[parent] {
			c := int(child)
			if seen[c] {
				continue
			}
			seen[c] = true
			out = append(out, c)
			queue = append(queue, child)
		}
	}
	return out, nil
}

// stopProcessTreeByPID TerminateProcess-es every process in the tree rooted at
// rootPID. Children are killed before parents (reverse BFS) to reduce races
// where a dying parent is respawned by a still-alive supervisor child.
func stopProcessTreeByPID(rootPID int) (int, error) {
	pids, err := descendantPIDs(rootPID)
	if err != nil {
		// Fall back to root-only if enumeration fails.
		if terr := terminatePID(rootPID); terr != nil {
			return 0, fmt.Errorf("tree enum failed (%v) and root kill failed: %w", err, terr)
		}
		return 1, nil
	}
	killed := 0
	for i := len(pids) - 1; i >= 0; i-- {
		if terr := terminatePID(pids[i]); terr != nil {
			// Process may already be gone; keep going.
			continue
		}
		killed++
	}
	time.Sleep(500 * time.Millisecond)
	return killed, nil
}

// stopAdoptedProcessTree stops an adopted Windows process tree.
//
// Preference order:
//  1. Open the named Job Object created at spawn and TerminateJobObject
//     (kills the whole tree in one shot — preferred after hot-swap).
//  2. Create a fresh job, AssignProcessToJobObject every descendant that is
//     not already in another job, then TerminateJobObject; TerminateProcess
//     any leftovers.
//  3. Enumerate the PID tree and TerminateProcess each (fallback for anonymous
//     jobs from older binaries that cannot be reopened by name).
//
// Returns a short method tag for logging: "job", "job-assign", or "tree-kill".
func stopAdoptedProcessTree(instanceName string, rootPID int) (method string, err error) {
	if rootPID <= 0 {
		return "", fmt.Errorf("invalid PID: %d", rootPID)
	}

	// 1) Preferred: reopen named job from prior spawn / hot-swap generation.
	if instanceName != "" {
		if job, oerr := openProcessJob(instanceName); oerr == nil {
			kerr := job.killTree()
			job.close()
			if kerr == nil {
				time.Sleep(500 * time.Millisecond)
				return "job", nil
			}
			log.Printf("Instance %s (adopted): TerminateJobObject on named job failed: %v; trying assign/tree fallback", instanceName, kerr)
		}
	}

	// 2) Try assign-into-new-job (works when processes are not already in a job).
	pids, enumErr := descendantPIDs(rootPID)
	if enumErr != nil {
		log.Printf("Instance %s (adopted): descendant enum failed: %v; falling back to root TerminateProcess", instanceName, enumErr)
		if terr := stopProcessByPID(rootPID); terr != nil {
			return "tree-kill", terr
		}
		return "tree-kill", nil
	}

	if job, jerr := newProcessJob(""); jerr == nil && job != nil {
		assigned := 0
		var unassigned []int
		for _, pid := range pids {
			if aerr := job.assignPID(pid); aerr != nil {
				unassigned = append(unassigned, pid)
				continue
			}
			assigned++
		}
		if assigned > 0 {
			if kerr := job.killTree(); kerr != nil {
				log.Printf("Instance %s (adopted): TerminateJobObject after assign failed: %v", instanceName, kerr)
			} else {
				method = "job-assign"
			}
		}
		job.close()
		// Kill any PIDs that could not join the new job (already in another job).
		for i := len(unassigned) - 1; i >= 0; i-- {
			_ = terminatePID(unassigned[i])
		}
		if method == "job-assign" {
			if len(unassigned) > 0 {
				log.Printf("Instance %s (adopted): job-assign killed %d; TerminateProcess fallback for %d already-jobbed PIDs", instanceName, assigned, len(unassigned))
			}
			time.Sleep(500 * time.Millisecond)
			return "job-assign", nil
		}
	}

	// 3) Full tree TerminateProcess fallback.
	n, terr := stopProcessTreeByPID(rootPID)
	if terr != nil {
		return "tree-kill", terr
	}
	log.Printf("Instance %s (adopted): tree-kill terminated %d process(es) under PID %d", instanceName, n, rootPID)
	return "tree-kill", nil
}
