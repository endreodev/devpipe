#!/usr/bin/env sh
# Uso: ./demo/preparar-demo.sh /caminho/destino
# Cria <destino>/loja-demo como repositório Git com as branches main, release e stage.
set -e
dest="${1:?informe a pasta de destino}"
here="$(cd "$(dirname "$0")" && pwd)"
src="$here/loja-demo"
target="$dest/loja-demo"
[ -e "$target" ] && { echo "Já existe: $target (nada foi alterado)"; exit 1; }
mkdir -p "$dest"
cp -R "$src" "$target"
cd "$target"
git init -q -b main
git add .
git -c user.name="DevPipe Demo" -c user.email="demo@localhost" commit -q -m "Versão inicial da Loja Demo"
git branch release
git branch stage
echo "Repositório de demonstração criado em: $target"
echo "Especificação de exemplo: $here/demanda-exemplo/CH-1001-cupom-desconto.docx"
