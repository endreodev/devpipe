# Ferramentas de economia de tokens

Scripts em Python 3.8+ (somente biblioteca padrão) para medir e reduzir o
consumo do limite da assinatura pelo DevPipe. Rode a partir da raiz do DevPipe.

| Script | Para quê |
|---|---|
| `relatorio_consumo.py` | Lê o `devpipe.db` e mostra, por demanda, custo equivalente, leituras, **releituras**, buscas, comandos, correções e quanto a janela de 5h subiu. Comece por aqui. |
| `estimar_tokens.py` | Estima os tokens de arquivos/pastas e lista os maiores. Use em arquivos de padrões e anexos. |
| `enxugar_markdown.py` | Gera `*.min.md` dos arquivos de padrões (sem comentários, imagens, badges, sumário e linhas em branco extras). Aponte o `.min.md` em *Arquivos de padrões* do projeto. |
| `compactar_saida.py` | Executa um comando de build/teste e imprime só o que importa (erros + contexto + final), preservando o código de saída. |

## Uso rápido

```powershell
python tools\tokens\relatorio_consumo.py --dias 7
python tools\tokens\relatorio_consumo.py --demanda CH-1001
python tools\tokens\estimar_tokens.py C:\repos\meu-projeto\docs
python tools\tokens\enxugar_markdown.py C:\repos\meu-projeto\docs\padroes.md --sem-exemplos 15
python tools\tokens\compactar_saida.py --stats -- dotnet build -v q
```

## Fazer o agente usar a saída compactada

O que o agente lê fica no histórico da sessão e é reenviado a cada passo, então
uma saída de build de 2.000 linhas pesa em todos os passos seguintes.

1. Copie `tools/tokens` para o repositório do projeto (ou use o caminho absoluto).
2. Em **Projetos > Comandos liberados ao agente**, libere o prefixo
   `python tools/tokens/compactar_saida.py`.
3. Em **Regras do projeto**, adicione:
   > Rode build e testes sempre via `python tools/tokens/compactar_saida.py -- <comando>`.

A validação oficial do DevPipe já compacta a saída antes de mandá-la para a
correção, então não precisa mudar os comandos de build/teste do projeto.
