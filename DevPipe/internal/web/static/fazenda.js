/* Fazenda da Esteira — painel de TV do DevPipe (Phaser 3, somente leitura).
 * Cada demanda é uma plantação, a IA e o revisor humano são os fazendeiros e
 * as etapas da esteira são o ciclo de vida da planta. Toda a pixel art é
 * desenhada em código (sem assets de terceiros).
 * Parâmetros de URL: ?demo=1 (dados fictícios acelerados), ?hora=20 (força a hora, para testes).
 */
"use strict";

const W = 640, H = 360, ZOOM = 3; // mundo em 640x360, desenhado em 1920x1080 (texto nítido, pixel art preservada)
const PARAMS = new URLSearchParams(location.search);
const DEMO = PARAMS.get("demo") === "1";
const HORA_FIXA = PARAMS.has("hora") ? Number(PARAMS.get("hora")) : null;
const VEL = Math.min(50, Math.max(1, Number(PARAMS.get("vel")) || 1)); // modo demo: multiplicador de velocidade

// ------------------------------------------------------------------ paleta e sprites
const PAL = {
  k: "#2b1d14", w: "#f4f1e8", s: "#f1c27d", S: "#c98f52", h: "#d9a441", H: "#a8742a", b: "#3b6fb6", B: "#264a80",
  r: "#d94b4b", R: "#8e2a2a", g: "#5fae3b", G: "#2f6d2a", m: "#9bd46a", y: "#f2d14b", Y: "#c9a227", o: "#e8842e",
  O: "#b45d1a", n: "#8b5a2b", N: "#5c3a1a", t: "#c49a6c", e: "#9aa0a6", E: "#5f656b", c: "#4fb3d9", C: "#2a7fa8",
  p: "#e98fb0", f: "#ffd23f", F: "#ff6a00", l: "#fff3b0", x: "#1a1a1a", v: "#7a4ea8",
};

const SPR = {
  fazendeiro: [
    "...hhhh...", "..hhhhhh..", ".HHHHHHHH.", "...ssss...", "...sxsx...", "...ssss...", "..oooooo..",
    ".soooooos.", ".soooooos.", "..oooooo..", "..BBBBBB..", "..BB..BB..", "..BB..BB..", "..NN..NN..",
  ],
  sementes: [
    "...kkkk...", "..kttttk..", "..ktnntk..", ".kttttttk.", "kttttttttk", "kttnnnnttk", "ktnnnnnntk",
    "kttnnnnttk", ".kttttttk.", "..kkkkkk..",
  ],
  broto: ["...gg...", "..gmmg..", "...gg...", "...G....", "...G....", "..NNNN.."],
  muda: ["..m..m..", ".mgm.mg.", "..gGgg..", "...GG...", "...G....", "...G....", "..NNNN.."],
  morango: [
    "...gGg....", "..gmGmg...", ".gmgGgmg..", "..rrGrr...", ".rRrrrRr..", ".rrwrrwr..", "..rrrrr...",
    "...rRr....", "....r.....", "..NNNNNN..",
  ],
  milho: [
    "....m.....", "...mgm....", "..mgGgm...", "...yYy....", "..gyYyg...", "..gYyYg...", "..gyYyg...",
    "..gYyYg...", "...yYy....", "...gGg....", "..g.G.g...", ".g..G..g..", "....G.....", "....G.....",
    "...NNNN...",
  ],
  abobora: [
    "......gG......", ".....gG.......", "...oOoOoOo....", "..oOooOooOo...", ".oOooOooOooO..", ".oOooOooOooO..",
    ".oOooOooOooO..", ".oOooOooOooO..", "..oOooOooOo...", "...oOOOOOo....", ".NNNNNNNNNNNN.",
  ],
  pedra: ["...EEEE.....", "..EeeeeEE...", ".EeeweeeeE..", "EeeeeeeeeeE.", "EeeeEeeeeeEE", "EEeeeeeeEeeE", ".EEEEEEEEEE."],
  corvo1: ["..xx......", ".xxxx.....", "xxwxxxx...", ".xxxxxxx..", "..xxxxxxx.", "...xxxxx.y", "....x.x...."],
  corvo2: ["..xx.x....", ".xxxxxx...", "xxwxxxxx..", ".xxxxxxx..", "..xxxxxxx.", "...xxxxx.y", "....x.x...."],
  lagarta: ["..m.m...", ".gggggg.", "gGgGgGgx", ".m.m.m.."],
  moeda: [".yyyy.", "yYyyYy", "yYffYy", "yYffYy", "yYyyYy", ".yyyy."],
  chama: ["..f..", ".fFf.", ".fFf.", "fFFFf", "fFfFf", ".fff."],
  erva: ["g.g.g", ".gGg.", "g.G.g", ".GGG."],
  cesto: [".nNnNnNn.", "nttttttttn", "nNnNnNnNn", "nnnnnnnnn", ".nnnnnnn."],
  caixa: ["NNNNNNNNNNNN", "NnnnnnnnnnnN", "NnNNNNNNNNnN", "NnnnnnnnnnnN", "NnNNNNNNNNnN", "NnnnnnnnnnnN", "NNNNNNNNNNNN"],
  passaro: ["..ww..", ".wwww.", "wwkwwo", ".wwww.", "..w.w."],
  gota: [".c.", "ccc", ".c."],
  sol: ["..y..y..", "...yy...", "y.yffy.y", ".yffffy.", ".yffffy.", "y.yffy.y", "...yy...", "..y..y.."],
  nuvem: ["...ww...", "..wwww..", ".wwwwwww", "wwwwwwww", ".eeeeee."],
  tempestade: ["...EE...", "..EEEE..", ".EEEEEEE", "EEEEEEEE", ".EEfEEE.", "...ff...", "..ff....", ".f.c.c.c"],
};

function criarTexturas(scene) {
  for (const [nome, linhas] of Object.entries(SPR)) pintar(scene, nome, linhas, null);
  // variante do fazendeiro humano (camisa azul, chapéu escuro)
  pintar(scene, "humano", SPR.fazendeiro, { o: "b", h: "N", H: "N", B: "E" });
  pintar(scene, "humanoSentado", SPR.fazendeiro.slice(0, 11).concat(["..BBBBBBBB", "..NN....NN"]), { o: "b", h: "N", H: "N", B: "E" });
  pintar(scene, "fazendeiroSentado", SPR.fazendeiro.slice(0, 11).concat(["..BBBBBBBB", "..NN....NN"]), null);
  // cenário desenhado com primitivas
  const g = scene.make.graphics({ x: 0, y: 0, add: false });
  desenharCenario(g);
  g.generateTexture("cenario", W, H);
  g.destroy();
}

