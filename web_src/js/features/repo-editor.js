import $ from 'jquery';
import {htmlEscape} from 'escape-goat';
import {createCodeEditor, destroyCodeEditor} from './codeeditor.ts';
import {hideElem, showElem, createElementFromHTML} from '../utils/dom.js';
import {initMarkupContent} from '../markup/content.js';
import {attachRefIssueContextPopup} from './contextpopup.js';
import {POST} from '../modules/fetch.js';
import {initTab} from '../modules/tab.ts';
import {showModal} from '../modules/modal.ts';
import {contributionRootSelector, createPageFeatureRun} from '../native-lifecycle.js';

let editorRun = null;

function onPreviewClick(form, run) {
  return async function () {
    if (!run.current()) return;
    const $this = $(this);
    let context = `${$this.data('context')}/`;
    const mode = $this.data('markup-mode') || 'comment';
    const $treePathEl = form.find('input#tree_path');
    if ($treePathEl.length > 0) context += $treePathEl.val();
    context = context.substring(0, context.lastIndexOf('/'));
    const formData = new FormData();
    formData.append('mode', mode);
    formData.append('context', context);
    formData.append('branch_path', $this.data('branch-path'));
    formData.append('text', form.find(`.tab[data-tab="${form.find('.switch').data('write')}"] textarea`).val());
    formData.append('file_path', $treePathEl.val());
    const panel = form.find(`.tab[data-tab="${form.find('.switch').data('preview')}"]`);
    try {
      const response = await POST($this.data('url'), {data: formData, signal: run.signal});
      if (!run.current()) return;
      const data = await response.text();
      if (!run.current()) return;
      renderPreviewPanelContent(panel, data);
    } catch (error) {
      if (run.current() && error?.name !== 'AbortError') console.error('Error:', error);
    }
  };
}

function initEditPreviewTab($form, run) {
  const $tabMenu = $form.find('.switch');
  if (!$tabMenu.length) return;
  initTab($tabMenu[0], {signal: run.signal});
  const $previewTab = $tabMenu.find(`.item[data-tab="${$tabMenu.data('preview')}"]`);
  if ($previewTab.length) {
    const handler = onPreviewClick($form, run);
    $previewTab.on('click', handler);
    run.own(() => $previewTab.off('click', handler));
  }
}

function getCursorPosition($e) {
  const el = $e.get(0);
  let pos = 0;
  if ('selectionStart' in el) {
    pos = el.selectionStart;
  } else if ('selection' in document) {
    el.focus();
    const Sel = document.selection.createRange();
    const SelLength = document.selection.createRange().text.length;
    Sel.moveStart('character', -el.value.length);
    pos = Sel.text.length - SelLength;
  }
  return pos;
}

function joinTreePath($fileNameEl) {
  const parts = [];
  $('.breadcrumb span.section').each(function () {
    const $element = $(this);
    if ($element.find('a').length) parts.push($element.find('a').text());
    else parts.push($element.text());
  });
  if ($fileNameEl.val()) parts.push($fileNameEl.val());
  $('#tree_path').val(parts.join('/'));
}

function onQuickPullChange() {
  if ($(this).val() === 'commit-to-new-branch') {
    showElem('.quick-pull-branch-name');
    const input = document.querySelector('.quick-pull-branch-name input');
    if (input) input.required = true;
  } else {
    hideElem('.quick-pull-branch-name');
    const input = document.querySelector('.quick-pull-branch-name input');
    if (input) input.required = false;
  }
  const btn = document.getElementById('commit-button');
  if (btn) btn.textContent = this.getAttribute('button_text');
}

function onFilenameInput() {
  const parts = $(this).val().split('/');
  if (parts.length > 1) {
    for (let i = 0; i < parts.length; ++i) {
      const value = parts[i];
      if (i < parts.length - 1) {
        if (value.length) {
          this.before(createElementFromHTML(`<span class="section"><a href="#">${htmlEscape(value)}</a></span>`));
          this.before(createElementFromHTML(`<div class="breadcrumb-divider">/</div>`));
        }
      } else {
        $(this).val(value);
      }
      this.setSelectionRange(0, 0);
    }
  }
  joinTreePath($(this));
}

function onFilenameKeydown(e) {
  const $section = $('.breadcrumb span.section');
  if (e.code === 'Backspace' && getCursorPosition($(this)) === 0 && $section.length > 0) {
    e.preventDefault();
    const $divider = $('.breadcrumb .breadcrumb-divider');
    const value = $section.last().find('a').text();
    $(this).val(value + $(this).val());
    this.setSelectionRange(value.length, value.length);
    $section.last().remove();
    $divider.last().remove();
    joinTreePath($(this));
  }
}

export function initRepoEditor(run = createPageFeatureRun()) {
  const $form = $('.repository .edit.form');
  if ($form.length && $form[0].closest(contributionRootSelector)) return;
  retireRepoEditor();
  editorRun = run;
  initEditPreviewTab($form, run);

  for (const el of document.querySelectorAll('.js-quick-pull-choice-option')) {
    if (!el.closest(contributionRootSelector)) el.addEventListener('change', onQuickPullChange, {signal: run.signal});
  }
  const filename = document.getElementById('file-name');
  if (filename && !filename.closest(contributionRootSelector)) {
    filename.addEventListener('input', onFilenameInput, {signal: run.signal});
    filename.addEventListener('keydown', onFilenameKeydown, {signal: run.signal});
  }
  const $editArea = $('.repository.editor textarea#edit_area');
  if (!$editArea.length || $editArea[0].closest(contributionRootSelector) || !filename || filename.closest(contributionRootSelector)) return;
  const commitButton = document.getElementById('commit-button');
  const $editForm = $('.ui.edit.form');
  const hasPosted = $('input[name="page_has_posted"]').val() === 'true';
  const value = new URLSearchParams(window.location.search).get('value');
  return (async () => {
    const editor = await createCodeEditor($editArea[0], filename, run);
    const release = run.own(() => destroyCodeEditor(editor));
    if (!run.current() || !$editArea[0].isConnected) {
      release();
      return;
    }
    const dirtyFileClass = 'dirty-file';
    if (!hasPosted && commitButton) commitButton.disabled = true;
    $editForm.areYouSure({
      silent: true,
      dirtyClass: dirtyFileClass,
      fieldSelector: ':input:not(.commit-form-wrapper :input)',
      change($form2) {
        if (!run.current()) return;
        const dirty = $form2[0]?.classList.contains(dirtyFileClass);
        if (commitButton) commitButton.disabled = !dirty;
      },
    });
    if (value) editor.setValue(value);
    commitButton?.addEventListener('click', (event) => {
      if (!$editArea.val()) {
        event.preventDefault();
        showModal('edit-empty-content-modal', () => {
          if (run.current()) $form[0]?.requestSubmit();
        });
      }
    }, {signal: run.signal});
  })().catch((error) => {
    run.dispose();
    if (error?.name !== 'AbortError') throw error;
  });
}

export function retireRepoEditor() {
  const run = editorRun;
  editorRun = null;
  run?.dispose();
}

export function renderPreviewPanelContent($panelPreviewer, data) {
  $panelPreviewer.html(data);
  initMarkupContent();
  const $refIssues = $panelPreviewer.find('p .ref-issue');
  attachRefIssueContextPopup($refIssues);
}
