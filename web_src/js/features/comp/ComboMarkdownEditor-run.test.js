import $ from 'jquery';
import {vi} from 'vitest';
import {getComboMarkdownEditor, initComboMarkdownEditor} from './ComboMarkdownEditor.js';
import {attachTribute} from '../tribute.js';

const lazyEditor = vi.hoisted(() => {
  let resolve;
  const promise = new Promise((accept) => { resolve = accept });
  return {promise, resolve, construct: vi.fn()};
});
vi.mock('easymde', () => lazyEditor.promise);
vi.mock('../repo-editor.js', () => ({renderPreviewPanelContent: vi.fn()}));
vi.mock('../tribute.js', () => ({attachTribute: vi.fn()}));

function editorRoot() {
  document.body.innerHTML = `<div class="combo-markdown-editor">
    <markdown-toolbar><div class="switch"><button class="item active" data-tab-for="markdown-writer">Write</button><button class="item" data-tab-for="markdown-previewer" data-preview-url="/preview">Preview</button></div>
      <button class="markdown-switch-monospace" data-enable-text="Monospace" data-disable-text="Normal"></button></markdown-toolbar>
    <div class="ui tab active" data-tab-panel="markdown-writer"><text-expander><textarea class="markdown-text-editor">Draft</textarea></text-expander></div>
    <div class="ui tab" data-tab-panel="markdown-previewer"></div>
    <div data-modal-name="new-markdown-table"><button data-selector-name="ok-button">Table</button></div>
    <div data-modal-name="new-markdown-link"><div data-selector-name="form"><input name="link-description"></div><button data-selector-name="ok-button">Link</button></div>
  </div>`;
  return document.querySelector('.combo-markdown-editor');
}

beforeEach(() => {
  $.fn.modal = vi.fn().mockReturnThis();
  localStorage.removeItem('markdown-editor-default');
  lazyEditor.construct.mockReset();
  attachTribute.mockReset();
});

afterEach(() => {
  for (const root of document.querySelectorAll('.combo-markdown-editor')) getComboMarkdownEditor(root)?.destroy();
  document.body.innerHTML = '';
  localStorage.removeItem('markdown-editor-default');
});

test('a supplied signal owns Markdown initialization before the lazy editor import', async () => {
  localStorage.setItem('markdown-editor-default', 'easymde');
  const root = editorRoot();
  const lifetime = new AbortController();
  const pending = initComboMarkdownEditor(root, {signal: lifetime.signal});
  lifetime.abort();
  const construct = lazyEditor.construct;
  lazyEditor.resolve({default: construct});
  await pending;
  expect(construct).not.toHaveBeenCalled();
  expect(getComboMarkdownEditor(root)).toBeUndefined();
});

test('public destroy removes both moved modal resources and finishes cleanup after a modal failure', async () => {
  const root = editorRoot();
  const editor = await initComboMarkdownEditor(root);
  const table = root.querySelector('[data-modal-name="new-markdown-table"]');
  const link = root.querySelector('[data-modal-name="new-markdown-link"]');
  document.body.append(table, link);
  root.remove();
  $.fn.modal = vi.fn(function (action) {
    if (action === 'destroy' && this[0] === table) throw new Error('table widget failed');
    return this;
  });
  expect(() => editor.destroy()).toThrow('table widget failed');
  expect(table.isConnected).toBe(false);
  expect(link.isConnected).toBe(false);
  expect(getComboMarkdownEditor(root)).toBeUndefined();
  const destroyed = $.fn.modal.mock.calls.flatMap(([action], index) => action === 'destroy' ? [$.fn.modal.mock.contexts[index][0]] : []);
  expect(destroyed).toEqual([table, link]);
});

test('a failed asynchronous editor switch releases its partial editor and keeps textarea mode usable', async () => {
  const root = editorRoot();
  const editor = await initComboMarkdownEditor(root, {easyMDEOptions: {toolbar: []}});
  const easyMDE = {
    codemirror: {on: vi.fn(), setOption: vi.fn(), getInputField: () => root.querySelector('textarea')},
    toTextArea: vi.fn(),
  };
  lazyEditor.construct.mockImplementation(function () { Object.assign(this, easyMDE) });
  attachTribute.mockRejectedValue(new Error('tribute import failed'));
  await expect(editor.switchToEasyMDE()).rejects.toThrow('tribute import failed');
  expect(easyMDE.toTextArea).toHaveBeenCalledOnce();
  expect(editor.value()).toBe('Draft');
  editor.destroy();
  expect(easyMDE.toTextArea).toHaveBeenCalledOnce();
});