function pintar(scene, nome, linhas, troca) {
  const h = linhas.length, w = Math.max(...linhas.map((l) => l.length));
  const tex = scene.textures.createCanvas(nome, w, h);
  const ctx = tex.getContext();
  linhas.forEach((linha, y) => {
    [...linha].forEach((ch, x) => {
      if (ch === "." || ch === " ") return;
      const c = troca && troca[ch] ? troca[ch] : ch;
      ctx.fillStyle = PAL[c] || "#ff00ff";
      ctx.fillRect(x, y, 1, 1);
    });
  });
  tex.refresh();
}

// ------------------------------------------------------------------ mapa
// Regiões (x, y, largura, altura). A ordem segue a esteira.
const REG = {
  backlog: { x: 6, y: 76, w: 104, h: 94, util: 72, nome: "Celeiro", sub: "Backlog" },
  preparo: { x: 116, y: 34, w: 98, h: 136, nome: "Preparo", sub: "Análise · Plano · Preparo" },
  desenvolvimento: { x: 220, y: 34, w: 140, h: 136, nome: "Canteiros", sub: "Desenvolvimento" },
  validacao: { x: 366, y: 34, w: 80, h: 136, util: 76, nome: "Poço", sub: "Validação" },
  revisao: { x: 452, y: 34, w: 96, h: 136, util: 78, nome: "Feira", sub: "Revisão humana" },
  concluido: { x: 554, y: 34, w: 80, h: 136, util: 76, nome: "Envio", sub: "Concluído hoje" },
  lago: { x: 6, y: 196, w: 134, h: 134, nome: "Lago", sub: "" },
  bloqueado: { x: 146, y: 196, w: 140, h: 134, nome: "Pedreira", sub: "Bloqueado" },
  ajustes: { x: 292, y: 196, w: 140, h: 134, nome: "Pragas", sub: "Devolvido p/ ajustes" },
};
const ESTRADA_Y = 180;
const REGIAO_DA_ETAPA = {
  backlog: "backlog", analise: "preparo", planejamento: "preparo", preparacao: "preparo", desenvolvimento: "desenvolvimento",
  validacao: "validacao", revisao: "revisao", concluido: "concluido", bloqueado: "bloqueado", ajustes: "ajustes",
};
const ETAPAS_RODAPE = [
  ["backlog", "Backlog"], ["analise", "Análise"], ["planejamento", "Plano"], ["preparacao", "Preparo"], ["desenvolvimento", "Desenv."],
  ["validacao", "Validação"], ["revisao", "Revisão"], ["bloqueado", "Bloq."], ["ajustes", "Ajustes"], ["concluido", "Entregues"],
];

function hex(c) { return Phaser.Display.Color.HexStringToColor(c).color; }

function desenharCenario(g) {
  // grama com textura
  g.fillStyle(hex("#5a9a3a")); g.fillRect(0, 0, W, H);
  for (let i = 0; i < 900; i++) {
    const x = (i * 97) % W, y = (i * 53 + (i % 7) * 31) % H;
    g.fillStyle(hex(i % 3 ? "#4f8c33" : "#6aac45")); g.fillRect(x, y, 1, 2);
  }
  // estrada principal (até a cidade, à direita)
  g.fillStyle(hex("#c9a46a")); g.fillRect(0, ESTRADA_Y - 6, W, 14);
  g.fillStyle(hex("#b38d55"));
  for (let x = 0; x < W; x += 9) g.fillRect(x, ESTRADA_Y - 6 + ((x / 9) % 3) * 4, 3, 1);
  // celeiro
  const bx = 14, by = 26;
  g.fillStyle(hex("#8e2a2a")); g.fillRect(bx, by + 14, 56, 30);
  g.fillStyle(hex("#d94b4b")); g.fillRect(bx + 2, by + 16, 52, 26);
  g.fillStyle(hex("#5c3a1a")); g.fillTriangle(bx - 4, by + 16, bx + 28, by - 2, bx + 60, by + 16);
  g.fillStyle(hex("#f4f1e8")); g.fillRect(bx + 20, by + 24, 16, 18);
  g.fillStyle(hex("#8e2a2a")); g.fillRect(bx + 22, by + 26, 12, 16);
  g.lineStyle(1, hex("#f4f1e8")); g.lineBetween(bx + 22, by + 26, bx + 34, by + 42); g.lineBetween(bx + 34, by + 26, bx + 22, by + 42);
  // canteiros (solo arado)
  for (const k of ["preparo", "desenvolvimento", "bloqueado", "ajustes"]) {
    const r = REG[k];
    g.fillStyle(hex(k === "bloqueado" ? "#6e6a5e" : "#7a5230")); g.fillRect(r.x + 2, r.y + 12, r.w - 4, r.h - 16);
    g.fillStyle(hex(k === "bloqueado" ? "#5f5b50" : "#6a4527"));
    for (let y = r.y + 16; y < r.y + r.h - 6; y += 6) g.fillRect(r.x + 4, y, r.w - 8, 2);
  }
  // cercas da pedreira e das pragas
  for (const k of ["bloqueado", "ajustes"]) {
    const r = REG[k];
    g.fillStyle(hex("#c49a6c"));
    g.fillRect(r.x, r.y + 10, r.w, 2); g.fillRect(r.x, r.y + r.h - 2, r.w, 2);
    for (let x = r.x; x <= r.x + r.w; x += 10) g.fillRect(x, r.y + 8, 2, r.h - 6);
  }
  // poço
  const p = REG.validacao;
  g.fillStyle(hex("#5f656b")); g.fillRect(p.x + 50, p.y + 118, 22, 14);
  g.fillStyle(hex("#2a7fa8")); g.fillRect(p.x + 52, p.y + 120, 18, 4);
  g.fillStyle(hex("#5c3a1a")); g.fillRect(p.x + 50, p.y + 106, 2, 12); g.fillRect(p.x + 70, p.y + 106, 2, 12); g.fillRect(p.x + 48, p.y + 104, 26, 3);
  // espantalho
  g.fillStyle(hex("#5c3a1a")); g.fillRect(p.x + 18, p.y + 108, 2, 26); g.fillRect(p.x + 10, p.y + 116, 18, 2);
  g.fillStyle(hex("#d9a441")); g.fillRect(p.x + 15, p.y + 104, 8, 6);
  g.fillStyle(hex("#7a4ea8")); g.fillRect(p.x + 14, p.y + 118, 10, 8);
  // feira (barraca listrada)
  const f = REG.revisao;
  g.fillStyle(hex("#8b5a2b")); g.fillRect(f.x + 6, f.y + 126, f.w - 12, 10);
  g.fillStyle(hex("#5c3a1a")); g.fillRect(f.x + 8, f.y + 112, 3, 14); g.fillRect(f.x + f.w - 11, f.y + 112, 3, 14);
  for (let i = 0; i < 8; i++) { g.fillStyle(hex(i % 2 ? "#f4f1e8" : "#d94b4b")); g.fillRect(f.x + 4 + i * 11, f.y + 104, 11, 8); }
  // caixa de envio e placa da cidade
  const e = REG.concluido;
  g.fillStyle(hex("#5c3a1a")); g.fillRect(e.x + 48, e.y + 112, 28, 18);
  g.fillStyle(hex("#8b5a2b")); g.fillRect(e.x + 50, e.y + 114, 24, 14);
  g.fillStyle(hex("#5c3a1a")); g.fillRect(W - 14, ESTRADA_Y - 26, 2, 20);
  g.fillStyle(hex("#c49a6c")); g.fillRect(W - 34, ESTRADA_Y - 30, 30, 9);
  // lago
  const l = REG.lago;
  g.fillStyle(hex("#2a7fa8")); g.fillEllipse(l.x + 66, l.y + 76, 112, 70);
  g.fillStyle(hex("#4fb3d9")); g.fillEllipse(l.x + 66, l.y + 74, 100, 58);
  g.fillStyle(hex("#8fd3ee")); g.fillRect(l.x + 40, l.y + 66, 12, 1); g.fillRect(l.x + 78, l.y + 84, 16, 1);
  // praça: banco, árvores, lampiões
  g.fillStyle(hex("#5c3a1a")); g.fillRect(470, 300, 34, 3); g.fillRect(472, 303, 2, 5); g.fillRect(500, 303, 2, 5);
  for (const [tx, ty] of [[452, 206], [612, 290], [446, 300]]) {
    g.fillStyle(hex("#5c3a1a")); g.fillRect(tx + 6, ty + 12, 4, 10);
    g.fillStyle(hex("#2f6d2a")); g.fillCircle(tx + 8, ty + 8, 9);
    g.fillStyle(hex("#5fae3b")); g.fillCircle(tx + 6, ty + 6, 5);
  }
  for (const [lx, ly] of LAMPIOES) {
    g.fillStyle(hex("#2b1d14")); g.fillRect(lx, ly, 2, 14);
    g.fillStyle(hex("#f2d14b")); g.fillRect(lx - 1, ly - 3, 4, 4);
  }
}
const LAMPIOES = [[112, ESTRADA_Y - 22], [362, ESTRADA_Y - 22], [550, ESTRADA_Y - 22], [440, 280], [140, 300]];

