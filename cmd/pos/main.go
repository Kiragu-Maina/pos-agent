// Command pos is the local agent. It serves the POS web UI on localhost and
// opens the default browser at it. Running locally means the app works with no
// internet, which is the default condition we design for.
//
// A few environment variables let the same binary run inside a container
// without changing its native default behaviour:
//
//	POS_ADDR        listen address (default 127.0.0.1:7777, loopback only)
//	POS_DATA        database file path (default data/pos.db)
//	POS_NO_BROWSER  when set, do not try to open a browser
package main

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"pos-system/internal/cloudsync"
	"pos-system/internal/store/boltstore"
	"pos-system/internal/web"
)

// syncInterval is how often the agent reconciles with the cloud when linked.
// Each tick is a no-op while unlinked, and a device that was offline catches up
// on the next tick after it reconnects.
const syncInterval = 30 * time.Second

// defaultCloudURL is the hosted cloud the agent links to. Baked in so a shop
// owner never types an address; POS_CLOUD overrides it for dev/self-hosting.
const defaultCloudURL = "https://pos.alkenacode.dev"

// defaultAddr binds to loopback only. On a normal install the UI and API are
// never exposed to the network; only this machine's browser talks to the agent.
// A container overrides this with POS_ADDR so the port can be published.
const defaultAddr = "127.0.0.1:7777"

func main() {
	setupLogFile()

	addr := env("POS_ADDR", defaultAddr)
	dbPath := env("POS_DATA", defaultDataPath())
	log.Printf("startup: data file %s", dbPath)

	if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Fatalf("startup: cannot create data directory: %v", err)
		}
	}
	db, err := boltstore.Open(dbPath)
	if err != nil {
		log.Fatalf("startup: cannot open database: %v", err)
	}
	defer db.Close()
	if err := db.SeedIfEmpty(); err != nil {
		log.Fatalf("startup: cannot seed catalogue: %v", err)
	}

	// Cloud sync is optional: the agent is fully usable offline and standalone.
	// The controller links the device to a shop, reconciles in the background,
	// and is driven from the Setup screen. The cloud address is baked in so the
	// shop owner only ever signs in with their email and password; POS_CLOUD can
	// override it for development or self-hosting.
	sync := cloudsync.NewController(db)
	syncCtx, stopSync := context.WithCancel(context.Background())
	defer stopSync()
	go sync.Run(syncCtx, syncInterval)

	srv, err := web.New(db, sync, env("POS_CLOUD", defaultCloudURL))
	if err != nil {
		log.Fatalf("startup: %v", err)
	}

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	url := "http://" + addr + "/"

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// An instance is most likely already running on this port. Treat a second
		// launch (clicking the shortcut or taskbar icon again) as "open the POS"
		// and hand off to the running instance instead of failing silently.
		if errors.Is(err, syscall.EADDRINUSE) {
			log.Printf("already running; opening %s", url)
			if os.Getenv("POS_NO_BROWSER") == "" {
				openBrowser(url)
			}
			return
		}
		log.Fatalf("cannot bind %s: %v", addr, err)
	}

	log.Printf("POS agent ready at %s", url)
	if os.Getenv("POS_NO_BROWSER") == "" {
		go openBrowser(url)
	}

	if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("serve: %v", err)
	}
}

// env reads an environment variable, falling back to def when it is unset or
// empty.
func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// defaultDataPath returns a per-user, always-writable location for the database,
// so a double-clicked exe never depends on its (possibly read-only) folder. On
// Windows this is %AppData%\AlkenaCode POS\pos.db. Falls back to a relative path
// only if the user config dir cannot be determined.
func defaultDataPath() string {
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return filepath.Join(dir, "AlkenaCode POS", "pos.db")
	}
	return filepath.Join("data", "pos.db")
}

// setupLogFile mirrors the log to a file in the temp dir. The GUI build has no
// console, so without this a startup failure is invisible; the file lets us see
// exactly why the agent did not open.
func setupLogFile() {
	path := filepath.Join(os.TempDir(), "alkenacode-pos.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	log.SetOutput(io.MultiWriter(os.Stderr, f))
	log.Printf("==== AlkenaCode POS starting (pid %d) ====", os.Getpid())
}

// openBrowser opens the system default browser at url. Failure is non-fatal;
// the user can always open the address themselves.
func openBrowser(url string) {
	// Give the listener a moment so the page loads on first try.
	time.Sleep(300 * time.Millisecond)

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("could not open browser automatically: %v", err)
	}
}
