package main

import (
	"context"
	"fmt"
	"io"
	"llamactl/pkg/config"
	"llamactl/pkg/database"
	"llamactl/pkg/manager"
	"llamactl/pkg/models"
	"llamactl/pkg/server"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

// version is set at build time using -ldflags "-X main.version=1.0.0"
var version string = "unknown"
var commitHash string = "unknown"
var buildTime string = "unknown"
var isWindows = runtime.GOOS == "windows"

// @title llamactl API
// @version 1.0
// @description llamactl is a control server for managing Llama Server instances.
// @license.name MIT License
// @license.url https://opensource.org/license/mit/
// @basePath /api/v1
// @securityDefinitions.apikey ApiKeyAuth
// @in header
// @name X-API-Key
// setupLogFile redirects os.Stdout and os.Stderr to a log file in the
// data directory, while still writing to the original stdout (tee).
// This ensures B-side (hot-swap) and wrapper-adopted processes have
// persistent logs regardless of how they were launched.
func setupLogFile(dataDir string) {
	logPath := filepath.Join(dataDir, "llamactl-server.log")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not open log file %s: %v\n", logPath, err)
		return
	}

	origStdout := os.Stdout
	tee := &teeWriter{w1: origStdout, w2: f}

	log.SetOutput(tee)
	// os.Stdout is *os.File on Windows; we can't assign a *teeWriter to it.
	// log.SetOutput(tee) covers all log.Printf calls. fmt.Println/Printf
	// calls that use os.Stdout directly are rare in this codebase.

	fmt.Fprintf(tee, "=== llamactl log started (PID %d) ===\n", os.Getpid())
}

// teeWriter writes to two writers simultaneously.
type teeWriter struct {
	w1, w2 io.Writer
}

func (t *teeWriter) Write(p []byte) (int, error) {
	_, err1 := t.w2.Write(p)
	_, err2 := t.w1.Write(p)
	if err1 != nil {
		return 0, err1
	}
	if err2 != nil {
		return 0, err2
	}
	return len(p), nil
}

