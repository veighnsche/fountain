import {initMarkupContent} from '../markup/content.js';
import {validateTextareaNonEmpty, initComboMarkdownEditor} from './comp/ComboMarkdownEditor.js';
import {fomanticMobileScreen} from '../modules/fomantic.js';
import {POST} from '../modules/fetch.js';
import {contributionRootSelector, createPageFeatureRun} from '../native-lifecycle.js';

let wikiState = null;

async function initRepoWikiFormEditor(root, run) {
  const form = root.querySelector('.repository.wiki.new .ui.form');
  const editorContainer = form?.querySelector('.combo-markdown-editor');
  const editArea = editorContainer?.querySelector('textarea');
  if (!editArea || editArea.closest(contributionRootSelector)) return;
  const editor = await initComboMarkdownEditor(editorContainer, {
    signal: run.signal,
    useScene: 'wiki',
    editorHeights: {minHeight: '300px', height: 'calc(100vh - 600px)'},
    previewMode: 'gfm',
    previewWiki: true,
    easyMDEOptions: {
      previewRender: (_content, previewTarget) => previewTarget.innerHTML,
      toolbar: ['bold', 'italic', 'strikethrough', '|',
        'heading-1', 'heading-2', 'heading-3', 'heading-bigger', 'heading-smaller', '|',
        'gitea-code-inline', 'code', 'quote', '|', 'gitea-checkbox-empty', 'gitea-checkbox-checked', '|',
        'unordered-list', 'ordered-list', '|',
        'link', 'image', 'table', 'horizontal-rule', '|',
        'preview', 'fullscreen', 'side-by-side', '|', 'gitea-switch-to-textarea',
      ],
    },
  });
  const release = run.own(() => editor.destroy?.());
  if (!run.current() || !root.isConnected) {
    release();
    return;
  }
  form.addEventListener('submit', (event) => {
    if (!validateTextareaNonEmpty(editArea)) {
      event.preventDefault();
      event.stopPropagation();
    }
  }, {signal: run.signal});
  let timer;
  let renderRequesting = false;
  let lastContent;
  const current = () => run.current() && root.isConnected;
  const schedule = (delay = 1000) => {
    if (current()) timer = setTimeout(renderEasyMDEPreview, delay);
  };
  async function renderEasyMDEPreview() {
    if (!current()) return;
    const previewFull = editorContainer.querySelector('.EasyMDEContainer .editor-preview-active');
    const previewSide = editorContainer.querySelector('.EasyMDEContainer .editor-preview-active-side');
    const previewTarget = previewSide || previewFull;
    const newContent = editArea.value;
    if (!previewTarget || lastContent === newContent || renderRequesting) {
      schedule();
      return;
    }
    renderRequesting = true;
    const formData = new FormData();
    formData.append('mode', editor.previewMode);
    formData.append('context', editor.previewContext);
    formData.append('text', newContent);
    formData.append('wiki', editor.previewWiki);
    try {
      const response = await POST(editor.previewUrl, {data: formData, signal: run.signal});
      if (!current()) return;
      const data = await response.text();
      if (!current()) return;
      lastContent = newContent;
      previewTarget.innerHTML = `<div class="markup ui segment">${data}</div>`;
      initMarkupContent();
    } catch (error) {
      if (current() && error?.name !== 'AbortError') console.error('Error rendering preview:', error);
    } finally {
      renderRequesting = false;
      schedule();
    }
  }
  run.own(() => clearTimeout(timer));
  schedule(0);
}

function collapseWikiTocForMobile(collapse) {
  if (collapse) document.querySelector('.wiki-content-toc details')?.removeAttribute('open');
}

export function initRepoWikiForm(run = createPageFeatureRun()) {
  const root = document.querySelector('.page-content.repository.wiki');
  if (!root || root.closest(contributionRootSelector)) return;
  if (wikiState?.root === root && wikiState.run.current()) {
    if (run !== wikiState.run) run.dispose();
    return wikiState.initialized;
  }
  retireRepoWikiForm();
  const state = {root, run, initialized: null};
  wikiState = state;
  const onTocChange = (event) => { if (run.current()) collapseWikiTocForMobile(event.matches); };
  fomanticMobileScreen.addEventListener('change', onTocChange);
  run.own(() => fomanticMobileScreen.removeEventListener('change', onTocChange));
  collapseWikiTocForMobile(fomanticMobileScreen.matches);
  state.initialized = initRepoWikiFormEditor(root, run).catch((error) => {
    run.dispose();
    throw error;
  });
  return state.initialized;
}

export function retireRepoWikiForm() {
  const state = wikiState;
  wikiState = null;
  state?.run.dispose();
}