// vagas dentro de uma região (grade), com modo compacto quando lota
function vagas(reg, n) {
  const top = reg.y + 22, left = reg.x + 4, w = reg.w - 8, h = (reg.util || reg.h - 24);
  let cw = 44, ch = 38;
  let cols = Math.max(1, Math.floor(w / cw)), rows = Math.max(1, Math.floor(h / ch));
  let compacto = false;
  if (n > cols * rows) { cw = 34; ch = 24; cols = Math.max(1, Math.floor(w / cw)); rows = Math.max(1, Math.floor(h / ch)); compacto = true; }
  const out = [];
  for (let i = 0; i < Math.min(n, cols * rows); i++) {
    const c = i % cols, r = Math.floor(i / cols);
    out.push({ x: left + c * cw + cw / 2, y: top + r * ch + (compacto ? 14 : 18) });
  }
  return { pos: out, compacto, cap: cols * rows };
}

// ------------------------------------------------------------------ dados
class Fonte {
  constructor(onEstado) {
    this.onEstado = onEstado;
    this.ultimoOk = Date.now();
    this.online = true;
    this.timer = null;
  }
  iniciar() {
    if (DEMO) { this.demo = new Demo(); this.onEstado(this.demo.estado()); setInterval(() => { this.demo.passo(); this.onEstado(this.demo.estado()); this.ultimoOk = Date.now(); }, 2500 / VEL); return; }
    this.buscar();
    setInterval(() => this.buscar(), 10000);
    this.conectarSSE();
  }
  agendar() { clearTimeout(this.timer); this.timer = setTimeout(() => this.buscar(), 700); }
  async buscar() {
    try {
      const r = await fetch("/api/fazenda", { cache: "no-store" });
      if (!r.ok) throw new Error(r.status);
      const j = await r.json();
      this.ultimoOk = Date.now(); this.online = true;
      this.onEstado({ ...j.estado, uso: j.uso, filaPausada: j.filaPausada });
    } catch { this.online = false; }
  }
  conectarSSE() {
    // EventSource reconecta sozinho; cada mensagem de demanda/evento dispara nova leitura do estado
    this.es = new EventSource("/api/stream");
    this.es.onmessage = (m) => { try { const x = JSON.parse(m.data); if (x.type === "demand" || x.type === "reload") this.agendar(); } catch { /* ignora */ } };
    this.es.onerror = () => { this.online = false; };
    this.es.onopen = () => { this.online = true; this.agendar(); };
  }
}

