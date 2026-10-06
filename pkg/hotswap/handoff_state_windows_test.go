//go:build windows

package hotswap

import (
	"os"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// A (old process) writes handoff-state.json while B (new process) reads it.
// handoffMu cannot serialize across processes, so the rename that replaces
// the file must succeed while another handle has it open — live swaps
// failed here with "Access is denied", leaving phase stuck at in_progress.

func TestWriteHandoffState_WhileFileOpenByReader(t *testing.T) {
	dir := t.TempDir()
	if err := WriteHandoffState(dir, &HandoffState{Phase: PhaseInProgress}); err != nil {
		t.Fatal(err)
	}

	// Stand-in for another process holding the file open the way Go's
	// os.Open / os.ReadFile does.
	f, err := os.Open(HandoffStatePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	// Release after a moment, as a reader in B would.
	go func() {
		time.Sleep(300 * time.Millisecond)
		f.Close()
	}()

	if err := WriteHandoffState(dir, &HandoffState{Phase: PhaseComplete}); err != nil {
		t.Fatalf("WriteHandoffState with a concurrent reader: %v", err)
	}
	got, err := ReadHandoffState(dir)
	if err != nil || got == nil || got.Phase != PhaseComplete {
		t.Fatalf("after write: %+v, %v; want phase complete", got, err)
	}
}

// A reader that opens the file the way readHandoffStateFile does must not
// block the replace at all (POSIX rename semantics), even if it never lets go.
func TestWriteHandoffState_SharedReaderNeverBlocks(t *testing.T) {
	dir := t.TempDir()
	if err := WriteHandoffState(dir, &HandoffState{Phase: PhaseInProgress}); err != nil {
		t.Fatal(err)
	}
	p, _ := windows.UTF16PtrFromString(HandoffStatePath(dir))
	h, err := windows.CreateFile(p, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)

	start := time.Now()
	if err := WriteHandoffState(dir, &HandoffState{Phase: PhaseComplete}); err != nil {
		t.Fatalf("WriteHandoffState with a shared reader open: %v", err)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("write took %v; shared readers should not need the retry path", d)
	}
	if got, _ := ReadHandoffState(dir); got == nil || got.Phase != PhaseComplete {
		t.Fatalf("after write: %+v; want phase complete", got)
	}
}

func TestWriteHandoffState_ConcurrentReadsAndWrites(t *testing.T) {
	dir := t.TempDir()
	if err := WriteHandoffState(dir, &HandoffState{Phase: PhaseStarting}); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var readErrs int
	var mu sync.Mutex
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				// Bypass handoffMu, like a reader in another process.
				if _, err := readHandoffStateFile(HandoffStatePath(dir)); err != nil && !os.IsNotExist(err) {
					mu.Lock()
					readErrs++
					mu.Unlock()
				}
			}
		}()
	}

	for i := 0; i < 200; i++ {
		if err := WriteHandoffState(dir, &HandoffState{Phase: PhaseInProgress, SocketsHandedOff: i}); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("write %d failed with concurrent readers: %v", i, err)
		}
	}
	close(stop)
	wg.Wait()
	if readErrs > 0 {
		t.Fatalf("%d reads failed during concurrent writes", readErrs)
	}
}
