package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// MergeResult resume a aplicação arquivo a arquivo (merge de 3 vias).
type MergeResult struct {
	Changed   []string // arquivos alterados no checkout sem conflito
	Conflicts []string // arquivos alterados com marcação de conflito (<<<<<<< / >>>>>>>)
	Kept      []string // arquivos mantidos como estavam no checkout (exclusão/binário em conflito)
	Same      []string // arquivos que já estavam iguais ao resultado da demanda
}

// MergeWorkingTreeFrom leva para os arquivos deste checkout as alterações
// feitas em src desde base, mesmo quando o checkout tem alterações locais nos
// mesmos arquivos. Para cada arquivo faz um merge de 3 vias (base, alteração
// local, alteração da demanda) com "git merge-file": o que não conflita é
// juntado automaticamente; trechos que conflitam ficam com marcação de
// conflito no arquivo, para revisão no Visual Studio. Não faz commit nem mexe
// no índice — tudo aparece como alteração pendente. src deve estar com as
// alterações no índice (ver PatchSince).
func (r Repo) MergeWorkingTreeFrom(src Repo, base, rotulo string) (MergeResult, error) {
	var res MergeResult
	if strings.TrimSpace(rotulo) == "" {
		rotulo = "demanda"
	}
	raw, err := src.gitRaw("-c", "core.quotepath=false", "diff", "--cached", "--no-renames", "--name-status", "-z", base)
	if err != nil {
		return res, err
	}
	parts := splitZ(raw)
	for i := 0; i+1 < len(parts); i += 2 {
		p := parts[i+1]
		if err := r.merge3File(src, base, p, rotulo, &res); err != nil {
			return res, fmt.Errorf("%s: %v", p, err)
		}
	}
	return res, nil
}

func readOpt(path string) ([]byte, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return b, true
}

func isBinary(bs ...[]byte) bool {
	for _, b := range bs {
		n := len(b)
		if n > 8000 {
			n = 8000
		}
		if bytes.IndexByte(b[:n], 0) >= 0 {
			return true
		}
	}
	return false
}

func (r Repo) merge3File(src Repo, base, p, rotulo string, res *MergeResult) error {
	oursPath := filepath.Join(r.Dir, filepath.FromSlash(p))
	ours, oursOK := readOpt(oursPath)
	theirs, theirsOK := readOpt(filepath.Join(src.Dir, filepath.FromSlash(p)))
	// conteúdo da base com as mesmas conversões do checkout (ex.: CRLF)
	baseB, baseErr := src.gitRaw("cat-file", "--filters", base+":"+p)
	baseOK := baseErr == nil

	switch {
	case !theirsOK: // a demanda excluiu o arquivo
		if !oursOK {
			res.Same = append(res.Same, p)
		} else if baseOK && bytes.Equal(ours, baseB) {
			if err := os.Remove(oursPath); err != nil {
				return err
			}
			res.Changed = append(res.Changed, p)
		} else {
			res.Kept = append(res.Kept, p+" (excluído pela demanda, mas alterado localmente: mantido)")
		}
		return nil
	case oursOK && bytes.Equal(ours, theirs):
		res.Same = append(res.Same, p)
		return nil
	case !oursOK || (baseOK && bytes.Equal(ours, baseB)):
		// sem alteração local (ou arquivo ausente no checkout): usa a versão da demanda
		os.MkdirAll(filepath.Dir(oursPath), 0o755)
		if err := os.WriteFile(oursPath, theirs, fileMode(oursPath)); err != nil {
			return err
		}
		res.Changed = append(res.Changed, p)
		return nil
	}
	if bytes.Contains(ours, []byte("<<<<<<< "+rotuloLocal)) {
		res.Kept = append(res.Kept, p+" (ainda tem marcação de conflito de uma aplicação anterior: resolva antes de reaplicar)")
		return nil
	}
	if !baseOK {
		baseB = nil // arquivo novo dos dois lados: merge com base vazia
	}
	if isBinary(ours, theirs, baseB) {
		if err := os.WriteFile(oursPath+SufixoBinario, theirs, 0o644); err != nil {
			return err
		}
		res.Kept = append(res.Kept, p+" (binário alterado dos dois lados: versão da demanda salva em "+p+SufixoBinario+")")
		return nil
	}
	merged, conflitos, err := mergeFile(ours, baseB, theirs, rotulo)
	if err != nil {
		return err
	}
	if conflitos == 0 && bytes.Equal(merged, ours) {
		res.Same = append(res.Same, p) // as alterações da demanda já estavam no arquivo
		return nil
	}
	if err := os.WriteFile(oursPath, merged, fileMode(oursPath)); err != nil {
		return err
	}
	if conflitos > 0 {
		res.Conflicts = append(res.Conflicts, p)
	} else {
		res.Changed = append(res.Changed, p)
	}
	return nil
}

func fileMode(path string) os.FileMode {
	if st, err := os.Stat(path); err == nil {
		return st.Mode().Perm()
	}
	return 0o644
}

// Rótulos que aparecem DENTRO dos arquivos do usuário (marcação de conflito)
// e no nome do arquivo auxiliar. Nada aqui pode identificar a ferramenta: é
// conteúdo que fica no repositório e vai para o commit de quem resolver.
const rotuloLocal = "Seu checkout (alterações locais)"

// SufixoBinario nomeia a cópia deixada quando um binário mudou dos dois lados.
const SufixoBinario = ".da-demanda"

// mergeFile roda "git merge-file -p" e devolve o resultado e a quantidade de
// conflitos (o git sai com código = nº de conflitos).
func mergeFile(ours, base, theirs []byte, rotulo string) ([]byte, int, error) {
	return mergeFileArgs(ours, base, theirs,
		"-L", rotuloLocal, "-L", "base da demanda", "-L", rotulo+" (demanda)")
}

// mergeFileUnion une os dois lados sem marcação de conflito: nos trechos em
// que ambos mudaram, o resultado fica com o conteúdo dos dois. É como a
// branch de teste recebe o código da origem (stage/release) E as alterações
// do desenvolvimento — ver CherryPickUnion.
func mergeFileUnion(ours, base, theirs []byte) ([]byte, error) {
	out, _, err := mergeFileArgs(ours, base, theirs, "--union")
	return out, err
}

func mergeFileArgs(ours, base, theirs []byte, extra ...string) ([]byte, int, error) {
	dir, err := os.MkdirTemp("", "devpipe-merge-")
	if err != nil {
		return nil, 0, err
	}
	defer os.RemoveAll(dir)
	names := []string{"ours", "base", "theirs"}
	for i, b := range [][]byte{ours, base, theirs} {
		if err := os.WriteFile(filepath.Join(dir, names[i]), b, 0o644); err != nil {
			return nil, 0, err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	args := append([]string{"merge-file", "-p"}, extra...)
	args = append(args, "ours", "base", "theirs")
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	hideWindow(cmd)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	Log("git "+strings.Join(args, " "), "", err)
	if err == nil {
		return out.Bytes(), 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() > 0 && ee.ExitCode() <= 127 {
		return out.Bytes(), ee.ExitCode(), nil
	}
	return nil, 0, fmt.Errorf("git merge-file: %s", strings.TrimSpace(errb.String()+" "+err.Error()))
}
