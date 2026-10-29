package pipeline

// Publicação do trabalho da demanda: push da branch de desenvolvimento e
// envio para teste (branch de origem -> branch de teste por cherry-pick).
//
// É a única parte da esteira que escreve fora da máquina do usuário, e nada
// aqui é automático: tudo parte de uma ação humana (aprovar a revisão ou
// clicar em publicar/enviar para teste). O checkout do usuário nunca é
// tocado — o cherry-pick roda em uma worktree própria da branch de teste.

import (
	"errors"
	"fmt"
	"strings"

	"devpipe/internal/domain"
	"devpipe/internal/gitx"
)

// PushWorkBranch publica a branch de desenvolvimento da demanda no servidor.
// Chamada pelo botão da tela e, quando o projeto tem a opção ligada, logo
// depois da aprovação da revisão.
func (e *Engine) PushWorkBranch(id int64) (string, error) {
	d, err := e.load(id)
	if err != nil {
		return "", err
	}
	p, err := e.st.GetProject(d.ProjectID)
	if err != nil {
		return "", err
	}
	msg, err := e.push(&d, p)
	if err != nil {
		return "", err
	}
	e.save(&d)
	return msg, nil
}

// push commita o que estiver pendente na área de trabalho e publica a branch.
// Não altera o estágio da demanda; quem chama decide o que fazer com a
// mensagem. Falha ao publicar é erro: nada fica "meio publicado".
func (e *Engine) push(d *domain.Demand, p domain.Project) (string, error) {
	if d.WorkBranch == "" || d.WorktreePath == "" || !dirExists(d.WorktreePath) {
		return "", errors.New("a demanda ainda não tem área de trabalho preparada")
	}
	wt := gitx.Repo{Dir: d.WorktreePath}
	main := gitx.Repo{Dir: p.Path}
	if sha, err := e.commitPending(*d, wt); err != nil {
		return "", err
	} else if sha != "" {
		e.log(*d, 0, domain.LevelOK, "git", "Commit local criado na branch "+d.WorkBranch+" ("+gitx.ShortSHA(sha)+") antes da publicação", "")
	}
	tip, err := main.BranchTip(d.WorkBranch)
	if err != nil {
		return "", err
	}
	// head != tip: ou o trabalho ainda não está na branch (modo "direto na
	// branch" com o checkout aberto: as alterações ficaram pendentes lá), ou
	// a branch já passou dele — caso de republicar depois de um commit.
	if head := wt.HeadSHA(); head != tip && !main.IsAncestor(head, tip) {
		if tip, err = e.commitNoCheckout(d, main, wt, tip); err != nil {
			return "", err
		}
	}
	res := main.Push(d.WorkBranch)
	switch {
	case res.Skipped:
		msg := "O projeto não tem servidor configurado (sem remoto): não há onde publicar a branch " + d.WorkBranch
		e.log(*d, 0, domain.LevelWarn, "git", msg, "")
		return msg, nil
	case res.Error != "":
		e.log(*d, 0, domain.LevelError, "git", "Falha ao publicar a branch "+d.WorkBranch, res.Error)
		return "", fmt.Errorf("falha ao publicar a branch %s: %s", d.WorkBranch, res.Error)
	}
	d.Pushed = true
	msg := "Branch " + d.WorkBranch + " publicada em " + res.Remote + " (" + gitx.ShortSHA(tip) + ")"
	if res.Already {
		msg = "A branch " + d.WorkBranch + " já estava publicada em " + res.Remote
	}
	e.log(*d, 0, domain.LevelAuth, "git", msg, "")
	return msg, nil
}

