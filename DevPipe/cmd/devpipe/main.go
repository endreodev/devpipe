// DevPipe — Esteira inteligente de desenvolvimento com IA.
// Executável único: servidor HTTP local (127.0.0.1) + interface web embutida.
package main

import (
	"context"
	"encoding/json"
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

	"devpipe/internal/encx"
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
	if len(os.Args) > 1 && os.Args[1] == "hook-encoding" {
		hookEncoding(os.Args[2:])
		return
	}
	port := flag.Int("port", 7420, "porta HTTP local")
	dataDir := flag.String("data", defaultDataDir(), "pasta de dados (banco, anexos, worktrees)")
	noOpen := flag.Bool("no-browser", false, "não abrir o navegador automaticamente")
	showVersion := flag.Bool("version", false, "exibir versão")
	flag.Parse()
	if *showVersion {
		fmt.Println("DevPipe", web.Version)
		return
	}

	// Porta primeiro: se outra instância estiver aberta, não tocamos no banco.
	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if v := runningVersion(*port); v != "" {
			msg := fmt.Sprintf("Já existe um DevPipe %s em execução na porta %d. Feche-o antes de abrir esta versão (%s): Gerenciador de Tarefas > devpipe.exe > Finalizar tarefa.", v, *port, web.Version)
			if v == web.Version {
				msg = fmt.Sprintf("O DevPipe %s já está em execução em http://localhost:%d", v, *port)
				if !*noOpen {
					openBrowser(fmt.Sprintf("http://localhost:%d", *port))
				}
			}
			log.Println(msg)
			waitKey()
			os.Exit(1)
		}
		log.Printf("porta %d indisponível: %v", *port, err)
		waitKey()
		os.Exit(1)
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

// runningVersion pergunta a uma instância já aberta qual é a sua versão.
func runningVersion(port int) string {
	c := http.Client{Timeout: 2 * time.Second}
	r, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/api/meta", port))
	if err != nil {
		return ""
	}
	defer r.Body.Close()
	var m struct{ Version string }
	if json.NewDecoder(r.Body).Decode(&m) != nil {
		return ""
	}
	return m.Version
}

// waitKey mantém a janela do console aberta no Windows para a mensagem ser lida.
func waitKey() {
	if runtime.GOOS == "windows" {
		fmt.Println("\nPressione Enter para fechar...")
		fmt.Scanln()
	}
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

// hookEncoding é executado pelo Claude Code (hook PreToolUse) antes de cada
// leitura/edição: converte para UTF-8 apenas o arquivo que o agente vai usar.
// Nunca bloqueia o agente: em caso de erro, a reparação posterior continua valendo.
func hookEncoding(args []string) {
	fs := flag.NewFlagSet("hook-encoding", flag.ContinueOnError)
	root := fs.String("root", "", "")
	manifest := fs.String("manifest", "", "")
	if fs.Parse(args) != nil || *root == "" || *manifest == "" {
		return
	}
	var in struct {
		ToolInput map[string]any `json:"tool_input"`
	}
	if json.NewDecoder(io.LimitReader(os.Stdin, 4<<20)).Decode(&in) != nil {
		return
	}
	for _, k := range []string{"file_path", "notebook_path"} {
		if p, ok := in.ToolInput[k].(string); ok && p != "" {
			if err := encx.HookConvert(*root, *manifest, p); err != nil {
				fmt.Fprintln(os.Stderr, "devpipe hook-encoding:", err)
			}
		}
	}
}
