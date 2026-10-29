package cryptx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCicloCompleto(t *testing.T) {
	dir := t.TempDir()
	k, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	const senha = "S3nh@ com acento ç e espaço"
	enc, err := k.Encrypt(senha)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(enc, "S3nh") || strings.Contains(enc, "acento") {
		t.Fatalf("a senha aparece no texto cifrado: %q", enc)
	}
	got, err := k.Decrypt(enc)
	if err != nil || got != senha {
		t.Fatalf("decifrou %q (%v)", got, err)
	}
	// o mesmo texto cifrado duas vezes não pode dar o mesmo resultado (nonce)
	outra, _ := k.Encrypt(senha)
	if outra == enc {
		t.Fatal("dois ciframentos iguais: o nonce não está sendo sorteado")
	}
}

func TestVazioContinuaVazio(t *testing.T) {
	k, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if enc, err := k.Encrypt(""); err != nil || enc != "" {
		t.Fatalf("vazio virou %q (%v)", enc, err)
	}
	if dec, err := k.Decrypt(""); err != nil || dec != "" {
		t.Fatalf("vazio virou %q (%v)", dec, err)
	}
}

func TestChaveEhReaproveitadaEntreAberturas(t *testing.T) {
	dir := t.TempDir()
	k1, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	enc, _ := k1.Encrypt("segredo")
	k2, err := Open(dir) // mesma pasta: deve reusar a chave já gravada
	if err != nil {
		t.Fatal(err)
	}
	if got, err := k2.Decrypt(enc); err != nil || got != "segredo" {
		t.Fatalf("a chave não foi reaproveitada: %q (%v)", got, err)
	}
}

// Chave trocada (ou dado adulterado) tem de falhar de forma explícita, e não
// devolver lixo silenciosamente.
func TestChaveTrocadaFalhaComMensagemUtil(t *testing.T) {
	dir1, dir2 := t.TempDir(), t.TempDir()
	k1, _ := Open(dir1)
	k2, _ := Open(dir2)
	enc, _ := k1.Encrypt("segredo")
	_, err := k2.Decrypt(enc)
	if err == nil {
		t.Fatal("decifrou com a chave errada")
	}
	if !strings.Contains(err.Error(), KeyFile) {
		t.Fatalf("a mensagem deveria citar o arquivo de chave: %v", err)
	}
}

func TestChaveComTamanhoErradoEhRecusada(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, KeyFile), []byte("curta"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("chave de 5 bytes deveria ser recusada")
	}
}
