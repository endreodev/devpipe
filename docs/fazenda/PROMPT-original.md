# PROMPT — "Fazenda da Esteira" (Painel visual do pipeline de desenvolvimento)

> Metodologia: **Context Engineering** (todo o contexto que o agente precisa, organizado em camadas) + **Spec-Driven Development** (spec → design → tarefas → implementação, com aprovação entre as fases).
> Cole este arquivo inteiro no seu agente de código (Claude Code, Cursor, etc.) ou salve na raiz do repositório como `PROMPT.md`.

---

## 0. COMO VOCÊ (AGENTE) DEVE TRABALHAR

Você é um engenheiro sênior full-stack (Go + TypeScript) com experiência em jogos 2D (Phaser 3).
Siga **Spec-Driven Development** rigorosamente:

1. **Fase 1 – Requisitos:** leia todo este documento e gere `specs/requirements.md` com requisitos numerados (RF-xx, RNF-xx) e critérios de aceite em formato **Dado / Quando / Então**. Liste as dúvidas em aberto. **PARE e peça aprovação.**
2. **Fase 2 – Design:** gere `specs/design.md` com arquitetura, modelo de domínio, contratos (JSON), máquina de estados das etapas, estrutura de pastas e decisões técnicas (ADR curto para cada decisão relevante). **PARE e peça aprovação.**
3. **Fase 3 – Tarefas:** gere `specs/tasks.md` com tarefas pequenas (≤ 2h cada), ordenadas, cada uma referenciando os requisitos que atende e com sua *Definition of Done*. **PARE e peça aprovação.**
4. **Fase 4 – Implementação:** execute uma tarefa por vez. Ao concluir cada uma: rode testes/lint, marque `[x]` em `tasks.md` e faça um resumo de 2 linhas. Se algo divergir da spec, **atualize a spec primeiro** e avise.

Regras de contexto:
- A **fonte da verdade** é a pasta `specs/`. O código segue a spec, nunca o contrário.
- Mantenha um `AGENTS.md` (ou `CLAUDE.md`) na raiz com: comandos de build/teste, convenções e decisões já tomadas. Atualize sempre que uma decisão mudar.
- **Não invente** campos do GLPI nem etapas da esteira: use apenas o que está na seção 3. Se faltar informação, pergunte ou crie um ponto configurável.
- Prefira simplicidade: poucas dependências, sem frameworks desnecessários.
- Todo texto visível ao usuário deve estar em **português (pt-BR)**.

---

## 1. OBJETIVO DO PRODUTO

Criar um **painel visual animado, estilo jogo de fazenda (pixel art, vibe Stardew Valley)**, exibido numa TV da equipe, que mostre em tempo (quase) real o andamento das demandas de desenvolvimento cadastradas no **GLPI**.

Cada **demanda é uma plantação**, cada **dev é um fazendeiro NPC** e a **esteira de desenvolvimento é o ciclo de vida da planta**: da semente até a colheita enviada para a cidade.

O painel precisa ser:
- **Legível em 3 segundos:** de longe, dá para ver onde estão os gargalos, o que está atrasado e quem está sobrecarregado.
- **Divertido:** animações, eventos e sons que deixem a equipe com vontade de olhar.
- **Somente leitura:** não altera nada no GLPI.

---

## 2. CONTEXTO TÉCNICO (STACK DEFINIDA)

| Camada | Tecnologia | Observações |
|---|---|---|
| Backend | **Go 1.22+** | `net/http` padrão, WebSocket com `github.com/coder/websocket`, `log/slog`, `embed` para servir o front |
| Front | **TypeScript + Phaser 3 + Vite** | WebGL, pixel art (`pixelArt: true`), resolução base 640x360 escalada para 1920x1080 |
| Integração | **GLPI REST API** (`/apirest.php`) | `initSession` com `App-Token` + `user_token`; `search/Ticket` com critérios |
| Configuração | `config.yaml` | Mapeamento de etapas, devs, intervalos e credenciais (via variáveis de ambiente) |
| Entrega | **Binário único** | Go embute o build do Vite (`//go:embed web/dist`) |
| Ambiente | TV com navegador em tela cheia | Sem interação de mouse; tem de funcionar sozinho por dias sem vazar memória |

