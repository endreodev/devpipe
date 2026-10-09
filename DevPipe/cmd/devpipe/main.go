// DevPipe — Esteira inteligente de desenvolvimento com IA.
// Executável único: servidor HTTP local (127.0.0.1) + interface web embutida.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"devpipe/internal/gitx"
	"devpipe/internal/pipeline"
	"devpipe/internal/provider"
	"devpipe/internal/security"
	"devpipe/internal/store"
	"devpipe/internal/web"
)

func defaultDataDir() string {
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "DevPipe")
	}
	return "devpipe-data"
}

func main() {
	port := flag.Int("port", 7420, "porta HTTP local")
	dataDir := flag.String("data", defaultDataDir(), "pasta de dados (banco, anexos, worktrees)")
	noOpen := flag.Bool("no-browser", false, "não abrir o navegador automaticamente")
	showVersion := flag.Bool("version", false, "exibir versão")
	flag.Parse()
	if *showVersion {
		fmt.Println("DevPipe", web.Version)
		return
	}

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("não foi possível criar a pasta de dados: %v", err)
	}
	lf, err := os.OpenFile(filepath.Join(*dataDir, "devpipe.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		log.SetOutput(io.MultiWriter(os.Stderr, lf))
		defer lf.Close()
	}
	gitx.Log = func(cmd, out string, err error) {
		if err != nil {
			log.Printf("[git] %s -> %s", cmd, security.Redact(err.Error()))
		}
	}

	st, err := store.Open(filepath.Join(*dataDir, "devpipe.db"))
	if err != nil {
		log.Fatalf("banco de dados: %v", err)
	}
	defer st.Close()

	settings, _ := st.GetSettings()
	reg := provider.NewRegistry()
	web.ConfigureProviders(reg, settings)

	bus := pipeline.NewBus()
	eng := pipeline.New(st, reg, bus, *dataDir)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	eng.Start(ctx)
	eng.SystemLog("info", "sistema", "DevPipe "+web.Version+" iniciado")

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("porta %d indisponível (já existe outra instância?): %v", *port, err)
	}
	srv := &http.Server{Handler: web.New(st, eng, reg, *dataDir, *port).Handler(), ReadHeaderTimeout: 10 * time.Second}
	url := fmt.Sprintf("http://localhost:%d", *port)
	log.Printf("DevPipe %s em %s  (dados: %s)", web.Version, url, *dataDir)
	if !*noOpen {
		go openBrowser(url)
	}
	go func() {
		<-ctx.Done()
		log.Printf("encerrando…")
		sctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		srv.Shutdown(sctx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond) // permite que execuções canceladas registrem o estado
}

func openBrowser(url string) {
	time.Sleep(400 * time.Millisecond)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