// Modo demo: demandas fictícias percorrendo a esteira em velocidade acelerada.
class Demo {
  constructor() {
    const titulos = ["Filtro por período no relatório", "Cupom de desconto", "Ajuste no cálculo de férias", "Exportar folha em CSV", "Tela de eventos analógicos",
      "Validação de CPF no cadastro", "Novo layout do contracheque", "Integração com o eSocial", "Corrigir arredondamento", "Permissões por perfil",
      "Log de auditoria", "Importação de rubricas", "Ajuste em CadEventos", "Relatório de horas extras", "Envio de e-mail de aviso", "Busca por matrícula"];
    const etapas = ["backlog", "backlog", "analise", "planejamento", "desenvolvimento", "desenvolvimento", "validacao", "revisao", "revisao", "bloqueado", "ajustes", "concluido", "backlog", "desenvolvimento", "revisao", "preparacao"];
    const agora = Date.now();
    this.ds = titulos.map((t, i) => ({
      id: i + 1, ticket: String(538600 + i * 7), titulo: t, etapa: etapas[i], prioridade: 1 + (i % 4), tamanho: "PMG"[i % 3],
      responsavel: "Claude", executando: false, aguardaHumano: false, entrouNaEtapa: new Date(agora - (i * 37 % 300) * 60000).toISOString(),
      murcha: (i * 13 % 100) / 100, slaEstourado: i % 6 === 0, retrabalhos: i % 5 === 0 ? 1 : 0, tentativas: 1, progresso: 0, mensagem: "",
    }));
    this.entregas = 2; this.semana = 9; this.seq = 0;
    this.marcar();
  }
  marcar() {
    let execs = 0;
    for (const d of this.ds) {
      d.executando = ["analise", "preparacao", "desenvolvimento", "validacao"].includes(d.etapa) && execs++ < 1;
      d.aguardaHumano = d.etapa === "revisao";
    }
  }
  passo() {
    const ordem = ["backlog", "analise", "planejamento", "preparacao", "desenvolvimento", "validacao", "revisao", "concluido"];
    const d = this.ds[Math.floor(Math.random() * this.ds.length)];
    const sorte = Math.random();
    if (d.etapa === "concluido") { d.etapa = "backlog"; d.id = 100 + ++this.seq; d.ticket = String(539000 + this.seq); d.retrabalhos = 0; d.slaEstourado = false; d.murcha = 0; }
    else if (d.etapa === "bloqueado" || d.etapa === "ajustes") d.etapa = d.etapa === "bloqueado" ? "desenvolvimento" : "analise";
    else if (d.etapa === "revisao" && sorte < 0.3) { d.etapa = "ajustes"; d.retrabalhos++; }
    else if (d.etapa === "validacao" && sorte < 0.2) d.etapa = "bloqueado";
    else { d.etapa = ordem[ordem.indexOf(d.etapa) + 1] || "concluido"; if (d.etapa === "concluido") { this.entregas++; this.semana++; } }
    d.entrouNaEtapa = new Date().toISOString(); d.murcha = Math.min(1, Math.random() * 1.3); d.slaEstourado = d.slaEstourado || Math.random() < 0.05;
    this.marcar();
  }
  estado() {
    const contagem = {}; let sla = 0, bloq = 0;
    for (const d of this.ds) { if (d.etapa !== "concluido") contagem[d.etapa] = (contagem[d.etapa] || 0) + 1; if (d.slaEstourado) sla++; if (d.etapa === "bloqueado") bloq++; }
    const alertas = sla + bloq;
    return {
      demandas: this.ds.map((d) => ({ ...d, paradoH: (Date.now() - Date.parse(d.entrouNaEtapa)) / 3600000 * 40 })), contagem,
      wip: { backlog: 20, desenvolvimento: 2, revisao: 5, bloqueado: 3, ajustes: 5 },
      metricas: { entregasHoje: this.entregas, entregasSemana: this.semana, leadTimeMedioH: 41.5, taxaRetrabalho: 0.12, clima: alertas === 0 ? "sol" : alertas >= 4 ? "tempestade" : "nublado", slaEstourados: sla, bloqueadas: bloq },
      uso: { fiveHourPct: 42, sevenDayPct: 18 }, filaPausada: false,
    };
  }
}

// ------------------------------------------------------------------ cena
class Fazenda extends Phaser.Scene {
  constructor() { super("fazenda"); this.plantas = new Map(); this.estado = null; this.fila = []; this.cardIdx = 0; }

  create() {
    criarTexturas(this);
    this.cameras.main.setZoom(ZOOM).centerOn(W / 2, H / 2);
    if (PARAMS.has("diag")) window.__fazenda = this; // diagnóstico (testes de estresse)
    this.add.image(0, 0, "cenario").setOrigin(0).setDepth(0);
    this.desenharPlacas();
    this.camadaPlantas = this.add.layer().setDepth(10);
    this.criarNPCs();
    this.criarClima();
    this.criarHUD();
    this.fonte = new Fonte((e) => this.atualizar(e));
    this.fonte.iniciar();
    this.time.addEvent({ delay: 1000, loop: true, callback: () => this.tick() });
    this.time.addEvent({ delay: 15000, loop: true, callback: () => this.rotacionarCard() });
    this.tick();
  }

  txt(x, y, s, size = 8, cor = "#fff8e7", extra = {}) {
    return this.add.text(x, y, s, {
      fontFamily: "Consolas, 'Courier New', monospace", fontSize: size + "px", color: cor, fontStyle: extra.bold === false ? "normal" : "bold",
      stroke: extra.stroke || "#2b1d14", strokeThickness: extra.stroke === "" ? 0 : 1.5, align: extra.align || "left",
    }).setResolution(ZOOM);
  }

  desenharPlacas() {
    for (const r of Object.values(REG)) {
      const c = this.add.container(r.x + 2, r.y - 2).setDepth(5);
      const t1 = this.txt(4, 1, r.nome, 7, "#fff3b0");
      const largura = Math.max(t1.width, r.sub ? this.txt(0, 0, r.sub, 6).setVisible(false).width : 0) + 8;
      const fundo = this.add.rectangle(0, 0, Math.min(largura, r.w - 4), r.sub ? 21 : 13, hex("#5c3a1a"), 0.92).setOrigin(0).setStrokeStyle(1, hex("#2b1d14"));
      c.add([fundo, t1]);
      if (r.sub) c.add(this.txt(4, 10, r.sub.length > 34 ? r.sub.slice(0, 33) + "…" : r.sub, 6, "#e8d9b0", { bold: false }));
    }
  }

  // ---------------------------------------------------------------- plantas
  atualizar(est) {
    const anterior = this.estado;
    this.estado = est;
    const vistos = new Set();
    // agrupa por região para calcular vagas
    const porRegiao = {};
    for (const d of est.demandas) (porRegiao[REGIAO_DA_ETAPA[d.etapa] || "backlog"] ||= []).push(d);
    for (const [regNome, lista] of Object.entries(porRegiao)) {
      const v = vagas(REG[regNome], lista.length);
      lista.forEach((d, i) => {
        vistos.add(d.id);
        const pos = v.pos[i];
        let p = this.plantas.get(d.id);
        const antes = p ? p.dado : null;
        if (!p) {
          p = this.criarPlanta(d);
          this.plantas.set(d.id, p);
          if (anterior) this.enfileirar(() => this.animCriada(p)); // evita "chuva" de criações no primeiro carregamento
        }
        p.oculta = !pos;
        p.compacto = v.compacto;
        if (pos) p.alvo = pos;
        this.desenharPlanta(p, d);
        if (antes && antes.etapa !== d.etapa) this.enfileirar(() => this.animTransicao(p, antes, d));
        else if (pos && !p.animando) { p.c.setPosition(pos.x, pos.y); p.c.setVisible(true); }
        if (antes && !antes.slaEstourado && d.slaEstourado) this.enfileirar(() => this.animCorvo(p));
      });
      // "+N" quando a região lota
      const extra = lista.length - v.pos.length;
      this.contadorExtra(regNome, extra);
    }
    for (const k of Object.keys(REG)) if (!porRegiao[k]) this.contadorExtra(k, 0);
    for (const [id, p] of this.plantas) if (!vistos.has(id)) { this.destruirPlanta(p); this.plantas.delete(id); }
    for (const p of this.plantas.values()) if (p.oculta) p.c.setVisible(false);
    this.atualizarNPCs();
    this.atualizarHUD();
    if (!anterior) this.rotacionarCard(); // card de destaque já na abertura
  }