Arquitetura alvo:
```
GLPI API ──(polling 30–60s)──► Go: Collector → Normalizer → StateStore → Differ ──WebSocket──► Phaser (cenas)
                                                   ▲
                                         Mock Provider (modo demo)
```

- **Collector:** busca os tickets no GLPI (ou no Mock).
- **Normalizer:** converte ticket GLPI → `Demanda` (modelo de domínio), usando o mapeamento do `config.yaml`.
- **StateStore:** guarda o estado atual em memória e registra quando cada demanda entrou na etapa atual.
- **Differ:** compara o estado novo com o anterior e emite **eventos** (`demanda.criada`, `demanda.moveu`, `demanda.voltou`, `demanda.bloqueou`, `demanda.desbloqueou`, `demanda.entregue`, `sla.estourou`).
- **Front:** ao conectar recebe um `snapshot`; depois recebe só os eventos, e cada evento vira uma animação.

---

## 3. MINHA ESTEIRA (PREENCHER / AJUSTAR)

> ⚠️ Esta seção define o comportamento. Ajuste para refletir **exatamente** o seu fluxo e como ele aparece no GLPI.

### 3.1 Etapas, em ordem

| # | Etapa (nome interno) | Como identifico no GLPI | Elemento da fazenda |
|---|---|---|---|
| 1 | `backlog` | Status = Novo (1) | 🌰 Saco de sementes no celeiro |
| 2 | `triagem` | Status = Em atendimento – atribuído (2), sem a tag de dev | 🧑‍🌾 Fazendeiro escolhe o canteiro e prepara a terra (enxada) |
| 3 | `desenvolvimento` | Categoria/tag `DEV` + status atribuído | 🌱 Broto crescendo (3 estágios conforme o tempo/progresso) |
| 4 | `code_review` | Tag/campo `REVIEW` | 🐔 Vizinho (outro NPC) inspeciona a planta com uma lupa |
| 5 | `testes_qa` | Tag/campo `QA` | 💧 Regador e espantalho; se reprovar, aparece uma **praga** e a planta volta |
| 6 | `homologacao` | Status = Pendente (4) + tag `HML` | 🧺 Colheita na banca da feira, com o cliente provando |
| 7 | `producao` | Status = Solucionado (5) | 📦 Caixa de envio: a carroça leva para a cidade e conta como entrega do dia |
| — | `bloqueado` | Status = Pendente (4) **sem** `HML`, ou tag `BLOQUEADO` | 🪨 Pedra/cerca no canteiro, com a planta acinzentada |
| — | `fechado` | Status = Fechado (6) | Some do mapa (só entra nas métricas) |

> Se a esteira usa um plugin de campos (ex.: *Fields*), categorias ITIL ou tags, informe aqui **qual campo e qual valor** define cada etapa. O agente deve tornar isso 100% configurável em `config.yaml`, sem nada fixo no código.

### 3.2 Pessoas (fazendeiros)

```yaml
devs:
  - glpi_user_id: 12
    nome: "Ana"
    sprite: fazendeira_1
  - glpi_user_id: 15
    nome: "Bruno"
    sprite: fazendeiro_2
  # demanda sem responsável → fica com o "fazendeiro coringa" (NPC genérico)
```

### 3.3 Regras de negócio
- **SLA:** usar `time_to_resolve` do GLPI; se não houver, `sla_padrao_horas` do config, por prioridade.
- **Tempo parado:** calculado a partir de quando a demanda entrou na etapa atual (o StateStore guarda isso; persistir em arquivo JSON para sobreviver a reinícios).
- **Retrabalho:** quando a demanda volta de `testes_qa` ou `code_review` para `desenvolvimento`.
- **WIP saudável por etapa:** configurável (ex.: `desenvolvimento: 2 por dev`).

---

## 4. MAPEAMENTO VISUAL (A "LINGUAGEM" DA FAZENDA)

