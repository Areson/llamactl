// Command graceful-drain demonstrates a reconnect-safe Windows HTTP restart.
//
// It deliberately uses a close-and-rebind handoff, rather than listener
// duplication: Go cannot yet reliably wrap an adopted Windows listener as a
// net.Listener. Existing HTTP connections are drained by A; new clients may
// see a short ECONNREFUSED window and retry against B.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var (
	role       = flag.String("role", "", "a (old server), b (new server), or demo")
	port       = flag.Int("port", 18080, "public HTTP port")
	control    = flag.Int("control-port", 0, "private A/B control port (B only)")
	nonce      = flag.String("nonce", "", "one-time A/B control secret (B only)")
	drainAfter = flag.Duration("drain-after", 3*time.Second, "when A begins drain")
)

type controlSession struct {
	r *bufio.Reader
	w io.Writer
}

func (s controlSession) send(line string) error { _, err := fmt.Fprintln(s.w, line); return err }
func (s controlSession) recv() (string, error)  { return s.r.ReadString('\n') }

type trackedHandler struct {
	draining atomic.Bool
	active   sync.WaitGroup
	name     string
}

func (h *trackedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.active.Add(1)
	defer h.active.Done()
	if h.draining.Load() {
		w.Header().Set("Connection", "close")
		w.Header().Set("Retry-After", "1")
		http.Error(w, "server is restarting; retry", http.StatusServiceUnavailable)
		return
	}
	if r.URL.Path == "/healthz" {
		_, _ = fmt.Fprintf(w, "ok from %s\n", h.name)
		return
	}
	if r.URL.Path != "/work" {
		http.NotFound(w, r)
		return
	}
	ms, _ := strconv.Atoi(r.URL.Query().Get("ms"))
	if ms < 0 || ms > 30000 {
		http.Error(w, "ms must be 0..30000", http.StatusBadRequest)
		return
	}
	time.Sleep(time.Duration(ms) * time.Millisecond)
	_, _ = fmt.Fprintf(w, "served by %s pid=%d\n", h.name, os.Getpid())
}

func main() {
	flag.Parse()
	switch *role {
	case "a":
		runA()
	case "b":
		runB()
	case "demo":
		runDemo()
	default:
		log.Fatal("use --role=a, --role=b, or --role=demo")
	}
}

func runA() {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		log.Fatal(err)
	}
	h := &trackedHandler{name: "A"}
	srv := &http.Server{Handler: h}
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(ln) }()

	ctlLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	defer ctlLn.Close()
	secret := randomHex(24)
	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	child := exec.Command(exe, "--role=b", fmt.Sprintf("--port=%d", *port), fmt.Sprintf("--control-port=%d", ctlLn.Addr().(*net.TCPAddr).Port), "--nonce="+secret)
	child.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000200}
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if err := child.Start(); err != nil {
		log.Fatal(err)
	}
	log.Printf("A: serving http://127.0.0.1:%d; B pid=%d is preparing", *port, child.Process.Pid)

	ctl, err := ctlLn.Accept()
	if err != nil {
		log.Fatal(err)
	}
	defer ctl.Close()
	s := controlSession{bufio.NewReader(ctl), ctl}
	line, err := s.recv()
	if err != nil || strings.TrimSpace(line) != "READY "+secret {
		log.Fatalf("A: invalid B readiness: %q %v", line, err)
	}
	log.Printf("A: B control plane ready; draining in %s", *drainAfter)
	time.Sleep(*drainAfter)

	// Stop accepting first. Shutdown then closes idle keep-alives and waits for
	// requests already admitted by A, while B can bind the released port.
	h.draining.Store(true)
	srv.SetKeepAlivesEnabled(false)
	_ = ln.Close()
	if err := <-serveDone; err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("A: Serve: %v", err)
	}
	if err := s.send("RELEASE " + secret); err != nil {
		log.Fatal(err)
	}
	line, err = s.recv()
	if err != nil || strings.TrimSpace(line) != "SERVING "+secret {
		log.Fatalf("A: B did not bind: %q %v", line, err)
	}
	log.Printf("A: B owns port %d; finishing bounded drain", *port)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("A: drain deadline: %v; forcing close", err)
		_ = srv.Close()
	}
	log.Printf("A: drained; exiting")
}

func runB() {
	if *control == 0 || *nonce == "" {
		log.Fatal("B needs --control-port and --nonce")
	}
	ctl, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", *control), 10*time.Second)
	if err != nil {
		log.Fatal(err)
	}
	defer ctl.Close()
	s := controlSession{bufio.NewReader(ctl), ctl}
	if err := s.send("READY " + *nonce); err != nil {
		log.Fatal(err)
	}
	line, err := s.recv()
	if err != nil || strings.TrimSpace(line) != "RELEASE "+*nonce {
		log.Fatalf("B: invalid release: %q %v", line, err)
	}

	var ln net.Listener
	deadline := time.Now().Add(5 * time.Second)
	for {
		ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			log.Fatalf("B: rebind timed out: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := s.send("SERVING " + *nonce); err != nil {
		log.Fatal(err)
	}
	log.Printf("B: serving http://127.0.0.1:%d", *port)
	if err := (&http.Server{Handler: &trackedHandler{name: "B"}}).Serve(ln); err != nil {
		log.Printf("B: Serve ended: %v", err)
	}
}

func runDemo() {
	// Demo proves an admitted A request completes, then a retrying client lands on B.
	go runA()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", *port))
		if err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			log.Fatalf("demo: A never became healthy: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/work?ms=1000", *port))
	if err != nil {
		log.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	log.Printf("demo: in-flight result: %s", strings.TrimSpace(string(body)))
	deadline = time.Now().Add(12 * time.Second)
	for {
		resp, err = client.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", *port))
		if err == nil {
			body, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
			if strings.Contains(string(body), "B") {
				log.Printf("demo: PASS — retry reached B: %s", strings.TrimSpace(string(body)))
				return
			}
		}
		if time.Now().After(deadline) {
			log.Fatalf("demo: B never became healthy: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