  contadorExtra(reg, n) {
    this.extras ||= {};
    let t = this.extras[reg];
    if (n <= 0) { if (t) t.setVisible(false); return; }
    const r = REG[reg];
    if (!t) { t = this.txt(r.x + r.w - 4, r.y + r.h - 4, "", 8, "#fff3b0").setOrigin(1, 1).setDepth(30); this.extras[reg] = t; }
    t.setText("+" + n).setVisible(true);
  }

  criarPlanta(d) {
    const c = this.add.container(0, 0);
    this.camadaPlantas.add(c);
    return { id: d.id, c, dado: null, assinatura: "", animando: false };
  }

  destruirPlanta(p) {
    this.tweens.killTweensOf(p.c);
    p.c.each((o) => this.tweens.killTweensOf(o));
    p.c.destroy(true);
  }

  // escolhe o sprite principal conforme etapa, tamanho e tempo
  spriteDaPlanta(d) {
    const crescido = { P: "morango", M: "milho", G: "abobora" }[d.tamanho] || "milho";
    switch (d.etapa) {
      case "backlog": return "sementes";
      case "analise": case "planejamento": return "broto";
      case "preparacao": return "broto";
      case "desenvolvimento": {
        const t = d.paradoH || 0;
        return t < 0.25 ? "broto" : t < 1 ? "muda" : crescido;
      }
      case "validacao": case "revisao": case "concluido": case "bloqueado": return crescido;
      case "ajustes": return "muda";
    }
    return "broto";
  }

  desenharPlanta(p, d) {
    const assinatura = [d.etapa, d.tamanho, d.prioridade, d.executando, d.aguardaHumano, d.slaEstourado, Math.round((d.murcha || 0) * 4), p.compacto, this.spriteDaPlanta(d), d.ticket, d.titulo].join("|");
    p.dado = d;
    if (assinatura === p.assinatura) return;
    p.assinatura = assinatura;
    p.c.each((o) => this.tweens.killTweensOf(o));
    p.c.removeAll(true);
    const add = (o) => { p.c.add(o); return o; };

    const murcha = d.murcha || 0;
    // canteiro
    if (!["backlog", "revisao", "concluido"].includes(d.etapa)) add(this.add.rectangle(0, 2, 16, 4, hex("#5a3a20")).setOrigin(0.5));
    // planta
    const spr = add(this.add.image(0, 2, this.spriteDaPlanta(d)).setOrigin(0.5, 1));
    if (d.etapa === "bloqueado") spr.setTint(0x9a9a9a);
    else if (d.slaEstourado) spr.setTint(0xb08a5a);
    else if (murcha > 0.35) spr.setTint(Phaser.Display.Color.GetColor(255, Math.round(255 - murcha * 90), Math.round(255 - murcha * 150)));
    if (murcha > 0.6 || d.slaEstourado) spr.setAngle(d.slaEstourado ? 14 : 7); // murchando
    if (d.etapa === "revisao") add(this.add.image(0, 3, "cesto").setOrigin(0.5, 1));
    if (d.etapa === "concluido") add(this.add.image(0, 3, "caixa").setOrigin(0.5, 1));
    if (d.etapa === "bloqueado") add(this.add.image(6, 3, "pedra").setOrigin(0.5, 1));
    if (d.etapa === "ajustes") { const l = add(this.add.image(-2, -4, "lagarta")); this.tweens.add({ targets: l, x: 3, yoyo: true, repeat: -1, duration: 900 }); }
    if (d.etapa === "validacao" && d.executando) {
      for (let i = 0; i < 3; i++) { const gt = add(this.add.image(-4 + i * 4, -18, "gota")); this.tweens.add({ targets: gt, y: -4, alpha: 0, duration: 700, delay: i * 230, repeat: -1 }); }
    }
    // ervas daninhas e corvos (tempo parado)
    if (murcha > 0.5 && !p.compacto) { add(this.add.image(-9, 2, "erva").setOrigin(0.5, 1)); if (murcha > 0.8) add(this.add.image(9, 2, "erva").setOrigin(0.5, 1)); }
    if (murcha >= 1 || d.slaEstourado) {
      const cv = add(this.add.image(7, -spr.height - 2, "corvo1").setOrigin(0.5, 1));
      this.tweens.add({ targets: cv, y: cv.y - 1, yoyo: true, repeat: -1, duration: 600 });
    }
    // placa de prioridade
    if (d.prioridade <= 2) {
      add(this.add.rectangle(-10, -6, 1, 8, hex("#5c3a1a")).setOrigin(0.5, 0));
      const placa = add(this.add.rectangle(-10, -8, 6, 5, d.prioridade === 1 ? hex("#e03b3b") : hex("#e8842e")).setStrokeStyle(1, hex("#2b1d14")));
      if (d.prioridade === 1) this.tweens.add({ targets: placa, alpha: 0.25, yoyo: true, repeat: -1, duration: 500 });
      if (d.slaEstourado) { const fo = add(this.add.image(-10, -14, "chama")); this.tweens.add({ targets: fo, scaleY: 1.25, yoyo: true, repeat: -1, duration: 220 }); }
    } else if (d.slaEstourado) {
      const fo = add(this.add.image(-9, -10, "chama")); this.tweens.add({ targets: fo, scaleY: 1.25, yoyo: true, repeat: -1, duration: 220 });
    }
    // IA trabalhando / aguardando você
    if (d.executando) {
      const brilho = add(this.add.star(8, -spr.height - 4, 4, 1, 3, hex("#fff3b0")));
      this.tweens.add({ targets: brilho, angle: 360, repeat: -1, duration: 1500 });
    }
    if (d.aguardaHumano) {
      const bal = add(this.add.container(8, -spr.height - 6));
      bal.add([this.add.rectangle(0, 0, 7, 8, hex("#fff3b0")).setStrokeStyle(1, hex("#2b1d14")), this.txt(0, 0, "!", 7, "#d94b4b", { stroke: "" }).setOrigin(0.5)]);
      this.tweens.add({ targets: bal, y: bal.y - 2, yoyo: true, repeat: -1, duration: 500 });
    }
    // rótulo
    const t1 = add(this.txt(0, 3, "#" + curto(d.ticket, 9), p.compacto ? 5 : 6, d.slaEstourado ? "#ffb3a8" : "#fff8e7").setOrigin(0.5, 0));
    if (!p.compacto) add(this.txt(0, 10, curto(d.titulo, 12), 5, "#f3e3bd", { bold: false }).setOrigin(0.5, 0));
    t1.setDepth(2);
  }

