# Fazenda da Esteira — painel de TV do DevPipe

Painel animado em pixel art (estilo jogo de fazenda) que mostra, em tempo real, a esteira do DevPipe. Pensado para ficar numa TV da equipe, em tela cheia, sem mouse. **Somente leitura**: não altera nenhuma demanda.

- **Abrir:** menu lateral → **Fazenda (TV)**, ou `http://localhost:7420/fazenda.html`.
- **TV na rede:** o DevPipe escuta apenas em `127.0.0.1` por segurança. Para exibir em outra máquina, abra o navegador da própria máquina do DevPipe em tela cheia (F11) e espelhe/estenda para a TV.
- **Parâmetros de URL:** `?demo=1` (dados fictícios para apresentação), `&vel=10` (acelera o demo), `&hora=21` (força a hora, para testar a noite), `&som=1` (liga os sons; navegadores podem bloquear áudio sem interação).

## Linguagem da fazenda

| Esteira do DevPipe | Na fazenda |
|---|---|
| Backlog | Saco de sementes no **Celeiro** |
| Análise · Planejamento · Preparação | Broto no **Preparo da terra** |
| Desenvolvimento | **Canteiros**: broto → muda → cultura crescida, conforme o tempo na etapa |
| Validação (build/testes) | **Poço e espantalho**: gotas de rega enquanto a validação roda |
| Revisão humana | Cesto na **Feira**, com balão "!" aguardando você |
| Concluído | Caixote no **Envio**; a carroça sai pela estrada e as moedas sobem ao placar. Some do mapa no dia seguinte |
| Bloqueado | **Pedreira**: planta acinzentada com uma pedra |
| Devolvido para ajustes | **Pragas**: lagarta comendo a planta |

| Dado | Representação |
|---|---|
| Complexidade (tamanho da especificação gerada pela IA) | 🍓 morango (até 2.500 caracteres), 🌽 milho (até 6.000), 🎃 abóbora (acima) |
| Prioridade | Placa vermelha piscando (Crítica), laranja (Alta), sem placa (Normal/Baixa) |
| Tempo parado na etapa | Planta murcha e inclina, ervas daninhas; no limite da etapa, **corvo** pousa |
| SLA estourado | Planta seca, corvo e 🔥 na placa |
| IA trabalhando | Brilho girando na planta; o fazendeiro **Claude** vai até ela |
| IA ociosa | Claude **pescando** no lago |
| Fila grande (6+ demandas aguardando) | Claude com gotas de suor, andando mais rápido |
| Aguardando você | Fazendeiro **Você** vai até a feira (ou até a especificação a aprovar); sentado no banco quando não há nada pendente |
| Saúde da esteira | **Clima**: sol (sem alertas), nublado, tempestade (4+ alertas: SLAs estourados, bloqueios e WIP acima do limite) |
| Hora real | Ciclo dia/noite; lampiões acesos à noite |

**HUD:** data, hora e clima (esquerda); entregas hoje/semana, lead time médio de 7 dias e taxa de retrabalho (direita); aviso de limite do plano Claude (centro); contadores por etapa no rodapé, em vermelho com `n/limite !` quando passa do WIP. A cada 15 s, um **card de destaque** mostra a próxima demanda atrasada, por gravidade. Se a comunicação com o DevPipe cair, aparece 🐦 "Sem notícias da esteira há X min" e o último estado continua na tela.

## Regras (padrões em `internal/fazenda/fazenda.go`)

- **Tempo na etapa:** a partir do último evento de mudança de etapa registrado no histórico.
- **SLA:** a partir do cadastro: Crítica 8 h, Alta 24 h, Normal 72 h, Baixa 168 h (horas corridas).
- **Limite parado (corvos):** Backlog 72 h, Análise 2 h, Planejamento 24 h, Preparação 2 h, Desenvolvimento 3 h, Validação 2 h, Revisão 24 h, Bloqueado 8 h, Ajustes 24 h.
- **WIP:** Backlog 20, Desenvolvimento = nº de execuções em paralelo, Revisão 5, Bloqueado 3, Ajustes 5.
- **Entrega:** aprovação na revisão humana (uma por demanda por dia). **Lead time:** do cadastro à aprovação. **Retrabalho:** entregas que passaram por "Devolver para ajustes".

## Arquitetura

```
SQLite (demandas, eventos, revisões) → GET /api/fazenda (internal/fazenda: regras e métricas)
                                          ↑ releitura a cada evento do SSE /api/stream e a cada 10 s
Navegador: fazenda.js (Phaser 3) → compara o estado novo com o anterior e anima as transições
```

- **Phaser 3.90** embutido no executável (`internal/web/static/vendor/phaser.min.js`, licença MIT em `PHASER-LICENSE.md`).
- **Arte 100% gerada em código** (pixel art desenhada a partir de matrizes de cores): não há assets de terceiros nem licenças a controlar.
- Mundo em 640×360, desenhado num canvas 1920×1080 com câmera 3× (pixel art nítida e texto legível de longe). WebGL, com fallback automático para Canvas.
- **Estabilidade:** objetos de plantas reaproveitados e destruídos com suas animações, fila de animações limitada, gotas de chuva em pool. Teste de estresse com o demo acelerado 25× por mais de 3 minutos: memória JS estável (~13 MB) e número de objetos e animações sem crescimento.

## Adaptações em relação ao PROMPT original

O PROMPT da fazenda previa os dados vindos do **GLPI**. Esta implementação usa a **esteira do próprio DevPipe** como fonte. O número do ticket do GLPI aparece no rótulo de cada planta, porque é o mesmo número cadastrado na demanda. Outras decisões:

| PROMPT | Implementação | Motivo |
|---|---|---|
| WebSocket `/ws` com eventos do servidor | SSE `/api/stream` (já existente) + `/api/fazenda`; as transições são detectadas no navegador | Reaproveita o canal em tempo real do DevPipe, sem nova dependência |
| TypeScript + Vite | JavaScript puro, sem etapa de build | Mantém o build do DevPipe simples (`go build`), sem Node na máquina |
| Tiled (`.tmj`) | Layout em código (`REG` em `fazenda.js`) | Primeira versão; o layout está concentrado em uma tabela |
| Fazendeiros = devs do GLPI | Fazendeiros = **Claude** (IA) e **Você** (revisor) | São quem trabalha na esteira do DevPipe |

## Próximos passos possíveis

1. Integração opcional com o GLPI (consultar status e responsável do ticket vinculado).
2. Regras (SLA, limites, WIP) editáveis na tela de Configuração.
3. Layout editável no Tiled.
4. Teste de 7 dias numa TV real (estabilidade e FPS no hardware de destino).
