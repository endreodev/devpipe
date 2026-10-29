// Package cryptx cifra os segredos que o DevPipe precisa guardar (hoje, as
// senhas dos servidores de banco cadastrados).
//
// O que isto protege e o que NÃO protege: o segredo não fica legível dentro do
// devpipe.db, então uma cópia do banco — inclusive os backups que ficam na
// própria pasta de dados — não expõe as senhas. A chave mora num arquivo
// separado, ao lado do banco. Quem tiver acesso de leitura à pasta de dados
// inteira tem o banco E a chave, e portanto consegue decifrar: isto é proteção
// contra cópia descuidada, não contra um invasor com acesso à máquina. A
// interface diz isso ao usuário, em vez de prometer mais do que entrega.
package cryptx

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// KeyFile é o nome do arquivo de chave dentro da pasta de dados.
const KeyFile = "chave.bin"

type Keeper struct{ aead cipher.AEAD }

// Open carrega a chave da pasta de dados, criando-a na primeira vez.
// A chave tem 32 bytes de crypto/rand (AES-256-GCM) e o arquivo é gravado com
// permissão restrita — no Windows isso é o melhor esforço portável, já que a
// permissão efetiva vem da ACL herdada da pasta.
func Open(dataDir string) (*Keeper, error) {
	path := filepath.Join(dataDir, KeyFile)
	key, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, fmt.Errorf("gerar chave: %w", err)
		}
		// O_EXCL: se duas instâncias subirem juntas, só uma cria a chave.
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				return Open(dataDir)
			}
			return nil, fmt.Errorf("gravar a chave em %s: %w", path, err)
		}
		_, werr := f.Write(key)
		cerr := f.Close()
		if werr != nil || cerr != nil {
			os.Remove(path) // chave truncada é pior que chave ausente
			return nil, fmt.Errorf("gravar a chave em %s: %w", path, errors.Join(werr, cerr))
		}
	} else if err != nil {
		return nil, fmt.Errorf("ler a chave em %s: %w", path, err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("a chave em %s tem %d bytes (esperado 32); se ela foi perdida, apague o arquivo e recadastre as senhas", path, len(key))
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	return &Keeper{aead: aead}, nil
}

// Encrypt devolve o texto cifrado em base64 (nonce || selo). String vazia
// entra e sai vazia: "sem senha" não vira um blob indistinguível de uma senha.
func (k *Keeper) Encrypt(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := k.aead.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverte Encrypt. Erro aqui significa chave trocada ou dado adulterado
// (o GCM autentica), nunca "senha errada".
func (k *Keeper) Decrypt(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", fmt.Errorf("segredo gravado em formato inválido: %w", err)
	}
	n := k.aead.NonceSize()
	if len(raw) < n {
		return "", errors.New("segredo gravado está truncado")
	}
	out, err := k.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return "", errors.New("não foi possível decifrar o segredo: a chave (" + KeyFile + ") foi trocada ou o dado foi alterado; recadastre a senha")
	}
	return string(out), nil
}
