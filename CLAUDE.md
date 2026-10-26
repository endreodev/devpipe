# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Idioma

O projeto inteiro é em **pt-BR**: comentários, mensagens de erro, textos da interface, nomes de etapas e valores de status no banco (`na_fila`, `desenvolvimento`, `concluido`...). Identificadores Go seguem o inglês idiomático (`Engine`, `Demand`, `Provider`); a documentação e tudo que o usuário lê, em português. Mantenha esse padrão ao editar.

## Comandos

```powershell
.\build.ps1                       # compila dist\devpipe.exe (Windows)
./build.sh                        # cross-compile para windows/linux/darwin em dist/
go build -mod=vendor -trimpath -ldflags "-s -w" -o devpipe.exe ./cmd/devpipe

go test -mod=vendor ./...                                           # suíte completa
go test -mod=vendor -count=1 ./internal/pipeline/                   # um pacote
go test -mod=vendor -run TestMergeWorkingTreeFrom ./internal/gitx/  # um teste
go vet -mod=vendor ./...

dist\devpipe.exe -port 7420 -data D:\DevPipeDados -no-browser
.\Iniciar-DevPipe.cmd             # mata instância anterior, instala dist\devpipe-v*.exe se houver, compila se faltar, inicia
```

- **`-mod=vendor` em tudo.** As dependências estão em `vendor/`; a compilação precisa funcionar sem internet. `CGO_ENABLED=0` (o driver SQLite é puro-Go/wazero) — nunca introduza dependência com CGO, isso quebraria o cross-compile.
- **F5 no VS Code** roda `cmd/devpipe` na porta 7430 com dados em `./.devdata` (separado dos dados reais).
- **Testes que executam Git são herméticos:** o Git for Windows grava `core.autocrlf=true` no gitconfig de **sistema**, que todo repositório temporário de teste herdaria — aí `git show`/`cat-file` devolvem a base em CRLF, os arquivos escritos pelo teste estão em LF e comparações byte a byte falham sem haver regressão. `internal/gittest.SemAutoCRLF()` é chamado num `init()` dos pacotes `gitx`, `encx`, `pipeline` e `web` e desliga a opção via `GIT_CONFIG_*` para todo subprocesso git. Ao criar um pacote novo cujos testes rodem Git, repita o `init()`.
- **Fim de linha:** `.gitattributes` fixa LF para tudo (exceto `*.cmd`/`*.bat`/`*.ps1`, que ficam em CRLF). Sem isso o checkout com `core.autocrlf=true` deixa a árvore em CRLF, o `git status` acusa centenas de arquivos sem diferença de conteúdo e o `gofmt -l` reprova 100% dos `.go` — anulando a verificação de formatação. Não reintroduza arquivos `.go` em CRLF.

## Arquitetura

Executável único em Go que serve uma interface web local (`go:embed` de `internal/web/static`) em `127.0.0.1:7420` e orquestra a CLI do Claude Code dentro de worktrees Git isoladas. Detalhamento completo em `docs/ARQUITETURA.md` (leia antes de mexer em pipeline/provider/encx); `docs/FAZENDA.md` cobre o painel de TV.

```
cmd/devpipe        main + subcomando oculto `hook-encoding` (chamado pelo hook PreToolUse do Claude)
internal/domain    tipos centrais, Stage/FlowStages, constantes de status — sem dependências
internal/store     SQLite (ncruces/go-sqlite3, sem CGO) + migrações
internal/pipeline  Engine: fila, agendador, etapas, validação, revisões, recuperação
internal/provider  abstração de agente (Claude Code CLI, CLI genérico)
internal/gitx      worktrees, branches, diff, commit local, merge de 3 vias
internal/encx      proteção Windows-1252 ⇄ UTF-8 (o ponto mais sutil do sistema)
internal/security  políticas fixas: diretórios autorizados, redação, classificação de comandos
internal/rules     composição das regras com precedência + hash SHA-256 de auditoria
internal/docs      extração de PDF/DOCX/DOC/TXT/MD, classificação de imagens
internal/textx     compactação de texto para economizar tokens no prompt
internal/fazenda   métricas do painel (somente leitura)
internal/web       API REST + SSE + UI embutida
tools/tokens       scripts Python de medição/economia de tokens (fora do build)
demo/              projeto JS e demanda de exemplo para ponta a ponta
```