| Dado | Representação |
|---|---|
| **Etapa** | Região do mapa: celeiro → canteiros → área de inspeção → poço/espantalho → feira → caixa de envio/estrada |
| **Complexidade/tamanho** | Tipo de cultura: 🍓 morango (pequena), 🌽 milho (média), 🎃 abóbora gigante (épico) |
| **Prioridade** | Placa ao lado: muito alta = placa vermelha piscando, alta = laranja, normal = sem placa |
| **Tempo parado na etapa** | Planta progressivamente **murcha**, aparecem **ervas daninhas**; ao passar do limite, **corvos** pousam |
| **SLA estourado** | Planta seca + corvo grasnando + ícone 🔥 sobre a placa |
| **Bloqueado** | Pedra/cerca no canteiro, planta acinzentada |
| **Bug/retrabalho** | Animação de praga (lagarta) comendo a planta, que volta para o canteiro de crescimento |
| **Responsável** | NPC fazendeiro com o nome em cima; anda até a planta em que está trabalhando |
| **Dev sobrecarregado** (> WIP) | NPC com gotas de suor e correndo mais rápido |
| **Dev ocioso** | NPC pescando no lago ou sentado no banco |
| **Saúde geral do time** | **Clima**: sol (ok), nublado (atenção), chuva/tempestade (muitos SLAs estourados) |
| **Hora real** | Ciclo **dia/noite** acompanhando o relógio; à noite, lampiões acesos |
| **Entrega em produção** | Carroça sai pela estrada, moedas sobem no placar e toca um som de "colheita" |

**Interação ao passar/selecionar:** como é TV, cada demanda mostra um rótulo curto (`#1234 • Título truncado`). A cada 15s, um "card de destaque" rotativo mostra os detalhes de uma demanda atrasada.

**HUD fixo (pixel font):**
- Canto superior esquerdo: data, hora e clima.
- Canto superior direito: 🪙 entregas hoje / semana, ⏱️ lead time médio (7 dias), 🔁 taxa de retrabalho.
- Rodapé: contador de demandas por etapa, com alerta quando o WIP passa do limite.

---

## 5. CONTRATOS (O AGENTE DEVE FORMALIZAR NO `design.md`)

### 5.1 Modelo de domínio (Go)
```go
type Etapa string // backlog, triagem, desenvolvimento, code_review, testes_qa, homologacao, producao, bloqueado

type Demanda struct {
    ID             int       `json:"id"`
    Titulo         string    `json:"titulo"`
    Etapa          Etapa     `json:"etapa"`
    EtapaAnterior  Etapa     `json:"etapaAnterior,omitempty"`
    Prioridade     int       `json:"prioridade"`  // 1..6 (GLPI)
    Tamanho        string    `json:"tamanho"`     // P, M, G
    ResponsavelID  int       `json:"responsavelId,omitempty"`
    EntrouNaEtapa  time.Time `json:"entrouNaEtapa"`
    PrazoSLA       *time.Time `json:"prazoSla,omitempty"`
    Bloqueada      bool      `json:"bloqueada"`
    Retrabalhos    int       `json:"retrabalhos"`
}
```

### 5.2 Mensagens WebSocket (`/ws`)
```json
{ "tipo": "snapshot", "ts": "2026-10-09T17:00:00-04:00", "demandas": [/* Demanda[] */], "devs": [/* ... */], "metricas": {/* ... */} }
{ "tipo": "evento", "ts": "...", "evento": "demanda.moveu", "id": 1234, "de": "code_review", "para": "testes_qa" }
{ "tipo": "metricas", "ts": "...", "entregasHoje": 3, "leadTimeMedioH": 41.5, "taxaRetrabalho": 0.12, "clima": "nublado" }
```
- Reconexão automática no front (backoff exponencial, máx. 30s); a cada reconexão chega um novo snapshot.
- Heartbeat a cada 20s.

### 5.3 Endpoints HTTP
- `GET /` → front (embed)
- `GET /ws` → WebSocket
- `GET /healthz` → `ok` + data do último polling bem-sucedido
- `GET /api/estado` → snapshot em JSON (debug)