  // ---------------------------------------------------------------- animações
  enfileirar(fn) {
    if (this.fila.length > 40) this.fila.shift(); // nunca acumula indefinidamente
    this.fila.push(fn);
    if (!this.rodandoFila) this.proximo();
  }
  proximo() {
    const fn = this.fila.shift();
    if (!fn) { this.rodandoFila = false; return; }
    this.rodandoFila = true;
    fn();
    this.time.delayedCall(450, () => this.proximo());
  }

  animCriada(p) {
    if (!p.alvo) return;
    p.c.setPosition(p.alvo.x, p.alvo.y - 30).setAlpha(0).setVisible(true);
    this.tweens.add({ targets: p.c, y: p.alvo.y, alpha: 1, duration: 600, ease: "Bounce.Out" });
  }

  animTransicao(p, de, para) {
    if (!p.alvo) return;
    const destino = p.alvo;
    p.animando = true;
    const mover = (dur = 1400) => this.tweens.add({
      targets: p.c, x: destino.x, y: destino.y, duration: dur, ease: "Sine.InOut",
      onComplete: () => { p.animando = false; if (p.alvo) p.c.setPosition(p.alvo.x, p.alvo.y); },
    });
    if (para.etapa === "ajustes") { // praga: lagarta come a planta e ela volta
      const lag = this.add.image(p.c.x - 20, p.c.y - 4, "lagarta").setDepth(40);
      this.tweens.add({ targets: lag, x: p.c.x, duration: 700, onComplete: () => { lag.destroy(); this.tweens.add({ targets: p.c, scaleY: 0.6, yoyo: true, duration: 250, onComplete: () => mover(1600) }); } });
      this.balao(p.c.x, p.c.y - 26, "Praga! Devolvida");
    } else if (para.etapa === "bloqueado") {
      mover(1000);
      this.time.delayedCall(1000, () => { const pd = this.add.image(destino.x + 6, destino.y - 60, "pedra").setDepth(40); this.tweens.add({ targets: pd, y: destino.y - 4, duration: 450, ease: "Bounce.Out", onComplete: () => pd.destroy() }); this.cameras.main.shake(120, 0.002); });
    } else if (de.etapa === "bloqueado") {
      this.balao(p.c.x, p.c.y - 26, "Desbloqueada!");
      mover();
    } else if (para.etapa === "concluido") {
      mover(1200);
      this.time.delayedCall(1300, () => this.animEntrega(para));
    } else {
      mover();
    }
  }

  animEntrega(d) {
    // carroça sai pela estrada rumo à cidade; moedas sobem ao placar
    const carro = this.add.container(REG.concluido.x + 30, ESTRADA_Y + 1).setDepth(45);
    carro.add([
      this.add.rectangle(0, -6, 22, 9, hex("#8b5a2b")).setStrokeStyle(1, hex("#2b1d14")),
      this.add.rectangle(-2, -12, 14, 5, hex("#e8842e")), this.add.circle(-7, 0, 3, hex("#2b1d14")), this.add.circle(7, 0, 3, hex("#2b1d14")),
    ]);
    this.tweens.add({ targets: carro, x: W + 30, duration: 2600, ease: "Sine.In", onComplete: () => carro.destroy(true) });
    for (let i = 0; i < 6; i++) {
      const m = this.add.image(REG.concluido.x + 40, ESTRADA_Y - 10, "moeda").setDepth(60);
      this.tweens.add({ targets: m, x: this.hud.moeda.x, y: this.hud.moeda.y, duration: 900, delay: i * 120, ease: "Cubic.In", onComplete: () => { m.destroy(); this.tweens.add({ targets: this.hud.moeda, scale: 1.6, yoyo: true, duration: 120 }); } });
    }
    this.balao(REG.concluido.x + 40, REG.concluido.y + 70, "Colheita! #" + d.ticket);
    this.som("colheita");
  }

  animCorvo(p) {
    const cv = this.add.image(-10, 40, "corvo1").setDepth(50);
    this.tweens.add({ targets: cv, x: p.c.x + 6, y: p.c.y - 24, duration: 1600, ease: "Sine.Out", onComplete: () => { this.balao(p.c.x, p.c.y - 34, "Croc! SLA estourado"); cv.destroy(); } });
    this.som("corvo");
  }

  balao(x, y, texto) {
    const t = this.txt(x, y, texto, 7, "#fff3b0").setOrigin(0.5).setDepth(70);
    this.tweens.add({ targets: t, y: y - 12, alpha: 0, delay: 1400, duration: 900, onComplete: () => t.destroy() });
  }

  som(tipo) {
    if (!PARAMS.has("som")) return; // sons desligados por padrão (política de autoplay das TVs)
    try {
      this.audio ||= new AudioContext();
      const o = this.audio.createOscillator(), g = this.audio.createGain();
      o.type = "square"; o.frequency.value = tipo === "colheita" ? 880 : 180; g.gain.value = 0.03;
      o.connect(g).connect(this.audio.destination); o.start(); o.stop(this.audio.currentTime + 0.15);
    } catch { /* sem áudio */ }
  }