### Fluxo de uma demanda

`backlog → analise → planejamento → preparacao → desenvolvimento → validacao → revisao → concluido`, com `bloqueado` e `ajustes` fora do fluxo. `FlowStages` em `internal/domain` é a fonte da verdade do progresso (`índice ÷ (n−1)`, nada estimado) e precisa casar com a constante `FLOW` em `internal/web/static/app.js`.

`Engine.process` (`internal/pipeline/engine.go`, ~1200 linhas) é o coração: cria a worktree **antes** da análise, roda o agente em modo somente leitura para gerar a especificação, continua a **mesma sessão** (`--resume`) no desenvolvimento, executa build/testes pela aplicação (não pelo agente) e, em falha, chama o ciclo de correção N vezes. O agendador respeita `maxParallel` e um **lock por repositório** (`repoKey`).

### Convenção de nomes de branch

`<TIPO>/<TICKET>_Desenvolvimento` — ex.: `BUG/CH101010_Desenvolvimento`. O tipo (`BUG` erro, `FIX` chamado urgente, `NR` novo requisito) é campo obrigatório da demanda (`Demand.Kind`, constantes em `internal/domain`); o ticket entra como foi digitado, com apenas os caracteres recusados pelo Git trocados por `-`. Tudo é montado em `gitx.BranchName`, com o sufixo em `gitx.DevSuffix`. Não existe prefixo por projeto — o campo antigo saiu do cadastro (a coluna `branch_prefix` continua no banco, sem uso). Ao mexer nisso, rode `internal/gitx` (há um teste que valida os nomes com `git check-ref-format`).

### Atualização do código ao escolher o projeto

Ao selecionar o projeto no cadastro da demanda (`GET /api/projects/{id}/branches`), a aplicação roda `gitx.Repo.Fetch()` = `git fetch --all --prune --no-tags`. **Fetch, nunca pull**: só atualiza `refs/remotes/*`, não avança branch local nem encosta na árvore de trabalho, e não pode gerar conflito. `git pull` faria merge no checkout do usuário e está proibido pela política. Passe `?fetch=0` para só ler.

Com as refs em dia, `Branches()` informa `upstream`/`ahead`/`behind` de cada branch local, a tela mostra o atraso, e a remota correspondente (ex.: `origin/main`) **permanece na lista quando a local está atrasada** — é por ela que a demanda parte do código atual do servidor sem alterar nada no checkout. `inspect` já sugere a remota como base nesse caso. Sem remoto configurado, o fetch é pulado sem erro. Testes em `internal/gitx` cobrem os três casos (atualiza sem tocar no checkout, sem remoto, remota mantida quando a local está atrasada).

### Invariantes que não podem ser quebradas

- **Nada destrutivo é automático.** A aplicação nunca faz merge, push, reset, rebase, clean, stash ou descarte. O checkout do usuário nunca muda. Commits ficam só na branch de trabalho local.
- **O agente nunca executa git de escrita.** `security.AgentDisallowedTools` é fixo e tem precedência sobre qualquer regra, instrução de demanda ou conteúdo de documento. `bypassPermissions` é recusado pela aplicação.
- **Precedência das regras** (`internal/rules`): política de segurança da aplicação > regras globais obrigatórias > regras do projeto obrigatórias > regras do projeto/padrões do repo > globais recomendadas > instruções da demanda. O texto composto é gravado com hash em cada `run` — ao mudar a composição, lembre que isso é trilha de auditoria.
- **Conteúdo externo é não confiável:** anexos e código entram no prompt delimitados como `<documento_nao_confiavel>`, com delimitador que o conteúdo não consegue fechar.
- **Redação de segredos** (`security.Redact`) antes de gravar qualquer evento, log ou diff.
- **Guardas HTTP** (`Server.guard`): só `127.0.0.1`, checagem de `Host` (DNS rebinding), cabeçalho `X-DevPipe: 1` e `Origin` local em toda mutação, CSP sem script inline.

