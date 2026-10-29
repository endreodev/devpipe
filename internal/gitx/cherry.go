package gitx

// Transporte dos commits da demanda para a branch de teste (stage/release).
//
// A branch de teste nasce da branch de origem recém-atualizada (fetch, nunca
// pull) e recebe os commits do desenvolvimento por cherry-pick. Nada é feito
// no checkout do usuário: o cherry-pick roda em uma worktree própria.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CherryResult resume o transporte dos commits para a branch de teste.
// Toda resolução automática fica registrada: nenhum arquivo é descartado em
// silêncio.
type CherryResult struct {
	Picked   []string // commits criados na branch de teste (sha curto)
	Squashed int      // commits da demanda reunidos no commit único (modo squash)
	Empty    []string // commits sem efeito na origem (o conteúdo já estava lá)
	Unified  []string // arquivos em conflito resolvidos pela união dos dois lados
	FromDev  []string // arquivos resolvidos com a versão da demanda (binário ou inexistente na origem)
	FromBase []string // arquivos mantidos como na origem (a demanda apagou, a origem alterou)
	Removed  []string // arquivos apagados dos dois lados
}

// Total informa quantos arquivos precisaram de resolução automática.
func (c CherryResult) Total() int {
	return len(c.Unified) + len(c.FromDev) + len(c.FromBase) + len(c.Removed)
}

// CherryOpts configura o transporte dos commits.
type CherryOpts struct {
	// Squash reúne tudo em um commit só: os commits são aplicados em
	// sequência sem commitar entre eles e o resultado vira um único commit
	// com Message. É o que a branch de teste recebe — o histórico da
	// demanda fica na branch de desenvolvimento.
	Squash  bool
	Message string
}

// CherryPickUnion transplanta para a branch em checkout NESTA worktree os
// commits do intervalo base..tip, na ordem original. Sem Squash, cada commit
// vira um commit com a mensagem original; com Squash, todos viram um só.
//
// Conflito não interrompe o processo nem deixa marcação dentro do arquivo: a
// regra da equipe é que a branch de teste fique com o código da origem
// (stage/release) E com as alterações do desenvolvimento. Cada arquivo em
// conflito é resolvido por UNIÃO ("git merge-file --union"): os dois lados
// entram, nenhum trecho é descartado. O que não dá para unir — binário,
// arquivo que só existe de um lado — fica registrado em CherryResult para
// conferência humana.
//
// Em caso de erro, o cherry-pick em andamento é abortado: a branch de teste
// não fica num estado intermediário.
func (r Repo) CherryPickUnion(base, tip string, opt CherryOpts) (CherryResult, error) {
	var res CherryResult
	out, err := r.git("rev-list", "--reverse", "--no-merges", base+".."+tip)
	if err != nil {
		return res, err
	}
	shas := strings.Fields(out)
	for _, sha := range shas {
		if err := r.pickOne(sha, &res, opt.Squash); err != nil {
			r.gitQuiet("cherry-pick", "--abort")
			r.gitQuiet("reset", "--merge") // estado de merge pendente do --no-commit
			return res, fmt.Errorf("commit %s: %w", ShortSHA(sha), err)
		}
	}
	if !opt.Squash || len(shas) == 0 {
		return res, nil
	}
	// Tudo foi aplicado sem commitar: agora vira um commit só.
	if len(r.ChangedFiles()) == 0 {
		for _, sha := range shas {
			res.Empty = append(res.Empty, ShortSHA(sha))
		}
		return res, nil
	}
	sha, err := r.CommitAll(opt.Message)
	if err != nil {
		return res, err
	}
	res.Picked, res.Squashed = []string{ShortSHA(sha)}, len(shas)
	return res, nil
}

// pickOne aplica um commit. Usa --no-commit para que a resolução dos
// conflitos e o commit fiquem sob controle da aplicação (sem abrir editor e
// sem depender do estado do sequenciador do Git).
func (r Repo) pickOne(sha string, res *CherryResult, squash bool) error {
	if _, err := r.git("cherry-pick", "--no-commit", sha); err != nil {
		if uerr := r.resolveUnion(res); uerr != nil {
			return uerr
		}
	}
	// --quit limpa o estado do sequenciador mantendo a árvore e o índice: é o
	// que permite aplicar o próximo commit por cima no modo squash.
	defer r.gitQuiet("cherry-pick", "--quit")
	if squash {
		return nil
	}
	if len(r.ChangedFiles()) == 0 {
		res.Empty = append(res.Empty, ShortSHA(sha))
		return nil
	}
	msg, err := r.git("log", "-1", "--format=%B", sha)
	if err != nil {
		return err
	}
	if _, err := r.CommitAll(strings.TrimSpace(msg)); err != nil {
		return err
	}
	res.Picked = append(res.Picked, ShortSHA(sha))
	return nil
}

// resolveUnion resolve todos os arquivos que o cherry-pick deixou em conflito.
func (r Repo) resolveUnion(res *CherryResult) error {
	out, err := r.git("diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return err
	}
	var paths []string
	for _, p := range strings.Split(out, "\n") {
		if p = strings.TrimSpace(p); p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return errors.New("o cherry-pick falhou sem deixar arquivos em conflito")
	}
	for _, p := range paths {
		if err := r.unionFile(p, res); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	return nil
}

// unionFile resolve um arquivo em conflito mantendo os dois lados.
// Estágios do índice: 1 = base comum, 2 = branch de teste (origem),
// 3 = commit da demanda.
func (r Repo) unionFile(p string, res *CherryResult) error {
	base, _ := r.stage(1, p)
	origem, temOrigem := r.stage(2, p)
	demanda, temDemanda := r.stage(3, p)
	abs := filepath.Join(r.Dir, filepath.FromSlash(p))
	switch {
	case !temOrigem && !temDemanda: // apagado dos dois lados
		os.Remove(abs)
		if _, err := r.git("rm", "-q", "-f", "--", p); err != nil {
			return err
		}
		res.Removed = append(res.Removed, p)
		return nil
	case !temDemanda:
		// A demanda apagou um arquivo que a origem alterou. Entre apagar e
		// manter, mantém: a esteira nunca descarta código alheio.
		if err := os.WriteFile(abs, origem, fileMode(abs)); err != nil {
			return err
		}
		res.FromBase = append(res.FromBase, p)
	case !temOrigem || isBinary(origem, demanda):
		// Não existe na origem, ou é binário (não há como unir linha a linha):
		// fica a versão da demanda.
		if err := os.WriteFile(abs, demanda, fileMode(abs)); err != nil {
			return err
		}
		res.FromDev = append(res.FromDev, p)
	default:
		merged, err := mergeFileUnion(origem, base, demanda)
		if err != nil {
			return err
		}
		if err := os.WriteFile(abs, merged, fileMode(abs)); err != nil {
			return err
		}
		res.Unified = append(res.Unified, p)
	}
	_, err := r.git("add", "--", p)
	return err
}

// stage lê um estágio do índice (o conteúdo de um dos lados do conflito).
func (r Repo) stage(n int, p string) ([]byte, bool) {
	b, err := r.gitRaw("show", fmt.Sprintf(":%d:%s", n, p))
	return b, err == nil
}

// ShortSHA encurta um commit para leitura humana nas mensagens e no log.
func ShortSHA(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}
