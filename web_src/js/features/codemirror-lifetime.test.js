import {vi} from 'vitest';
import {createFeatureRun} from '../native-feature-run.js';
import {createCodemirror} from './codemirror.ts';

const language = vi.hoisted(() => {
  let resolve;
  const promise = new Promise((accept) => { resolve = accept });
  return {promise, resolve, started: vi.fn()};
});
vi.mock('./codemirror-lang.ts', () => ({
  languages: ({LanguageDescription}) => [LanguageDescription.of({
    name: 'fixture', extensions: ['fixture'],
    load: () => { language.started(); return language.promise },
  })],
}));

function textarea() {
  document.body.innerHTML = '<div><textarea>before</textarea><div class="codemirror-container"><div class="editor-loading">Loading</div></div></div>';
  return document.querySelector('textarea');
}

afterEach(() => {
  for (const view of window.codeEditors ?? []) view.destroy();
  document.body.innerHTML = '';
});

test('a language load cannot construct a view after its exact run retires', async () => {
  const element = textarea();
  const run = createFeatureRun();
  const pending = createCodemirror(element, 'file.fixture', {wordWrap: false, indentStyle: 'space'}, run);
  await vi.waitFor(() => expect(language.started).toHaveBeenCalledOnce());
  run.dispose();
  language.resolve([]);
  await expect(pending).rejects.toMatchObject({name: 'AbortError'});
  expect(document.querySelector('.cm-editor')).toBeNull();
  expect(window.codeEditors?.size ?? 0).toBe(0);
});

test('ordinary CodeMirror view destruction unregisters itself and preserves synchronized text', async () => {
  const element = textarea();
  const editor = await createCodemirror(element, 'file.txt', {wordWrap: false, indentStyle: 'space'});
  expect(window.codeEditors.has(editor.view)).toBe(true);
  editor.view.dispatch({changes: {from: 0, to: editor.view.state.doc.length, insert: 'saved draft'}});
  expect(element.value).toBe('saved draft');
  editor.view.destroy();
  expect(window.codeEditors.has(editor.view)).toBe(false);
  expect(element.value).toBe('saved draft');
});
