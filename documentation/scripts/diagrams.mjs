// Structural accessibility and self-contained-reference checks for technical SVGs.
import assert from 'node:assert/strict';

// Check rendered technical SVGs, not theme icons. References are local to each
// diagram so two components cannot accidentally borrow one another's markers.
export function assertDiagram(svg) {
  const ids = new Map(), references = [];
  const attrs = node => Object.fromEntries((node.attrs ?? []).map(a => [a.name, a.value]));
  // visit gathers references before resolving them, allowing forward marker uses.
  function visit(node) {
    const values = attrs(node);
    if (values.id) {
      assert(!ids.has(values.id), `Duplicate diagram ID: ${values.id}`);
      ids.set(values.id, node);
    }
    assert(!['script', 'foreignObject', 'image'].includes(node.tagName), 'Diagram must be self-contained SVG');
    for (const [name, value] of Object.entries(values)) {
      assert(!name.toLowerCase().startsWith('on'), 'Diagram must not contain event handlers');
      if (name === 'href' || name === 'xlink:href') {
        assert(/^#[\w-]+$/.test(value), 'Diagram reference must be local');
        references.push(value.slice(1));
      }
      for (const match of value.matchAll(/url\(([^)]+)\)/g)) {
        assert(/^#[\w-]+$/.test(match[1]), 'Diagram URL must be local');
        references.push(match[1].slice(1));
      }
    }
    node.childNodes?.forEach(visit);
  }
  visit(svg);
  const properties = attrs(svg);
  assert.equal(properties.role, 'img');
  const labels = (properties['aria-labelledby'] ?? '').split(/\s+/);
  for (const id of labels) assert(ids.has(id), `Missing diagram label: ${id}`);
  for (const tag of ['title', 'desc']) {
    assert(labels.some(id => ids.get(id)?.tagName === tag && ids.get(id).childNodes?.some(n => n.value?.trim())), `Diagram needs an accessible ${tag}`);
  }
  for (const id of references) assert(ids.has(id), `Missing diagram marker: ${id}`);
}