  // ---------------------------------------------------------------- fazendeiros
  criarNPCs() {
    this.npcs = {
      ia: this.criarNPC("fazendeiro", "Claude", REG.lago.x + 30, REG.lago.y + 30),
      humano: this.criarNPC("humano", "Você", 487, 296),
    };
  }
  criarNPC(sprite, nome, x, y) {
    const c = this.add.container(x, y).setDepth(35);
    const img = this.add.image(0, 0, sprite).setOrigin(0.5, 1);
    const rot = this.txt(0, -16, nome, 6, "#fff3b0").setOrigin(0.5, 1);
    const suor = this.add.image(6, -14, "gota").setVisible(false);
    const vara = this.add.line(0, 0, 4, -8, 18, -16, hex("#5c3a1a")).setOrigin(0).setVisible(false);
    c.add([vara, img, rot, suor]);
    const npc = { c, img, rot, suor, vara, sprite, alvo: null, andando: false, sentado: false };
    this.tweens.add({ targets: suor, y: -8, alpha: 0, repeat: -1, duration: 600 });
    return npc;
  }
  irPara(npc, x, y, rapido, aoChegar) {
    const chave = Math.round(x) + ":" + Math.round(y);
    if (npc.alvo === chave) { if (!npc.andando && aoChegar) aoChegar(); return; }
    npc.alvo = chave; npc.andando = true; npc.vara.setVisible(false);
    npc.img.setTexture(npc.sprite);
    this.tweens.killTweensOf(npc.c);
    if (npc.balanco) { npc.balanco.remove(); npc.balanco = null; npc.img.y = 0; } // trajeto anterior interrompido
    const vel = rapido ? 75 : 42;
    // caminho em "L" pela estrada: vai até a estrada, anda na horizontal e sobe/desce
    const pts = [{ x: npc.c.x, y: ESTRADA_Y + 4 }, { x, y: ESTRADA_Y + 4 }, { x, y }];
    const passos = pts.map((p, i) => {
      const ant = i === 0 ? { x: npc.c.x, y: npc.c.y } : pts[i - 1];
      return { x: p.x, y: p.y, duration: Math.max(80, (Math.abs(p.x - ant.x) + Math.abs(p.y - ant.y)) / vel * 1000) };
    });
    npc.img.setFlipX(x < npc.c.x);
    npc.balanco = this.tweens.add({ targets: npc.img, y: -1, yoyo: true, repeat: -1, duration: rapido ? 110 : 170 });
    this.tweens.chain({
      targets: npc.c, tweens: passos,
      onComplete: () => { if (npc.balanco) { npc.balanco.remove(); npc.balanco = null; } npc.img.y = 0; npc.andando = false; if (aoChegar) aoChegar(); },
    });
  }
  atualizarNPCs() {
    const est = this.estado; if (!est) return;
    const ia = this.npcs.ia, hu = this.npcs.humano;
    const executando = est.demandas.filter((d) => d.executando);
    const fila = est.demandas.filter((d) => ["analise", "planejamento", "preparacao", "desenvolvimento", "validacao", "ajustes", "backlog"].includes(d.etapa) && !d.executando).length;
    const sobrecarregado = fila >= 6;
    ia.suor.setVisible(sobrecarregado);
    const alvoIA = executando.map((d) => this.plantas.get(d.id)).find((p) => p && p.alvo);
    if (alvoIA) this.irPara(ia, alvoIA.alvo.x - 14, alvoIA.alvo.y + 2, sobrecarregado);
    else this.irPara(ia, REG.lago.x + 26, REG.lago.y + 48, false, () => { ia.vara.setVisible(true); }); // ociosa: pescando
    const aguardando = est.demandas.filter((d) => d.aguardaHumano);
    hu.suor.setVisible(aguardando.length >= 3);
    if (aguardando.length) {
      const naFeira = aguardando.find((d) => d.etapa === "revisao");
      const p = naFeira ? null : this.plantas.get(aguardando[0].id);
      if (naFeira) this.irPara(hu, REG.revisao.x + REG.revisao.w / 2, REG.revisao.y + 124, aguardando.length >= 3);
      else if (p && p.alvo) this.irPara(hu, p.alvo.x + 14, p.alvo.y + 2, false);
    } else {
      this.irPara(hu, 487, 300, false, () => hu.img.setTexture("humanoSentado")); // sentado no banco
    }
  }

  // ---------------------------------------------------------------- clima e dia/noite
  criarClima() {
    this.nuvens = [];
    for (let i = 0; i < 4; i++) {
      const n = this.add.container(i * 180, 50 + (i % 2) * 140).setDepth(80);
      n.add([this.add.ellipse(0, 0, 130, 40, 0x1b2430, 1), this.add.ellipse(40, -10, 90, 36, 0x1b2430, 1), this.add.ellipse(-40, 6, 90, 30, 0x1b2430, 1)]);
      n.setAlpha(0);
      this.nuvens.push(n);
    }
    this.chuva = [];
    for (let i = 0; i < 110; i++) this.chuva.push(this.add.rectangle(Math.random() * W, Math.random() * H, 1, 4, 0x9fd3ff, 0.55).setDepth(81).setVisible(false));
    this.noite = this.add.rectangle(0, 0, W, H, 0x0b1630, 0).setOrigin(0).setDepth(85);
    this.luzes = LAMPIOES.map(([x, y]) => this.add.circle(x + 1, y - 2, 12, 0xfff3b0, 0).setDepth(86).setBlendMode(Phaser.BlendModes.ADD));
    this.relampago = this.add.rectangle(0, 0, W, H, 0xffffff, 0).setOrigin(0).setDepth(87);
  }
  update(_, dt) {
    const clima = this.estado ? this.estado.metricas.clima : "sol";
    const chovendo = clima === "tempestade";
    for (const n of this.nuvens) { n.x += dt * 0.008; if (n.x > W + 120) n.x = -120; n.setAlpha(clima === "sol" ? 0 : chovendo ? 0.16 : 0.08); }
    for (const g of this.chuva) {
      g.setVisible(chovendo);
      if (!chovendo) continue;
      g.y += dt * 0.35; g.x -= dt * 0.06;
      if (g.y > H) { g.y = -4; g.x = Math.random() * (W + 60); }
    }
    if (chovendo && Math.random() < 0.0015) { this.relampago.fillAlpha = 0.35; this.tweens.add({ targets: this.relampago, fillAlpha: 0, duration: 300 }); }
  }
  tick() {
    const agora = new Date();
    const hora = HORA_FIXA !== null ? HORA_FIXA : agora.getHours() + agora.getMinutes() / 60;
    // escuridão: 0 de dia, 0.5 de madrugada, transições no amanhecer (5–7h) e entardecer (17–19h)
    let esc = 0;
    if (hora >= 19 || hora < 5) esc = 0.62;
    else if (hora >= 17) esc = (hora - 17) / 2 * 0.62;
    else if (hora < 7) esc = (7 - hora) / 2 * 0.62;
    if (this.estado && this.estado.metricas.clima === "tempestade") esc = Math.max(esc, 0.18);
    this.noite.fillAlpha = esc;
    for (const l of this.luzes) l.fillAlpha = esc > 0.25 ? 0.35 : 0;
    this.atualizarRelogio(agora);
    this.atualizarOffline();
  }

