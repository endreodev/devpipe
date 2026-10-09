package gitx

import "testing"

func TestSafeBranchName(t *testing.T) {
	got := SafeBranchName("devpipe/", "CH-1001", "Cupom de desconto: ação ~ especial!! no pedido final")
	if got != "devpipe/ch-1001-cupom-de-desconto-acao-especial" {
		t.Fatalf("obtido %q", got)
	}
	if SafeBranchName("x/", "", "") != "x/demanda" {
		t.Fatal("nome vazio")
	}
}