### Codificação de arquivos (`internal/encx`)

Não é detalhe: é a correção de um bug real de produção. As ferramentas de edição do agente leem/gravam UTF-8, então editar um projeto Windows-1252 direto transformaria todos os acentos em `�`. Isso **não se resolve por prompt** — a conversão acontece dentro da ferramenta. O fluxo é determinístico: `Prepare` (ou o hook `PreToolUse` sob demanda, que converte só o arquivo que o agente vai abrir) → agente edita em UTF-8 → `Restore` sempre, mesmo se o agente falhar, devolvendo cada arquivo alterado à codificação original e preservando BOM e CRLF → o que sobrar de `U+FFFD` ou caractere fora de Windows-1252 **reprova a validação** e entra no ciclo de correção. Ao alterar `encx`, rode `internal/encx` e `internal/pipeline/encoding_test.go`.

### Persistência

SQLite em `<dados>/devpipe.db` (WAL, foreign keys, `SetMaxOpenConns(1)`). **O wazero precisa ser ≥ 1.9.0**: as versões anteriores, compiladas com Go 1.24+ no amd64, corrompem um registrador no `memmove` ([tetratelabs/wazero#2378](https://github.com/tetratelabs/wazero/pull/2378)), e o SQLite em wasm passa a ler e gravar memória errada (panics `out of bounds memory access`, parâmetro gravado por cima de outro). `store.TestWasmRuntimeSurvivesExclusiveWAL` reproduz o problema; ao mexer em `go.mod`/`vendor`, rode-o. As migrações são a slice `migrations` em `internal/store/store.go`, aplicadas uma vez cada e controladas por `schema_version`: **só acrescente ao final** (um `ALTER TABLE` novo), nunca edite ou reordene itens existentes — bancos em uso já marcaram aquele índice. SQL padrão, concentrado em `internal/store`.

### Front-end

JS puro, sem npm e sem build step (`internal/web/static/app.js`, `fazenda.js` com Phaser 3 embutido em `static/vendor/`). A CSP proíbe script inline; toda mutação precisa do header `X-DevPipe: 1` (ver o helper `api()` em `app.js`). Atualizações em tempo real vêm do SSE `GET /api/stream`. Como os assets são `go:embed`, é preciso recompilar para ver mudanças no front.

### Novo provedor de IA

Implemente `provider.Provider` (`ID`, `Name`, `Check`, `Run`, `SupportsImages`) em `internal/provider` e registre em `web.ConfigureProviders`. Opcionalmente `HookCapable`, para conversão de codificação sob demanda. O `Request` carrega a fase (`analise`/`desenvolvimento`/`correcao`/`teste_conexao`) e cabe ao provedor traduzi-la em permissões concretas.

## Dados em runtime

`%AppData%\DevPipe` (Windows), `~/Library/Application Support/DevPipe` (macOS), `~/.config/DevPipe` (Linux): `devpipe.db`, `devpipe.log`, `anexos/<demanda>/rev<n>/`, `patches/`, `codificacao/`.

As **áreas de trabalho** das demandas NÃO ficam na pasta de dados: a raiz vem de `Settings.WorktreeRoot` (Configurações > Áreas de trabalho), que por padrão é `D:\repos\devpipe_demandas` no Windows (`domain.DefaultWorktreeRoot`) e a subpasta `worktrees/` da pasta de dados nos demais sistemas. Cada demanda fica em `<raiz>/p<projeto>/<demanda>-<ticket>/` e grava o caminho escolhido em `Demand.WorktreePath` — mudar a raiz vale só para demandas novas; as existentes continuam onde estão. A versão exibida e comparada entre instâncias é `web.Version` (`internal/web/server.go`) — atualize-a ao publicar um binário novo.
