package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"time"

	"bandwidthpilot/internal/core"
	"bandwidthpilot/internal/ui"
)

// serveHTTP runs the interface on 127.0.0.1 for a browser. It is the shell
// used on systems without a native window yet, and for development.
//
// Only this machine can connect, the Host header is checked (so a hostile web
// page cannot reach it through DNS tricks), and every RPC needs a token that
// only the served page knows.
func serveHTTP(sv *core.Service, o options) error {
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(o.port))
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	hosts := map[string]bool{
		fmt.Sprintf("127.0.0.1:%d", port): true,
		fmt.Sprintf("localhost:%d", port): true,
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	token := hex.EncodeToString(raw)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !hosts[r.Host] || r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, ui.HTML("window.__PILOT_TOKEN="+strconv.Quote(token)+";"))
	})
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		if !hosts[r.Host] || r.Method != http.MethodPost ||
			subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Pilot-Token")), []byte(token)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		res, err := sv.Call(req.Method, req.Params)
		if err != nil {
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": res})
	})

	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	fmt.Println("Bandwidth Pilot is running at", url, "(Ctrl+C to quit)")
	if !o.noOpen {
		openBrowser(url)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		<-sig
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func openBrowser(url string) {
	switch runtime.GOOS {
	case "windows":
		exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		exec.Command("open", url).Start()
	default:
		exec.Command("xdg-open", url).Start()
	}
}
