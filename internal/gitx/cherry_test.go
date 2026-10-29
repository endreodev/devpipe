package gitx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Conflito entre a origem (stage/release) e o desenvolvimento não pode
// descartar nenhum dos lados nem deixar marcação dentro do arquivo: a branch
// de teste tem de sair com o código da origem E com as alterações da demanda.
func TestCherryPickUniaoMantemOsDoisLados(t *testing.T) {
	_, clone, g := cenario(t)
	arq := filepath.Join(clone, "a.txt")

	// a origem (stage) evolui a mesma linha que a demanda vai mexer
	g(clone, "checkout", "-q", "-b", "stage")
	os.WriteFile(arq, []byte("1\nlinha da release\n"), 0o644)
	g(clone, "commit", "-qam", "stage: ajuste da release")

	// a demanda sai de main e mexe na mesma linha
	g(clone, "checkout", "-q", "-b", "BUG/CH101010_Desenvolvimento", "main")
	base := g(clone, "rev-parse", "HEAD")
	os.WriteFile(arq, []byte("1\nlinha da demanda\n"), 0o644)
	g(clone, "commit", "-qam", "CH101010: ajuste da demanda")
	dev := g(clone, "rev-parse", "HEAD")

	// a branch de teste nasce de stage, em worktree própria
	wt := filepath.Join(t.TempDir(), "teste")
	if _, err := (Repo{Dir: clone}).EnsureWorktree(wt, "BUG/CH101010_stage", "stage"); err != nil {
		t.Fatal(err)
	}
	res, err := (Repo{Dir: wt}).CherryPickUnion(base, dev, CherryOpts{})
	if err != nil {
		t.Fatalf("cherry-pick: %v", err)
	}
	if len(res.Picked) != 1 || len(res.Unified) != 1 || res.Unified[0] != "a.txt" {
		t.Fatalf("resultado inesperado: %+v", res)
	}
	b, _ := os.ReadFile(filepath.Join(wt, "a.txt"))
	got := string(b)
	for _, quer := range []string{"linha da release", "linha da demanda"} {
		if !strings.Contains(got, quer) {
			t.Fatalf("o arquivo perdeu %q: %q", quer, got)
		}
	}
	if strings.Contains(got, "<<<<<<<") || strings.Contains(got, ">>>>>>>") {
		t.Fatalf("a união não pode deixar marcação de conflito: %q", got)
	}
	// o resultado ficou commitado na branch de teste, com a mensagem original
	if sujo := (Repo{Dir: wt}).ChangedFiles(); len(sujo) > 0 {
		t.Fatalf("sobrou alteração pendente: %v", sujo)
	}
	if msg := g(wt, "log", "-1", "--format=%s"); msg != "CH101010: ajuste da demanda" {
		t.Fatalf("mensagem do commit: %q", msg)
	}
	// a branch da origem não foi tocada
	if s := g(clone, "show", "stage:a.txt"); !strings.Contains(s, "linha da release") || strings.Contains(s, "linha da demanda") {
		t.Fatalf("a branch de origem foi alterada: %q", s)
	}
}

// Sem conflito, os commits são transplantados um a um, na ordem, mantendo as
// mensagens. Arquivo que só existe na demanda entra inteiro.
func TestCherryPickVariosCommitsSemConflito(t *testing.T) {
	_, clone, g := cenario(t)
	g(clone, "checkout", "-q", "-b", "release")
	os.WriteFile(filepath.Join(clone, "r.txt"), []byte("release\n"), 0o644)
	g(clone, "add", ".")
	g(clone, "commit", "-qm", "release: outro arquivo")

	g(clone, "checkout", "-q", "-b", "NR/CH9_Desenvolvimento", "main")
	base := g(clone, "rev-parse", "HEAD")
	for _, n := range []string{"um", "dois"} {
		os.WriteFile(filepath.Join(clone, n+".txt"), []byte(n+"\n"), 0o644)
		g(clone, "add", ".")
		g(clone, "commit", "-qm", "CH9: "+n)
	}
	dev := g(clone, "rev-parse", "HEAD")

	wt := filepath.Join(t.TempDir(), "teste")
	if _, err := (Repo{Dir: clone}).EnsureWorktree(wt, "NR/CH9_release", "release"); err != nil {
		t.Fatal(err)
	}
	res, err := (Repo{Dir: wt}).CherryPickUnion(base, dev, CherryOpts{})
	if err != nil {
		t.Fatalf("cherry-pick: %v", err)
	}
	if len(res.Picked) != 2 || res.Total() != 0 {
		t.Fatalf("resultado inesperado: %+v", res)
	}
	for _, n := range []string{"um.txt", "dois.txt", "r.txt"} {
		if _, err := os.Stat(filepath.Join(wt, n)); err != nil {
			t.Fatalf("faltou %s na branch de teste: %v", n, err)
		}
	}
	if msgs := g(wt, "log", "-2", "--format=%s"); msgs != "CH9: dois\nCH9: um" {
		t.Fatalf("ordem/mensagens dos commits: %q", msgs)
	}
}