// commitNoCheckout cuida do caso "direto na branch" com a branch aberta no
// checkout do usuário: as alterações da demanda foram deixadas lá como
// pendentes (ver ApplyToBranch), então o commit que falta é no checkout dele.
//
// Commita SOMENTE os arquivos que a demanda tocou. O que o usuário tiver
// alterado em outros arquivos continua pendente, intocado. Quando os dois
// mexeram no mesmo arquivo, o conteúdo já foi juntado na aplicação (merge de
// 3 vias) e vai junto — é um arquivo só.
func (e *Engine) commitNoCheckout(d *domain.Demand, main, wt gitx.Repo, tip string) (string, error) {
	dir := main.WorktreeOfBranch(d.WorkBranch)
	if !d.DirectBranch || dir == "" {
		return "", fmt.Errorf("o trabalho da demanda ainda não está commitado na branch %s; nada foi publicado", d.WorkBranch)
	}
	// "Arquivos da demanda" = o que a área de trabalho tem a mais do que a
	// própria branch. Demand.BaseCommit não serve aqui: ele é o começo da
	// demanda e fica para trás quando a área acompanha a ponta da branch
	// entre uma revisão e outra.
	base := wt.MergeBase(tip)
	if base == "" {
		base = d.BaseCommit
	}
	checkout := gitx.Repo{Dir: dir}
	pendentes, err := checkout.ChangedPaths()
	if err != nil {
		return "", err
	}
	daDemanda := map[string]bool{}
	for _, f := range wt.FilesSince(base) {
		daDemanda[f] = true
	}
	var alvo []string
	for _, f := range pendentes {
		if daDemanda[f] {
			alvo = append(alvo, f)
		}
	}
	// Nada pendente: o commit já foi feito (republicar) ou o usuário mesmo
	// commitou no Visual Studio. Publica a branch como ela está.
	if len(alvo) == 0 {
		return tip, nil
	}
	sha, err := checkout.CommitPaths(alvo, e.commitMsg(*d))
	if err != nil {
		return "", fmt.Errorf("não foi possível commitar as alterações da demanda em %s: %w", dir, err)
	}
	if sha == "" {
		return tip, nil
	}
	restantes := len(pendentes) - len(alvo)
	msg := fmt.Sprintf("Commit de %d arquivo(s) da demanda criado no checkout %s (branch %s, %s)", len(alvo), dir, d.WorkBranch, gitx.ShortSHA(sha))
	if restantes > 0 {
		msg += fmt.Sprintf("; %d alteração(ões) suas em outros arquivos continuam pendentes", restantes)
	}
	e.log(*d, 0, domain.LevelOK, "git", msg, strings.Join(alvo, "\n"))
	return sha, nil
}

// commitMsg é a mensagem do commit da demanda na revisão atual.
func (e *Engine) commitMsg(d domain.Demand) string {
	rev, err := e.st.GetRevision(d.ID, d.CurrentRevision)
	if err != nil {
		rev = domain.Revision{Number: d.CurrentRevision}
	}
	return commitMessage(d, rev)
}

// commitPending commita o que restou na área de trabalho (por exemplo, o
// reparo de codificação feito depois do commit do desenvolvimento), com a
// mesma mensagem do commit da demanda.
func (e *Engine) commitPending(d domain.Demand, wt gitx.Repo) (string, error) {
	if len(wt.ChangedFiles()) == 0 {
		return "", nil
	}
	return wt.CommitAll(e.commitMsg(d))
}

// TestBranchSuggestion devolve o nome sugerido para a branch de teste de uma
// demanda a partir da origem escolhida. A tela mostra o nome antes de
// confirmar e o usuário pode trocá-lo.
func (e *Engine) TestBranchSuggestion(id int64, alvo string) (string, error) {
	d, err := e.st.GetDemand(id)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(alvo) == "" {
		return "", nil
	}
	return gitx.TargetBranchName(d.Kind, d.Ticket, alvo), nil
}

