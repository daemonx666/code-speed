// devui serves the panel to a browser, for working on its page without
// rebuilding the app: go run -tags nogui ./build/devui, then open
// http://127.0.0.1:47811. The page is read from disk, so a reload shows
// an edit. It uses your real logs, settings and history.
package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/daemonx666/code-speed/internal/gui"
	"github.com/daemonx666/code-speed/internal/service"
	_ "github.com/daemonx666/code-speed/internal/sources"
)

func main() {
	addr := "127.0.0.1:47811"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}
	svc := service.New("dev")
	svc.Start()
	api := gui.Handler(svc, func() { log.Println("quit asked") }, func(l string) { log.Println("page language:", l) })
	mux := http.NewServeMux()
	mux.Handle("/api/", api)
	mux.Handle("/", http.FileServer(http.Dir("internal/gui/assets")))
	log.Printf("panel at http://%s", addr)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
