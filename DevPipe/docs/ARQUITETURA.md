# DevPipe — Arquitetura e decisões técnicas

## 1. Avaliação de tecnologia

A pergunta certa não era "qual linguagem é mais rápida", e sim "onde está o gargalo". Nesta aplicação o tempo é dominado por **chamadas ao agente de IA (minutos)**, **builds e testes do projeto-alvo (segundos a minutos)** e **operações Git**. O código da própria esteira passa a maior parte do tempo esperando processos externos. Diferenças de microssegundos entre linguagens são irrelevantes; o que importa é confiabilidade ao orquestrar processos, distribuição simples e produtividade.

| Critério | C# / .NET 8 | Go | Rust |
|---|---|---|---|
| Velocidade de desenvolvimento | Alta (ecossistema maduro, ótimo tooling) | Alta (linguagem pequena, stdlib completa) | Média/baixa (borrow checker, async mais complexo) |
| Memória/CPU em repouso | ~60–120 MB com runtime | ~15–30 MB | ~5–15 MB |
| Arquivos e diretórios | Excelente | Excelente | Excelente |
| Processos externos (git, CLI do agente, build) | Bom (`Process`), cuidado com deadlocks de stdout/stderr | **Excelente**: `os/exec` com `context` (cancelamento/timeout nativos), goroutines para ler fluxos | Bom (`std::process`/tokio), mais código para o mesmo resultado |
| Integração com agentes de IA | SDKs disponíveis; mas a integração aqui é via CLI | Via CLI/HTTP, trivial | Via CLI/HTTP, mais verboso |
| Segurança em operações locais | Boa | Boa (sem estado global, tipagem simples) | Melhor garantia de memória, mas o risco real aqui é *o que* se executa, não corrupção de memória |
| Interface moderna | WPF/WinUI/MAUI (só Windows ou pesado) ou Blazor | Web local embutida | Tauri (exige WebView2 + toolchain) ou web local |
| Distribuição | Single-file self-contained ~70 MB, ou requer runtime | **Um binário estático ~16 MB**, cross-compile trivial (`GOOS=windows`) | Binário pequeno; cross-compile para Windows exige toolchain/linker |
| Manutenção / novos provedores | Boa | Boa (interfaces implícitas) | Boa, porém mais cerimônia |

### Recomendação adotada: **Go + interface web local embutida**

- **Orquestração de processos é o núcleo do sistema**, e é o ponto mais forte do Go: `exec.CommandContext` + cancelamento por grupo de processos + goroutines lendo `stdout`/`stderr` sem risco de bloqueio.
- **Executável único**, sem runtime, sem instalador, gerado para Windows a partir de qualquer SO. SQLite via driver **puro-Go (WebAssembly/wazero), sem CGO**, preservando o cross-compile.
- **Interface web local** dá liberdade total de design (o visual "HUD/gaming" pedido) com HTML/CSS/JS, sem framework de desktop. O executável serve a interface embutida (`go:embed`) e abre o navegador.
- **Custo de complexidade**: duas linguagens (Go + JS). Mitigado mantendo o front-end sem build step (JS puro, sem npm).

Por que não as outras: **C#** seria a segunda melhor opção (especialmente se a equipe já domina .NET); perde em distribuição (tamanho/runtime) e em UI multiplataforma. **Rust** não traz benefício proporcional ao custo para uma ferramenta limitada por I/O e processos externos.

## 2. Visão geral

```
 Navegador (localhost)  ──HTTP/JSON + SSE──►  devpipe (processo único)
                                              ├─ web        API REST, SSE, UI embutida, guardas CSRF/Host
                                              ├─ pipeline   fila + agendador + etapas + ações humanas
                                              │    ├─ provider   Claude Code CLI | CLI genérico | (futuros)
                                              │    ├─ gitx       validação, worktree isolada, diff, commit local
                                              │    ├─ docs       extração de anexos
                                              │    ├─ rules      composição/precedência/hash das regras
                                              │    └─ security   políticas fixas, redação, classificação
                                              └─ store      SQLite (metadados) + disco (anexos, worktrees)
```

### Fluxo de uma demanda

1. **Cadastro** — demanda + revisão nº 1 (instruções originais) + anexos gravados em disco.
2. **Fila** — o agendador escolhe por prioridade e ordem; no máximo `maxParallel` execuções e **uma por repositório**.
3. **Análise + Planejamento** — anexos extraídos (texto delimitado como *dado não confiável*). O agente roda em **modo somente leitura** e devolve a especificação normalizada (objetivo, regras, critérios de aceite, arquivos afetados, dúvidas, pressupostos, plano). Opcionalmente a esteira **pausa para aprovação humana**.
4. **Preparação** — a aplicação (não o agente) valida o repositório, resolve a branch base, avisa sobre alterações locais e cria/reutiliza a **worktree isolada** `dados/worktrees/p<projeto>/<id>-<ticket>` na branch `devpipe/<ticket>-<titulo>`.
5. **Desenvolvimento** — o agente edita arquivos na worktree com ferramentas restritas.
6. **Validação** — a aplicação executa os comandos oficiais de build/teste. Se falhar, pede correção ao agente (retomando a mesma sessão) até N vezes. Se não houver comandos, registra explicitamente "validação não executada".
7. **Revisão** — commit local na branch de trabalho (opcional), diff disponível, aguarda humano.
8. **Concluir** ou **Devolver para ajustes** — a devolução cria a revisão nº N com novas instruções/anexos; a nova análise recebe as especificações anteriores, o resumo da última implementação e o diff existente, e planeja **somente os ajustes**.