  // ---------------------------------------------------------------- HUD
  criarHUD() {
    const hud = {};
    this.add.rectangle(0, 0, W, 20, 0x2b1d14, 0.88).setOrigin(0).setDepth(90);
    this.add.rectangle(0, H - 24, W, 24, 0x2b1d14, 0.88).setOrigin(0).setDepth(90);
    hud.relogio = this.txt(6, 4, "", 9, "#fff3b0").setDepth(91);
    hud.climaIcone = this.add.image(170, 10, "sol").setDepth(91);
    hud.clima = this.txt(178, 4, "", 9, "#cfe8ff").setDepth(91);
    hud.moeda = this.add.image(300, 10, "moeda").setDepth(91).setScale(1.2);
    hud.entregas = this.txt(308, 4, "", 9).setDepth(91);
    hud.lead = this.txt(430, 4, "", 9).setDepth(91);
    hud.retrab = this.txt(540, 4, "", 9).setDepth(91);
    hud.titulo = this.txt(W / 2, 24, "", 7, "#fff3b0").setOrigin(0.5, 0).setDepth(91);
    hud.rodape = ETAPAS_RODAPE.map((_, i) => this.txt(4 + i * 63.5, H - 21, "", 7).setDepth(91));
    hud.rodape2 = ETAPAS_RODAPE.map((_, i) => this.txt(4 + i * 63.5, H - 11, "", 8, "#fff3b0").setDepth(91));
    hud.offline = this.add.container(W / 2, 44).setDepth(95).setVisible(false);
    hud.offline.add([this.add.rectangle(0, 0, 230, 14, 0x2b1d14, 0.85).setStrokeStyle(1, hex("#c9a227")), this.add.image(-105, 0, "passaro"), this.txt(-96, -5, "", 7, "#fff3b0")]);
    // card rotativo de destaque
    hud.card = this.add.container(440, 196).setDepth(92).setVisible(false);
    hud.cardFundo = this.add.rectangle(0, 0, 194, 92, 0xf4ead2, 0.96).setOrigin(0).setStrokeStyle(2, hex("#5c3a1a"));
    hud.cardTitulo = this.txt(8, 6, "", 8, "#8e2a2a", { stroke: "" });
    hud.cardTexto = this.txt(8, 20, "", 7, "#2b1d14", { stroke: "", bold: false });
    hud.card.add([hud.cardFundo, hud.cardTitulo, hud.cardTexto]);
    this.hud = hud;
  }
  atualizarRelogio(agora) {
    const dia = agora.toLocaleDateString("pt-BR", { weekday: "short", day: "2-digit", month: "2-digit" });
    const hora = agora.toLocaleTimeString("pt-BR", { hour: "2-digit", minute: "2-digit" });
    this.hud.relogio.setText(`${dia} · ${hora}`);
  }
  atualizarHUD() {
    const e = this.estado, m = e.metricas, hud = this.hud;
    hud.clima.setText({ sol: "Sol", nublado: "Nublado", tempestade: "Tempestade" }[m.clima] || "");
    hud.climaIcone.setTexture({ sol: "sol", nublado: "nuvem", tempestade: "tempestade" }[m.clima] || "sol");
    hud.entregas.setText(`Hoje ${m.entregasHoje} · Semana ${m.entregasSemana}`);
    hud.lead.setText(`Lead ${m.leadTimeMedioH ? m.leadTimeMedioH.toLocaleString("pt-BR") + "h" : "—"}`);
    hud.retrab.setText(`Retrabalho ${Math.round((m.taxaRetrabalho || 0) * 100)}%`);
    const extras = [];
    if (e.filaPausada) extras.push("Fila pausada: limite do plano Claude atingido");
    else if (e.uso && e.uso.fiveHourPct >= 80) extras.push(`Plano Claude: ${Math.round(e.uso.fiveHourPct)}% da janela de 5h`);
    hud.titulo.setText(extras.join(" · "));
    const entreguesNoMapa = e.demandas.filter((d) => d.etapa === "concluido").length;
    ETAPAS_RODAPE.forEach(([k, rot], i) => {
      const n = k === "concluido" ? entreguesNoMapa : (e.contagem[k] || 0);
      const lim = (e.wip || {})[k] || 0;
      const estourou = lim > 0 && n > lim;
      hud.rodape[i].setText(rot).setColor(estourou ? "#ff8a7a" : "#e8d9b0");
      hud.rodape2[i].setText(estourou ? `${n}/${lim} !` : String(n)).setColor(estourou ? "#ff8a7a" : "#fff3b0");
    });
  }
  atualizarOffline() {
    const f = this.fonte; if (!f) return;
    const min = Math.floor((Date.now() - f.ultimoOk) / 60000);
    const mostrar = !f.online || min >= 1;
    this.hud.offline.setVisible(mostrar);
    if (mostrar) this.hud.offline.list[2].setText(min < 1 ? "Sem notícias da esteira…" : `Sem notícias da esteira há ${min} min`);
  }
  rotacionarCard() {
    const e = this.estado; if (!e) return;
    const atrasadas = e.demandas.filter((d) => d.etapa !== "concluido" && (d.slaEstourado || d.murcha >= 1 || d.etapa === "bloqueado"))
      .sort((a, b) => gravidade(b) - gravidade(a));
    const card = this.hud.card;
    if (!atrasadas.length) { card.setVisible(false); return; }
    const d = atrasadas[this.cardIdx++ % atrasadas.length];
    this.hud.cardTitulo.setText(curto(`#${d.ticket} ${d.titulo}`, 34));
    const etapa = d.etapa === "bloqueado" && d.bloqueadaEm ? `Bloqueado (em ${d.bloqueadaEm})` : (e.rotulos && e.rotulos[d.etapa]) || d.etapa;
    const linhas = [
      `Etapa: ${etapa}`,
      `Parado há: ${duracao(d.paradoH)}`,
      `Responsável: ${d.aguardaHumano ? "Você (aprovação)" : d.responsavel}`,
      d.prazoSla ? `SLA: ${d.slaEstourado ? "ESTOURADO" : "até " + new Date(d.prazoSla).toLocaleString("pt-BR", { day: "2-digit", month: "2-digit", hour: "2-digit", minute: "2-digit" })}` : "SLA: —",
      `Tentativas: ${d.tentativas} · Retrabalhos: ${d.retrabalhos}`,
    ];
    if (d.mensagem) linhas.push(curto(d.mensagem, 38));
    this.hud.cardTexto.setText(linhas.join("\n")).setLineSpacing(2);
    card.setVisible(true).setAlpha(0);
    this.tweens.add({ targets: card, alpha: 1, duration: 400 });
  }
}

function gravidade(d) { return (d.slaEstourado ? 100 : 0) + (d.etapa === "bloqueado" ? 50 : 0) + (5 - d.prioridade) * 5 + Math.min(d.paradoH || 0, 48); }
function curto(s, n) { s = String(s || ""); return s.length > n ? s.slice(0, n - 1) + "…" : s; }
function duracao(h) {
  h = h || 0;
  if (h < 1) return Math.round(h * 60) + " min";
  if (h < 48) return h.toFixed(1).replace(".", ",") + " h";
  return Math.round(h / 24) + " dias";
}

// ------------------------------------------------------------------ jogo
new Phaser.Game({
  type: Phaser.AUTO, // WebGL com fallback automático para Canvas
  parent: "jogo", width: W * ZOOM, height: H * ZOOM, backgroundColor: "#14210f",
  pixelArt: true, roundPixels: true,
  scale: { mode: Phaser.Scale.FIT, autoCenter: Phaser.Scale.CENTER_BOTH },
  scene: [Fazenda],
  banner: false,
});
