'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { calcularTotal } = require('../src/pedido');

test('soma preço x quantidade', () => {
  const r = calcularTotal([{ sku: 'A', preco: 10, quantidade: 2 }, { sku: 'B', preco: 5.5, quantidade: 1 }]);
  assert.equal(r.subtotal, 25.5);
  assert.equal(r.total, 25.5);
});

test('rejeita quantidade inválida', () => {
  assert.throws(() => calcularTotal([{ sku: 'A', preco: 10, quantidade: 0 }]), RangeError);
});
