// Check focus repair without a browser, filesystem fixture or content rewrite.
import assert from 'node:assert/strict';
import test from 'node:test';
import { focusScrollableContent } from './content-focus.mjs';

test('focus repair preserves exact code, clipboard values and script bytes', () => {
  const input = '<!doctype html><div class="sl-markdown-content"><table class="wide"><tr><td>A</td></tr></table>' +
    '<pre data-language="sh"><code>cat &lt;&lt;\'EOF\'\n# keep this comment\nEOF\n</code></pre>' +
    '<button data-code="a&#x7f;b">Copy</button></div><script>const text = "<pre>";</script>';
  const expected = input.replace('<table class="wide">', '<table class="wide" tabindex="0">')
    .replace('<pre data-language="sh">', '<pre data-language="sh" tabindex="0">');
  assert.equal(focusScrollableContent(input), expected);
  assert.equal(focusScrollableContent(expected), expected);
});

test('focus repair preserves explicit focus choices and ignores other page surfaces', () => {
  const input = '<nav><pre>Navigation</pre></nav><table><tr><td>Outside</td></tr></table>' +
    '<div class="other sl-markdown-content"><section><pre tabindex="-1">Intentional</pre>' +
    '<table tabindex="0"><tr><td>Ready</td></tr></table></section></div>';
  assert.equal(focusScrollableContent(input), input);
});

test('focus repair covers nested authored and generated content without normalizing markup', () => {
  const input = "<p>\u00e9\u{1f642}</p><div class='sl-markdown-content api'><section><PRE data-language='json'>{&quot;n&quot;:9007199254740993,&quot;label&quot;:&quot;\u00e9\u{1f642}&quot;}</PRE>" +
    '<div><table><tr><td>Generated API</td></tr></table></div></section></div>';
  assert.equal(focusScrollableContent(input), input.replace("<PRE data-language='json'>", "<PRE data-language='json' tabindex=\"0\">")
    .replace('<table>', '<table tabindex="0">'));
});

test('focus repair includes readonly API paths only within manual content', () => {
  const path = '<div class="sl-openapi-operation-method-path">/api/v1/publishers/{publisherId}/domains/{domain}/verification:check</div>';
  const input = path + '<main class="sl-markdown-content">' + path + '<div>Ordinary text</div></main>';
  assert.equal(focusScrollableContent(input), path + '<main class="sl-markdown-content">' +
    path.replace('">', '" tabindex="0">') + '<div>Ordinary text</div></main>');
});
