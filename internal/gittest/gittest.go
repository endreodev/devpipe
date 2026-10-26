// Package gittest prepara o ambiente dos testes que executam o Git.
//
// É importado somente por arquivos _test.go, portanto não entra no binário.
package gittest

import (
	"os"
	"strconv"
)

// SemAutoCRLF desliga core.autocrlf para todos os subprocessos git deste
// processo de teste — inclusive os que o código sob teste dispara.
//
// O instalador do Git for Windows grava core.autocrlf=true no gitconfig de
// SISTEMA, e todo repositório temporário criado por um teste o herda. Com ele
// ligado, `git show` e `git cat-file --filters` devolvem o conteúdo da base em
// CRLF enquanto os arquivos que o teste escreveu estão em LF: o merge de 3 vias
// passa a enxergar o arquivo inteiro como alterado e as comparações byte a byte
// falham, sem que exista regressão no código.
//
// As variáveis GIT_CONFIG_* têm precedência sobre qualquer arquivo de
// configuração (sistema, global ou do repositório). A entrada é acrescentada ao
// final das que já existirem, para não descartar configurações do ambiente.
func SemAutoCRLF() {
	n, _ := strconv.Atoi(os.Getenv("GIT_CONFIG_COUNT"))
	os.Setenv("GIT_CONFIG_KEY_"+strconv.Itoa(n), "core.autocrlf")
	os.Setenv("GIT_CONFIG_VALUE_"+strconv.Itoa(n), "false")
	os.Setenv("GIT_CONFIG_COUNT", strconv.Itoa(n+1))
}
