# Padrões de desenvolvimento — Loja Demo

- JavaScript CommonJS (`require`/`module.exports`), Node 18+, `'use strict'` no topo.
- Sem dependências externas: use apenas a biblioteca padrão do Node.
- Nomes de funções e variáveis em português, camelCase.
- Valores monetários sempre arredondados com a função `arredondar` (2 casas).
- Erros de entrada: `TypeError` para tipo errado, `RangeError` para valor fora do intervalo.
- Toda regra nova precisa de teste em `test/` usando `node:test` e `node:assert/strict`.
- Não alterar a assinatura pública de funções existentes de forma incompatível.
