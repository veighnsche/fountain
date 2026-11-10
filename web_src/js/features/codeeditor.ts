import {basename, extname} from '../utils.js';
import {hideElem, onInputDebounce, showElem} from '../utils/dom.js';
import {createCodemirror, type CodemirrorEditor, type EditorOptions, type EditorLifetime} from './codemirror.ts';
import {EditorView} from '@codemirror/view';
import type {LanguageSupport} from '@codemirror/language';

interface EditorConfig {
  indent_style: string;
  indent_size: string;
}

export class SettableEditorView extends EditorView {
  __codeEditorCleanup?: () => void;
  private codeEditorDestroyed?: boolean;

  public override destroy() {
    if (this.codeEditorDestroyed) return;
    this.codeEditorDestroyed = true;
    try {
      this.__codeEditorCleanup?.();
    } finally {
      this.__codeEditorCleanup = undefined;
      super.destroy();
    }
  }

  public setValue(value: string) {
    // Replace \n with the actual newline character and unescape escaped \n
    value = value.replaceAll(/(?<!\\)\\n/g, '\n').replaceAll(/\\\\n/g, '\\n');
    this.dispatch({changes: {from: 0, to: this.state.doc.length, insert: value}});
  }
}

function getEditorconfig(input: HTMLInputElement): null | EditorConfig {
  try {
    return JSON.parse(input.getAttribute('data-editorconfig'));
  } catch {
    return null;
  }
}

async function updateEditor(editor: CodemirrorEditor, filename: string, lineWrapExts: string[], current: () => boolean) {
  if (!current()) return;
  const fileOption = getFileBasedOptions(filename, lineWrapExts);
  editor.view.dispatch({
    effects: editor.compartments.wordWrap.reconfigure(fileOption.wordWrap ? editor.codemirrorView.EditorView.lineWrapping : []),
  });

  const currentLanguage = editor.compartments.language.get(editor.view.state) as Array<unknown> | LanguageSupport;
  const newLanguage = editor.codemirrorLanguage.LanguageDescription.matchFilename(editor.languages, filename);
  if (!currentLanguage || (currentLanguage as Array<unknown>).length === 0 || !newLanguage || (currentLanguage as LanguageSupport).language.name.toLowerCase() !== newLanguage.name.toLowerCase()) {
    const language = newLanguage ? await newLanguage.load() : [];
    if (!current()) return;
    editor.view.dispatch({effects: editor.compartments.language.reconfigure(language)});
  }
}

function getFileBasedOptions(filename: string, lineWrapExts: string[]): Pick<EditorOptions, 'wordWrap'> {
  return {
    wordWrap: (lineWrapExts || []).includes(extname(filename)),
  };
}

function togglePreviewDisplay(previewable: boolean) {
  const previewTab = document.querySelector('.item[data-tab="preview"]') as HTMLAnchorElement;
  if (!previewTab) return;

  if (previewable) {
    const newUrl = (previewTab.getAttribute('data-url') || '').replace(/(.*)\/.*/, `$1/markup`);
    previewTab.setAttribute('data-url', newUrl);
    previewTab.style.display = '';
  } else {
    previewTab.style.display = 'none';
    // If the "preview" tab was active, user changes the filename to a non-previewable one,
    // then the "preview" tab becomes inactive (hidden), so the "write" tab should become active
    if (previewTab.classList.contains('active')) {
      const writeTab = document.querySelector('.item[data-tab="write"]') as HTMLAnchorElement;
      writeTab.click();
    }
  }
}

export async function createCodeEditor(textarea: HTMLTextAreaElement, filenameInput: HTMLInputElement, lifetime: EditorLifetime = {}): Promise<SettableEditorView> {
  let destroyed = false;
  const current = () => !destroyed && !lifetime.signal?.aborted && (lifetime.current?.() ?? true);
  if (!current()) throw new DOMException('Code editor initialization was retired', 'AbortError');
  const filename = basename(filenameInput.value);
  const previewableExts = new Set((textarea.getAttribute('data-previewable-extensions') || '').split(','));
  const lineWrapExts = (textarea.getAttribute('data-line-wrap-extensions') || '').split(',');
  const previewable = previewableExts.has(extname(filename));
  const editorConfig = getEditorconfig(filenameInput);
  const searchButton = document.querySelector('#editor-find') as HTMLElement | null;
  const switchEl = document.querySelector('#editor-bar .switch');
  const writeTab = document.querySelector('#editor-bar .switch .item[data-tab="write"]');

  togglePreviewDisplay(previewable);

  const editor = await createCodemirror(textarea, filename, {
    ...getFileBasedOptions(filenameInput.value, lineWrapExts),
    ...getEditorConfigOptions(editorConfig),
  }, lifetime);
  if (!current()) {
    editor.view.destroy();
    throw new DOMException('Code editor initialization was retired', 'AbortError');
  }

  let filenameRevision = 0;
  const onFilenameInput = onInputDebounce(async () => {
    if (!current()) return;
    const revision = ++filenameRevision;
    try {
      const name = filenameInput.value;
      togglePreviewDisplay(previewableExts.has(extname(name)));
      await updateEditor(editor, name, lineWrapExts, () => current() && revision === filenameRevision);
    } catch (error) {
      if (current() && revision === filenameRevision && error?.name !== 'AbortError') console.error('Could not update code editor language', error);
    }
  });
  filenameInput.addEventListener('input', onFilenameInput);

  const onSearchClick = () => {
    if (!current()) return;
    const search = editor.codemirrorSearch;
    const view = editor.view;
    if (search.searchPanelOpen(view.state)) search.closeSearchPanel(view);
    else search.openSearchPanel(view);
  };
  searchButton?.addEventListener('click', onSearchClick);

  const onSwitchClick = () => {
    if (!current() || !searchButton || !writeTab) return;
    if (writeTab.classList.contains('active')) showElem(searchButton);
    else hideElem(searchButton);
  };
  switchEl?.addEventListener('click', onSwitchClick);

  const view = Object.setPrototypeOf(editor.view, SettableEditorView.prototype) as SettableEditorView;
  // Retirement destroys the CodeMirror view and every listener wired above.
  // The textarea keeps its last synced value, so no edit is lost on retire.
  view.__codeEditorCleanup = () => {
    destroyed = true;
    filenameInput.removeEventListener('input', onFilenameInput);
    (onFilenameInput as unknown as {cancel?: () => void}).cancel?.();
    searchButton?.removeEventListener('click', onSearchClick);
    switchEl?.removeEventListener('click', onSwitchClick);
  };
  return view;
}

export function destroyCodeEditor(view: unknown) {
  if (view && typeof (view as EditorView).destroy === 'function') (view as EditorView).destroy();
}

function getEditorConfigOptions(ec: null | EditorConfig): Pick<EditorOptions, 'indentSize' | 'tabSize' | 'indentStyle'> {
  if (ec === null) {
    return {indentStyle: 'space'};
  }

  const opts: ReturnType<typeof getEditorConfigOptions> = {
    indentStyle: ec.indent_style,
  };
  if ('indent_size' in ec) opts.indentSize = Number(ec.indent_size);
  if ('tab_width' in ec) opts.tabSize = Number(ec.tab_width) || opts.indentSize;
  return opts;
}
