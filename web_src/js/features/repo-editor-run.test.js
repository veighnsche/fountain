import $ from 'jquery';
import {vi} from 'vitest';
import {createFeatureRun} from '../native-feature-run.js';
import {initRepoEditor, retireRepoEditor} from './repo-editor.js';
import {createCodeEditor, destroyCodeEditor} from './codeeditor.ts';

vi.mock('./codeeditor.ts', () => ({createCodeEditor: vi.fn(), destroyCodeEditor: vi.fn()}));
vi.mock('../markup/content.js', () => ({initMarkupContent: vi.fn()}));
vi.mock('./contextpopup.js', () => ({attachRefIssueContextPopup: vi.fn()}));

function editorForm() {
  document.body.innerHTML = `<div class="page-content repository editor"><form class="repository edit form ui">
    <input id="file-name" value="file.txt"><input id="tree_path"><input name="page_has_posted" value="true">
    <textarea id="edit_area">draft</textarea><button type="button" id="commit-button">Commit</button>
  </form></div>`;
  $.fn.areYouSure = vi.fn();
}

afterEach(() => {
  retireRepoEditor();
  document.body.innerHTML = '';
  delete $.fn.areYouSure;
});

test('editor entry returns and cleans a failed asynchronous initialization', async () => {
  editorForm();
  const run = createFeatureRun();
  createCodeEditor.mockRejectedValue(new Error('editor import failed'));
  await expect(initRepoEditor(run)).rejects.toThrow('editor import failed');
  expect(createCodeEditor.mock.lastCall[2]).toBe(run);
  expect(run.signal.aborted).toBe(true);
  const filename = document.getElementById('file-name');
  filename.value = 'stale/path.txt';
  filename.dispatchEvent(new Event('input'));
  expect(document.getElementById('tree_path').value).toBe('');
});

test('an outgoing completion in the same page generation releases only its own editor', async () => {
  editorForm();
  let resolve;
  createCodeEditor.mockReturnValueOnce(new Promise((accept) => { resolve = accept }));
  const outgoing = initRepoEditor();
  const incomingEditor = {setValue: vi.fn()};
  createCodeEditor.mockResolvedValueOnce(incomingEditor);
  await initRepoEditor();
  const outgoingEditor = {setValue: vi.fn()};
  resolve(outgoingEditor);
  await outgoing;
  expect(destroyCodeEditor).toHaveBeenCalledWith(outgoingEditor);
  expect(destroyCodeEditor).not.toHaveBeenCalledWith(incomingEditor);
  retireRepoEditor();
  expect(destroyCodeEditor.mock.calls.filter(([view]) => view === incomingEditor)).toHaveLength(1);
});