func main() {

	// --version flag to print the version
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Printf("llamactl version: %s\n", version)
		fmt.Printf("Commit hash: %s\n", commitHash)
		fmt.Printf("Build time: %s\n", buildTime)
		return
	}

	configPath := os.Getenv("LLAMACTL_CONFIG_PATH")
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		log.Printf("Error loading config: %v\nUsing default configuration.", err)
	}

	// Set version information
	cfg.Version = version
	cfg.CommitHash = commitHash
	cfg.BuildTime = buildTime

	// Create data directory if it doesn't exist
	if cfg.Instances.AutoCreateDirs {
		// Create the main data directory
		if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
			log.Printf("Error creating data directory %s: %v\nData persistence may not be available.", cfg.DataDir, err)
		}

		// Create logs directory
		if err := os.MkdirAll(cfg.Instances.LogsDir, 0755); err != nil {
			log.Printf("Error creating log directory %s: %v\nInstance logs will not be available.", cfg.Instances.LogsDir, err)
		}
	}

	// Tee log file: all Go log.Printf / fmt.Println output goes here
	// and to the console. This captures A's and B's Go-level logs
	// (adoption decisions, health probes, proxy errors, swap protocol).
	setupLogFile(cfg.DataDir)

	// Initialize database
	db, err := database.Open(&database.Config{
		Path:               cfg.Database.Path,
		MaxOpenConnections: cfg.Database.MaxOpenConnections,
		MaxIdleConnections: cfg.Database.MaxIdleConnections,
		ConnMaxLifetime:    cfg.Database.ConnMaxLifetime,
	})
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}

	// Run database migrations
	if err := database.RunMigrations(db); err != nil {
		log.Fatalf("Failed to run database migrations: %v", err)
	}

	// Initialize the instance manager with dependency injection
	instanceManager := manager.New(&cfg, db)

	hotSwapExit := make(chan struct{})
	if m, ok := instanceManager.(interface{ SetHotSwapExit(chan struct{}) }); ok {
		m.SetHotSwapExit(hotSwapExit)
	}

	// Determine if this is the B-side of a hot-swap.
	isHotSwapB := false
	for _, arg := range os.Args {
		if arg == "--hot-swap" {
			isHotSwapB = true
			break
		}
	}

	// If this is the B-side of a hot-swap, receive sockets from A first.
	if isHotSwapB {
		if hs, ok := instanceManager.(interface{ HotSwapB() error }); ok {
			log.Printf("HotSwapB: entering B-side handoff mode")
			if err := hs.HotSwapB(); err != nil {
				log.Fatalf("HotSwapB failed: %v", err)
			}
			log.Printf("HotSwapB: handoff complete, continuing as server")
		}
	}

	// Initialize model manager
	modelManager := models.NewManager(cfg.Backends.LlamaCpp.CacheDir, cfg.Backends.LlamaCpp.DownloadTimeout, cfg.Version)

	// Create a new handler with the instance manager
	handler := server.NewHandler(instanceManager, modelManager, cfg, db)

	// Setup the router with the handler
	r := server.SetupRouter(handler)

	// Handle graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	// Create the HTTP server.
	httpServer := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler: r,
	}

	// Bind the listener.
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	var ln net.Listener
	var lnErr error

	if isHotSwapB {
		log.Printf("HotSwapB: waiting for A to release port %d...", cfg.Server.Port)
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			var err error
			ln, err = net.Listen("tcp", addr)
			if err == nil {
				log.Printf("HotSwapB: bound to %s", addr)
				break
			}
			log.Printf("HotSwapB: port %d not ready yet: %v", cfg.Server.Port, err)
			time.Sleep(500 * time.Millisecond)
		}
		if ln == nil {
			log.Fatalf("HotSwapB: failed to bind port %d after 30s", cfg.Server.Port)
		}
		if m, ok := instanceManager.(interface{ AdoptedConns() []net.Conn }); ok {
			if conns := m.AdoptedConns(); len(conns) > 0 {
				log.Printf("HotSwapB: injecting %d adopted client connections", len(conns))
				ln = server.NewChainedListener(ln, conns)
			}
		}
	} else {
		ln, lnErr = net.Listen("tcp", addr)
		if lnErr != nil {
			log.Fatalf("Failed to listen on %s: %v", addr, lnErr)
		}
		fmt.Printf("Llamactl server listening on %s\n", addr)
	}

	// Track accepted connections so a later hot-swap can duplicate them.
	if isWindows {
		tl := server.NewTrackingListener(ln)
		if m, ok := instanceManager.(interface {
			SetConnectionTracker(interface{ ActiveConns() []net.Conn })
		}); ok {
			m.SetConnectionTracker(tl)
		}
		ln = tl
		log.Printf("HotSwapB: listener chain ready (TrackingListener wrapping %T)", tl)
	} else {
		log.Printf("HotSwapB: no TrackingListener (not Windows)")
	}

	log.Printf("HotSwapB: starting Serve() on %s (listener type: %T)", ln.Addr(), ln)

	// Start serving.
	go func() {
		log.Printf("HotSwapB: Serve() goroutine started")
		err := httpServer.Serve(ln)
		log.Printf("HotSwapB: Serve() returned: err=%v", err)
		if err != nil && err != http.ErrServerClosed {
			log.Printf("Error serving: %v\n", err)
			select {
			case <-hotSwapExit:
			default:
				close(hotSwapExit)
			}
		}
	}()

	select {
	case <-hotSwapExit:
		// Successor process owns the port and model children. Do not
		// Shutdown() instances — that would kill llama-server.
		log.Printf("Hot-swap complete; exiting without stopping model instances")
		modelManager.Close()
		if err := db.Close(); err != nil {
			log.Printf("Error closing database: %v\n", err)
		}
		fmt.Println("Exiting llamactl (hot-swap).")
		return
	case <-stop:
		fmt.Println("Shutting down server...")
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("Error shutting down server: %v\n", err)
	} else {
		fmt.Println("Server shut down gracefully.")
	}

	instanceManager.Shutdown()

	modelManager.Close()

	if err := db.Close(); err != nil {
		log.Printf("Error closing database: %v\n", err)
	}

	fmt.Println("Exiting llamactl.")
}
