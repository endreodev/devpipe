'use strict';

/**
 * Calcula o total de um pedido.
 * @param {Array<{sku: string, preco: number, quantidade: number}>} itens
 * @returns {{subtotal: number, total: number}}
 */
function calcularTotal(itens) {
  if (!Array.isArray(itens)) {
    throw new TypeError('itens deve ser uma lista');
  }
  let subtotal = 0;
  for (const item of itens) {
    validarItem(item);
    subtotal += item.preco * item.quantidade;
  }
  subtotal = arredondar(subtotal);
  return { subtotal, total: subtotal };
}

function validarItem(item) {
  if (!item || typeof item.sku !== 'string' || item.sku === '') {
    throw new TypeError('item sem SKU');
  }
  if (!(item.preco >= 0)) {
    throw new RangeError(`preço inválido para ${item.sku}`);
  }
  if (!Number.isInteger(item.quantidade) || item.quantidade <= 0) {
    throw new RangeError(`quantidade inválida para ${item.sku}`);
  }
}

function arredondar(valor) {
  return Math.round(valor * 100) / 100;
}

module.exports = { calcularTotal, arredondar };