// SendToTest cria a branch de teste da demanda a partir da branch de origem
// escolhida — stage, release ou outra, conforme o projeto — e leva para ela
// os commits do desenvolvimento por cherry-pick.
//
// nome é o nome da branch de teste; vazio usa a sugestão da convenção
// (<TIPO>/<TICKET>_<alvo>). O que a demanda produziu entra como UM commit:
// os commits do desenvolvimento são aplicados em sequência e reunidos em um
// só (squash) — o histórico detalhado fica na branch de desenvolvimento.
//
// A origem é sempre atualizada antes (fetch, nunca pull) e a branch de teste
// nasce da ref REMOTA quando ela existe: é o código atual do servidor, sem
// depender de a cópia local estar em dia e sem encostar no checkout do
// usuário. Conflito não para o envio: o arquivo fica com o código da origem
// E com as alterações do desenvolvimento (ver gitx.CherryPickUnion).
func (e *Engine) SendToTest(id int64, alvo, nome string) (string, error) {
	d, err := e.load(id)
	if err != nil {
		return "", err
	}
	alvo = strings.TrimSpace(alvo)
	if alvo == "" {
		return "", errors.New("escolha a branch de origem do teste (ex.: stage ou release)")
	}
	if !d.Pushed {
		return "", errors.New("publique a branch de desenvolvimento antes de enviar para teste")
	}
	if d.WorktreePath == "" || !dirExists(d.WorktreePath) {
		return "", errors.New("a demanda ainda não tem área de trabalho preparada")
	}
	p, err := e.st.GetProject(d.ProjectID)
	if err != nil {
		return "", err
	}
	main := gitx.Repo{Dir: p.Path}
	if res := main.Fetch(); res.Error != "" {
		e.log(d, 0, domain.LevelWarn, "git", "Não foi possível atualizar as referências do servidor antes do envio para teste", res.Error)
	}
	base, err := baseDoTeste(main, alvo)
	if err != nil {
		return "", err
	}
	if nome = gitx.NormalizeBranchName(nome); nome == "" {
		nome = gitx.TargetBranchName(d.Kind, d.Ticket, alvo)
	}
	if main.LocalBranchExists(nome) && nome != d.TestBranch {
		return "", fmt.Errorf("a branch %s já existe no repositório; escolha outro nome ou trate o envio manualmente", nome)
	}
	devTip, err := main.BranchTip(d.WorkBranch)
	if err != nil {
		return "", err
	}
	wtDev := gitx.Repo{Dir: d.WorktreePath}
	desde := d.BaseCommit
	if desde == "" {
		desde = wtDev.MergeBase(d.DiffBase())
	}
	if desde == "" {
		return "", errors.New("não foi possível identificar o ponto de partida da demanda")
	}
	dir := testWorktreeDir(d.WorktreePath, alvo)
	if _, err := main.EnsureWorktree(dir, nome, base); err != nil {
		return "", fmt.Errorf("não foi possível criar a área da branch %s: %w", nome, err)
	}
	res, cerr := (gitx.Repo{Dir: dir}).CherryPickUnion(desde, devTip, gitx.CherryOpts{Squash: true, Message: e.commitMsg(d)})
	if cerr != nil {
		e.log(d, 0, domain.LevelError, "git", "Falha ao levar os commits da demanda para "+nome, cerr.Error())
		return "", fmt.Errorf("falha ao levar os commits para %s: %w", nome, cerr)
	}
	d.TestBranch, d.TestTarget = nome, gitx.TargetName(alvo)
	msg := fmt.Sprintf("Branch de teste %s criada a partir de %s com um commit único (%d commit(s) da demanda reunidos)", nome, base, res.Squashed)
	if len(res.Picked) == 0 {
		msg = fmt.Sprintf("Branch de teste %s criada a partir de %s — a origem já continha as alterações da demanda", nome, base)
	}
	if n := res.Total(); n > 0 {
		msg += fmt.Sprintf("; %d arquivo(s) precisaram de resolução automática", n)
	}
	nivel := domain.LevelOK
	if res.Total() > 0 {
		nivel = domain.LevelWarn
	}
	e.log(d, 0, nivel, "teste", msg, detalheCherry(res))
	if pr := main.Push(nome); pr.Error != "" {
		e.log(d, 0, domain.LevelError, "git", "A branch de teste "+nome+" foi criada localmente, mas não foi publicada", pr.Error)
		msg += ". A publicação falhou: " + pr.Error
	} else if pr.Done {
		e.log(d, 0, domain.LevelAuth, "git", "Branch de teste "+nome+" publicada em "+pr.Remote, "")
		msg += ", publicada em " + pr.Remote
	}
	e.touch(&d, "Enviada para teste em "+nome)
	e.save(&d)
	return msg, nil
}

// baseDoTeste resolve a branch de origem escolhida no ponto de onde a branch
// de teste vai nascer. A ref remota tem preferência: é o código atual do
// servidor, e a cópia local pode estar atrasada.
func baseDoTeste(main gitx.Repo, alvo string) (string, error) {
	nome := gitx.TargetName(alvo)
	for _, ref := range []string{"refs/remotes/" + alvo, "refs/remotes/origin/" + nome, "refs/heads/" + nome} {
		if main.RefExists(ref) {
			return ref, nil
		}
	}
	return "", fmt.Errorf("branch de origem %q não encontrada no repositório", alvo)
}

// testWorktreeDir é a área de trabalho da branch de teste, ao lado da área da
// demanda (<worktree>_stage, <worktree>_release).
func testWorktreeDir(worktree, alvo string) string {
	return worktree + "_" + sanitize(gitx.TargetName(alvo))
}

func detalheCherry(res gitx.CherryResult) string {
	var b strings.Builder
	add := func(titulo string, itens []string) {
		if len(itens) > 0 {
			b.WriteString(titulo + ":\n" + strings.Join(itens, "\n") + "\n")
		}
	}
	add("Commit criado na branch de teste", res.Picked)
	add("Commits da demanda sem efeito (a origem já tinha o conteúdo)", res.Empty)
	add("Arquivos com os dois lados unidos (origem + desenvolvimento)", res.Unified)
	add("Arquivos resolvidos com a versão da demanda (binário ou inexistente na origem)", res.FromDev)
	add("Arquivos mantidos como na origem (a demanda apagou e a origem alterou)", res.FromBase)
	add("Arquivos apagados nos dois lados", res.Removed)
	return b.String()
}