### Progresso

`progresso = índice da etapa atual ÷ (nº de etapas do fluxo − 1)`. Reflete apenas etapas efetivamente concluídas; nada é estimado.

### Recuperação

Ao iniciar, execuções marcadas como "executando" são encerradas como interrompidas e as demandas vão para **Bloqueado**, guardando a etapa de origem (`blocked_from`). "Retomar" reaproveita a especificação já gerada e a worktree; se o bloqueio foi na validação e a implementação existe, retoma direto pela validação.

## 3. Integração com o Claude

Opções avaliadas:

| Opção | Prós | Contras |
|---|---|---|
| **Claude Code CLI** (`claude -p --output-format stream-json`) — **adotada** | Agente completo (leitura, edição, busca, execução) já pronto; permissões por ferramenta; eventos em fluxo JSON; funciona com assinatura Pro/Max/Team/Enterprise ou chave do Console | Depende da CLI instalada e autenticada; formato de eventos pode evoluir |
| Claude API (Messages + tool use próprio) | Controle total, cobrança por tokens previsível | Exigiria reimplementar o loop de agente e as ferramentas de edição; só funciona com chave de API |
| Claude Agent SDK | Mesmo agente do Claude Code como biblioteca | SDK em TypeScript/Python: adicionaria um runtime à distribuição |

Como a CLI é chamada:

- `--permission-mode default` na análise (só `Read, Glob, Grep, LS` liberados; todo o resto é negado automaticamente em modo não interativo).
- `--permission-mode acceptEdits` no desenvolvimento, com `--allowedTools` = leitura + edição + `git status/diff/log` + prefixos de comando configurados no projeto.
- `--disallowedTools` sempre inclui git de escrita (push, merge, rebase, reset, checkout, switch, clean, restore, stash, commit, remote, config, worktree), `rm -rf`, `sudo`, `curl`, `wget`, `WebFetch`, `WebSearch` e `PowerShell`.
- `bypassPermissions` é recusado pela aplicação.
- Prompt via **stdin** (sem limite de linha de comando); `--append-system-prompt` com a política de segurança em linha única; `--add-dir` com a pasta de anexos (para o Claude ler PDFs e imagens diretamente); `--resume <sessão>` nas correções.
- No Windows, se a CLI for um wrapper `.cmd` com `cli.js` ao lado, o `cli.js` é executado diretamente via `node` (o `cmd.exe` corromperia argumentos).
- **Modo somente assinatura** (padrão): variáveis de API removidas do ambiente do processo; o evento `system/init` informa `apiKeySource` — só `"none"` (login da assinatura) é aceito; o evento `rate_limit_event` informa o consumo das janelas de 5 h e semanal e `isUsingOverage` — uso extra pago interrompe a execução; `status: rejected` (limite atingido) devolve a demanda à fila na mesma etapa e pausa o agendador até `resetsAt`.
- Cada evento (`assistant`/`tool_use`, `tool_result` com erro, `result` com custo e `permission_denials`) vira um evento do monitor.

## 4. Modelo de persistência (SQLite)

Banco: `dados/devpipe.db` (WAL, chaves estrangeiras). Anexos e worktrees ficam no disco; o banco guarda metadados e caminhos.

| Tabela | Conteúdo |
|---|---|
| `projects` | nome, caminho, linguagem, solução, atalhos de branch, arquivos de padrões, regras extras, comandos de build/teste, comandos liberados ao agente, prefixo de branch, commit automático |
| `demands` | ticket, título, resumo, projeto, prioridade, ordem, observações, branch base/de trabalho, worktree, provedor, modelo, etapa, `blocked_from`, status, mensagem, progresso, tentativas, validação, revisão atual, última atividade, tempo acumulado |
| `revisions` | nº, tipo (original/ajuste), instruções, **especificação normalizada** (artefato separado do original), aprovada? |
| `attachments` | arquivo original (caminho em disco), tipo, tamanho, caminho do texto extraído, nota de processamento |
| `runs` | execução por tentativa: provedor, status, etapa, **hash e texto das regras usadas**, sessão do agente, resumo, erro, custo, diff-stat, commit |
| `events` | log auditável: data/hora, demanda, ticket, projeto, execução, etapa, nível (info, aviso, erro, sucesso, agente, autorização), mensagem, detalhe |
| `rules` / `rule_history` | regras globais/projeto, escopo por diretório/linguagem, obrigatória, prioridade, versão; histórico de cada versão |
| `settings` | configuração geral (JSON) |
| `schema_version` | controle de migrações |

