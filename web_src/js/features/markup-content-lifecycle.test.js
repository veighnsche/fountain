import {vi} from 'vitest';
import {initPageFeatures, registerPageFeature, retirePageFeatures, setPageConfig} from '../native-lifecycle.js';
import {initMarkupContent, retireMarkupContent} from '../markup/content.js';
import {renderCodeCopy} from '../markup/codecopy.js';
import {renderMermaid} from '../markup/mermaid.js';
import {renderAsciicast} from '../markup/asciicast.js';

vi.mock('mermaid', () => ({default: {initialize: vi.fn(), parse: vi.fn(), render: vi.fn(async () => ({svg: '<svg></svg>'}))}}));
vi.mock('asciinema-player', () => ({create: vi.fn(() => ({dispose: vi.fn()}))}));
vi.mock('katex/dist/katex.js', () => ({default: {render: vi.fn((source, el) => { el.textContent = `math:${source}` })}}));
vi.mock('../modules/toast.js', () => ({showErrorToast: vi.fn()}));

function installMarkup() {
  document.body.innerHTML = `<div class="page-content"><div class="markup">
    <pre class="code-block"><code>console.log(1)\n</code></pre>
    <pre><code class="language-mermaid">graph TD</code></pre>
    <pre><code class="language-math">x^2</code></pre>
    <div class="asciinema-player-container" data-asciinema-player-src="/cast.json"></div>
  </div><iframe class="external-render"></iframe></div>
  <div data-extension-panel><div class="markup"><pre class="code-block"><code>owned\n</code></pre>
    <pre><code class="language-mermaid">owned</code></pre></div></div>`;
  // happy-dom 21 no longer loads src-less iframe pages, so the external-render
  // frame has no window of its own; give it one to receive the height request.
  const frame = document.querySelector('iframe.external-render');
  Object.defineProperty(frame, 'contentWindow', {value: {postMessage: vi.fn()}});
  return frame;
}

// The mocked renderer imports resolve in microtasks; the mermaid iframe load
// listener fires as a later macrotask (twice under happy-dom, which removes
// the block on the stale second firing). One real-timer macrotask therefore
// observes the freshly rendered state deterministically.
async function settleRenderers() {
  await new Promise((resolve) => setTimeout(resolve, 0));
}

afterEach(() => {
  retireMarkupContent();
  try { retirePageFeatures() } catch {}
  document.body.innerHTML = '';
  vi.clearAllMocks();
});

test('markup content renders each native renderer once and skips owned markup', async () => {
  const mermaid = (await import('mermaid')).default;
  const player = await import('asciinema-player');
  const katex = (await import('katex/dist/katex.js')).default;
  installMarkup();
  initMarkupContent();
  await settleRenderers();
  expect(document.querySelectorAll('.page-content .code-block > .code-copy')).toHaveLength(1);
  expect(document.querySelector('.page-content .code-block > .code-copy').getAttribute('data-clipboard-text')).toBe('console.log(1)');
  expect(document.querySelectorAll('.mermaid-block')).toHaveLength(1);
  // the css-backed chunks resolve later; their mock calls are stable once made
  await vi.waitFor(() => expect(player.create).toHaveBeenCalledTimes(1));
  await vi.waitFor(() => expect(katex.render).toHaveBeenCalledTimes(1));
  expect(mermaid.parse).toHaveBeenCalledTimes(1);
  expect(mermaid.parse.mock.lastCall[0]).toBe('graph TD');
  expect(mermaid.render).toHaveBeenCalledTimes(1);
  expect(player.create.mock.lastCall[0]).toBe('/cast.json');
  expect(document.querySelectorAll('[data-extension-panel] .code-copy')).toHaveLength(0);
  expect(document.querySelectorAll('[data-extension-panel] .mermaid-block')).toHaveLength(0);
});

test('markup retirement removes mermaid blocks, disposes players, drops external handler', async () => {
  const player = await import('asciinema-player');
  const frame = installMarkup();
  initMarkupContent();
  await settleRenderers();
  expect(document.querySelectorAll('.mermaid-block')).toHaveLength(1);
  expect(frame.contentWindow.postMessage).toHaveBeenCalledWith({requestOffsetHeight: true}, '*');
  const created = player.create.mock.results[0].value;
  retireMarkupContent();
  expect(document.querySelectorAll('.mermaid-block')).toHaveLength(0);
  expect(created.dispose).toHaveBeenCalledTimes(1);
  window.dispatchEvent(new MessageEvent('message', {source: frame.contentWindow, data: {frameHeight: 400}}));
  expect(frame.height).toBe('');
  // code-copy buttons and katex output are region-owned mutations: they die
  // with the transplanted region, so retire leaves them in place
  expect(document.querySelectorAll('.page-content .code-block > .code-copy')).toHaveLength(1);
  // re-entry after retire renders fresh renderers once
  player.create.mockClear();
  installMarkup();
  initMarkupContent();
  await settleRenderers();
  expect(document.querySelectorAll('.mermaid-block')).toHaveLength(1);
  expect(player.create).toHaveBeenCalledTimes(1);
});

test('code-copy binds once per block with current text and never duplicates', () => {
  document.body.innerHTML = `<div class="markup"><pre class="code-block"><code>alpha\n</code></pre></div>`;
  renderCodeCopy();
  renderCodeCopy();
  const buttons = document.querySelectorAll('.code-copy');
  expect(buttons).toHaveLength(1);
  expect(buttons[0].getAttribute('data-clipboard-text')).toBe('alpha');
  // fresh blocks on re-entry get fresh buttons with current text
  document.querySelector('.markup').insertAdjacentHTML('beforeend', '<pre class="code-block"><code>beta\n</code></pre>');
  renderCodeCopy();
  expect(document.querySelectorAll('.code-copy')).toHaveLength(2);
});

test('renderers with no native targets resolve without invoking heavy renderers', async () => {
  const mermaid = (await import('mermaid')).default;
  const player = await import('asciinema-player');
  document.body.innerHTML = `<div class="page-content"><div class="markup"><p>plain</p></div></div>`;
  await renderMermaid();
  await renderAsciicast();
  expect(mermaid.render).not.toHaveBeenCalled();
  expect(player.create).not.toHaveBeenCalled();
});

test('page seam drives markup-content init and retire', async () => {
  installMarkup();
  registerPageFeature('test-b03-markup', {init: initMarkupContent, retire: retireMarkupContent});
  setPageConfig({});
  initPageFeatures();
  await settleRenderers();
  expect(document.querySelectorAll('.mermaid-block')).toHaveLength(1);
  retirePageFeatures();
  expect(document.querySelectorAll('.mermaid-block')).toHaveLength(0);
});