---

## 6. REQUISITOS NÃO FUNCIONAIS

- **RNF-01:** 60 FPS estáveis com até **80 demandas** e **10 NPCs** numa TV/mini-PC modesto.
- **RNF-02:** Roda **7 dias sem reiniciar** sem vazar memória (objetos Phaser destruídos/reciclados com pool).
- **RNF-03:** Se o GLPI cair, o painel continua com o último estado e mostra um aviso discreto (🐦 "Sem notícias da cidade há X min").
- **RNF-04:** Credenciais **somente** via variáveis de ambiente (`GLPI_URL`, `GLPI_APP_TOKEN`, `GLPI_USER_TOKEN`); nunca no log.
- **RNF-05:** **Modo demo** (`--mock`): gera demandas fictícias que andam pela esteira em velocidade acelerada, para desenvolver sem GLPI e para apresentações.
- **RNF-06:** Testes em Go para Normalizer (mapeamento GLPI → etapa) e Differ (geração de eventos), cobrindo todas as transições, inclusive retrabalho e bloqueio.
- **RNF-07:** Build com um comando: `make build` gera o binário único com o front embutido.

---

## 7. ASSETS

- Começar com **placeholders** (retângulos coloridos + texto) para validar a lógica; a arte vem depois.
- Depois, usar packs de pixel art de fazenda gratuitos (ex.: Kenney.nl, packs "farm/cozy" do itch.io). **Verificar e registrar a licença** de cada asset em `assets/LICENSES.md`.
- Tileset 16x16, spritesheets com animações de andar em 4 direções para os NPCs.
- Sons curtos (colheita, praga, carroça, corvo) com volume baixo, desligáveis via config.
- Mapa montado no **Tiled** (`.tmj`), com camadas nomeadas por etapa, para eu poder redesenhar o layout sem mexer no código.

---

## 8. ESTRUTURA DE PASTAS SUGERIDA

```
fazenda-esteira/
├── AGENTS.md
├── PROMPT.md
├── specs/            requirements.md, design.md, tasks.md
├── config.yaml
├── cmd/fazenda/      main.go
├── internal/
│   ├── glpi/         cliente da API + mock
│   ├── dominio/      Demanda, Etapa, regras
│   ├── estado/       StateStore + persistência JSON
│   ├── diff/         geração de eventos
│   └── ws/           hub WebSocket
├── web/              Vite + TS + Phaser
│   ├── src/scenes/   Boot, Fazenda, HUD
│   ├── src/entities/ Planta, Fazendeiro, Carroca, Corvo
│   ├── src/net/      cliente WS + tipos espelhando o contrato
│   └── public/assets/
└── Makefile
```

---

## 9. ROADMAP EM FASES (MVP PRIMEIRO)

1. **MVP lógico:** Go com mock → WebSocket → Phaser com placeholders mostrando as demandas nas regiões certas e movendo entre elas.
2. **Integração GLPI:** cliente real, mapeamento configurável e persistência do "entrou na etapa".
3. **Fazenda de verdade:** tilemap, sprites, NPCs andando (pathfinding simples em grade), estágios de crescimento.
4. **Vida e eventos:** murchar, corvos, praga/retrabalho, clima, dia/noite, carroça, sons.
5. **HUD e métricas:** entregas, lead time, retrabalho, alertas de WIP.
6. **Polimento:** card rotativo de destaque, performance, teste de 7 dias rodando.

## 10. FORA DE ESCOPO (POR ENQUANTO)
- Editar demandas pelo painel.
- Login/autenticação no painel (roda na rede interna).
- Mobile.
- Múltiplos times/fazendas (deixar o design preparado, mas não implementar).

---

## 11. PRIMEIRA AÇÃO

Comece pela **Fase 1**: leia tudo, gere `specs/requirements.md` e me traga:
1. A lista de requisitos com critérios de aceite.
2. As **dúvidas sobre a minha esteira** (seção 3) que impedem um mapeamento exato.
3. Os riscos técnicos que você enxerga.

Não escreva código antes da minha aprovação.