Consultas por ticket, projeto, branch e período estão na API (`/api/demands?q=&project=&branch=&from=&to=`). Migração futura para outro banco: o acesso está concentrado em `internal/store`, com SQL padrão.

Pastas de dados:

```
DevPipe/
  devpipe.db, devpipe.log
  anexos/<demanda>/rev<n>/<arquivo> (+ .extraido.txt)
  worktrees/p<projeto>/<demanda>-<ticket>/
```

## 5. Regras e precedência

1. Política de segurança da aplicação (fixa).
2. Regras globais obrigatórias.
3. Regras do projeto obrigatórias.
4. Regras do projeto, regras adicionais do cadastro e arquivos de padrões do repositório.
5. Regras globais recomendadas.
6. Instruções da demanda (podem detalhar, nunca violar 1–3).

O texto composto recebe um hash SHA-256 (16 caracteres) gravado em cada execução, junto com o texto integral — é possível auditar exatamente quais regras a IA recebeu.

## 6. Segurança

| Ameaça | Controle |
|---|---|
| Outro site acionar a API local | Escuta só em 127.0.0.1; checagem de `Host` (DNS rebinding); cabeçalho obrigatório `X-DevPipe` e checagem de `Origin` em mutações (CSRF); CSP sem scripts inline |
| Alterar repositórios não autorizados | Lista de diretórios autorizados; só projetos cadastrados; agente roda com `cwd` na worktree |
| Perda de trabalho do usuário | Worktree isolada; checkout do usuário nunca muda; sem reset/clean/descartes; branch existente não criada pela demanda nunca é reutilizada |
| Operações Git perigosas | Feitas apenas pela aplicação (worktree add, commit local); agente proibido de git de escrita; merge/push manuais |
| Comandos destrutivos / rede | Classificação (leitura, alteração, teste/build, instalação, rede, destrutivo); validação bloqueia destrutivo e rede; prefixos liberados ao agente não aceitam encadeamento (`; & | $ \``) |
| Prompt injection em documentos/código | Conteúdo externo delimitado como `<documento_nao_confiavel>` (não pode ser fechado pelo conteúdo); política de segurança com precedência máxima; o agente deve relatar tentativas como "Alerta de segurança" |
| Vazamento de segredos | Redação de chaves (Anthropic, OpenAI, GitHub, Slack, AWS), `Bearer`, `senha=`/`token=`, credenciais em URLs e chaves privadas antes de gravar eventos e diffs |
| Execuções presas | Tempo limite por chamada do agente e por comando; cancelamento encerra a árvore de processos |
| Conflito no mesmo repositório | Lock por repositório no agendador |
| Anexos maliciosos | Executáveis/scripts recusados; nomes de arquivo sanitizados; limite de 50 MB; download servido como anexo |

## 6.1 Proteção de codificação (`internal/encx`)

Problema observado em produção: em projetos Windows-1252, a ferramenta de edição do agente lê o arquivo como UTF-8 (cada acento vira U+FFFD) e regrava o arquivo inteiro em UTF-8. Instruções no prompt não evitam isso, pois a conversão ocorre na ferramenta.

| Etapa | O que acontece |
|---|---|
| `Prepare` (antes de cada chamada de desenvolvimento/correção) | Varre os arquivos versionados de texto da worktree; os que não são UTF-8 válido são decodificados como Windows-1252 (tabela própria, ida e volta sem perdas inclusive para os 5 bytes indefinidos) e gravados em UTF-8. Registra a codificação original e a predominante do repositório. |
| Agente edita | Vê a acentuação correta. O prompt informa que a reconversão é automática e pede somente caracteres representáveis em Windows-1252. |
| `Restore` (sempre, mesmo se o agente falhar) | Para cada arquivo alterado (worktree e commits desde o merge-base): codificação alvo = original registrada › codificação na branch base › padrão do projeto. Preserva BOM e CRLF (quando a base era 100% CRLF). Recupera U+FFFD por correspondência com o vocabulário da versão base (uma sequência de U+FFFD casa 1+ caracteres não-ASCII; só corrige quando há candidata única). |
| Validação | Problemas restantes (U+FFFD sem candidata, caracteres fora de Windows-1252) reprovam a validação e entram no ciclo de correção da IA. |

O diff exibido na interface decodifica linhas Windows-1252 para exibição. A ação "Reparar acentuação" executa apenas o `Restore` em uma worktree existente (branches afetadas por versões anteriores) e faz commit local.

## 7. Pontos de extensão

- **Novo provedor de IA**: implementar `provider.Provider` (`Check`, `Run`, `SupportsImages`) e registrá-lo em `web.ConfigureProviders`.
- **Pipelines por projeto / validações personalizadas**: `Engine.validate` hoje executa build + testes; é o ponto para listas de passos configuráveis.
- **Sistemas de tickets externos**: criar demandas via `POST /api/demands` (multipart) ou um importador que use `store.CreateDemand`.
- **Relatórios**: tabelas `runs` e `events` já guardam status, custo, duração e regras usadas.