// Arquivo que a demanda apagou e a origem alterou: a esteira mantém o da
// origem (nunca descarta código alheio) e registra para conferência.
func TestCherryPickMantemArquivoApagadoPelaDemanda(t *testing.T) {
	_, clone, g := cenario(t)
	g(clone, "checkout", "-q", "-b", "stage")
	os.WriteFile(filepath.Join(clone, "a.txt"), []byte("1\nda release\n"), 0o644)
	g(clone, "commit", "-qam", "stage: ajuste")

	g(clone, "checkout", "-q", "-b", "BUG/CH5_Desenvolvimento", "main")
	base := g(clone, "rev-parse", "HEAD")
	g(clone, "rm", "-q", "a.txt")
	g(clone, "commit", "-qm", "CH5: remove arquivo")
	dev := g(clone, "rev-parse", "HEAD")

	wt := filepath.Join(t.TempDir(), "teste")
	if _, err := (Repo{Dir: clone}).EnsureWorktree(wt, "BUG/CH5_stage", "stage"); err != nil {
		t.Fatal(err)
	}
	res, err := (Repo{Dir: wt}).CherryPickUnion(base, dev, CherryOpts{})
	if err != nil {
		t.Fatalf("cherry-pick: %v", err)
	}
	if len(res.FromBase) != 1 || res.FromBase[0] != "a.txt" {
		t.Fatalf("esperava o arquivo mantido como na origem: %+v", res)
	}
	b, err := os.ReadFile(filepath.Join(wt, "a.txt"))
	if err != nil || !strings.Contains(string(b), "da release") {
		t.Fatalf("o arquivo da origem foi perdido: %v %q", err, b)
	}
}

// Squash: os commits da demanda viram UM commit na branch de teste, e a
// união dos dois lados continua valendo em cada passo.
func TestCherryPickSquashReuneTudoEmUmCommit(t *testing.T) {
	_, clone, g := cenario(t)
	arq := filepath.Join(clone, "a.txt")
	os.WriteFile(arq, []byte("1\n2\n3\n"), 0o644)
	g(clone, "commit", "-qam", "conteudo inicial")

	g(clone, "checkout", "-q", "-b", "stage")
	os.WriteFile(arq, []byte("1\nda release\n3\n"), 0o644)
	g(clone, "commit", "-qam", "stage: ajuste")

	g(clone, "checkout", "-q", "-b", "BUG/CH42_Desenvolvimento", "main")
	base := g(clone, "rev-parse", "HEAD")
	os.WriteFile(arq, []byte("1\nda demanda\n3\n"), 0o644)
	g(clone, "commit", "-qam", "CH42: primeiro")
	os.WriteFile(arq, []byte("1\nda demanda\n3\nsegunda parte\n"), 0o644)
	os.WriteFile(filepath.Join(clone, "novo.txt"), []byte("novo\n"), 0o644)
	g(clone, "add", ".")
	g(clone, "commit", "-qm", "CH42: segundo")
	dev := g(clone, "rev-parse", "HEAD")

	wt := filepath.Join(t.TempDir(), "teste")
	if _, err := (Repo{Dir: clone}).EnsureWorktree(wt, "BUG/CH42_stage", "stage"); err != nil {
		t.Fatal(err)
	}
	res, err := (Repo{Dir: wt}).CherryPickUnion(base, dev, CherryOpts{Squash: true, Message: "CH42: Título da demanda"})
	if err != nil {
		t.Fatalf("cherry-pick: %v", err)
	}
	if len(res.Picked) != 1 || res.Squashed != 2 {
		t.Fatalf("esperava 2 commits reunidos em 1: %+v", res)
	}
	// um único commit novo sobre a stage, com a mensagem pedida
	if n := g(wt, "rev-list", "--count", "stage..HEAD"); n != "1" {
		t.Fatalf("a branch de teste ficou com %s commit(s) além da stage", n)
	}
	if msg := g(wt, "log", "-1", "--format=%s"); msg != "CH42: Título da demanda" {
		t.Fatalf("mensagem do commit: %q", msg)
	}
	b, _ := os.ReadFile(filepath.Join(wt, "a.txt"))
	for _, quer := range []string{"da release", "da demanda", "segunda parte"} {
		if !strings.Contains(string(b), quer) {
			t.Fatalf("o arquivo perdeu %q: %q", quer, b)
		}
	}
	if strings.Contains(string(b), "<<<<<<<") {
		t.Fatalf("sobrou marcação de conflito: %q", b)
	}
	if _, err := os.Stat(filepath.Join(wt, "novo.txt")); err != nil {
		t.Fatalf("o arquivo do segundo commit não entrou: %v", err)
	}
	if sujo := (Repo{Dir: wt}).ChangedFiles(); len(sujo) > 0 {
		t.Fatalf("sobrou alteração pendente: %v", sujo)
	}
}
