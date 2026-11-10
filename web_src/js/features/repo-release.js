import {hideElem, showElem} from '../utils/dom.js';
import {initComboMarkdownEditor} from './comp/ComboMarkdownEditor.js';
import {contributionRootSelector, createPageFeatureRun} from '../native-lifecycle.js';

let attachmentsController = null;
let releaseController = null;
let newAttachmentCount = 0;

export function retireRepoRelease() {
  attachmentsController?.dispose();
  attachmentsController = null;
}

export function retireRepoReleaseNew() {
  releaseController?.dispose();
  releaseController = null;
}

export function initRepoRelease(run = createPageFeatureRun()) {
  retireRepoRelease();
  attachmentsController = run;
  const {signal} = attachmentsController;
  for (const el of document.querySelectorAll('.remove-rel-attach')) {
    if (el.closest(contributionRootSelector)) continue;
    el.addEventListener('click', (e) => {
      const uuid = e.target.getAttribute('data-uuid');
      const id = e.target.getAttribute('data-id');
      document.querySelector(`input[name='attachment-del-${uuid}']`).value =
        'true';
      hideElem(`#attachment-${id}`);
    }, {signal});
  }
}

export function initRepoReleaseNew(run = createPageFeatureRun()) {
  retireRepoReleaseNew();
  const root = document.querySelector('.repository.new.release');
  if (!root || root.closest(contributionRootSelector)) return;
  releaseController = run;
  const {signal} = releaseController;
  newAttachmentCount = 0;

  initTagNameEditor(signal);
  const initialized = initRepoReleaseEditor(run).catch((error) => {
    run.dispose();
    if (error?.name !== 'AbortError') throw error;
  });
  initAddExternalLinkButton(signal);
  return initialized;
}

function initTagNameEditor(signal) {
  const el = document.getElementById('tag-name-editor');
  if (!el) return;

  const existingTags = JSON.parse(el.getAttribute('data-existing-tags'));
  if (!Array.isArray(existingTags)) return;

  const defaultTagHelperText = el.getAttribute('data-tag-helper');
  const newTagHelperText = el.getAttribute('data-tag-helper-new');
  const existingTagHelperText = el.getAttribute('data-tag-helper-existing');

  let previousTag = '';
  document.getElementById('tag-name').addEventListener('keyup', (e) => {
    const value = e.target.value;
    const tagHelper = document.getElementById('tag-helper');
    if (existingTags.includes(value)) {
      // If the tag already exists, hide the target branch selector.
      hideElem('#tag-target-selector');
      tagHelper.textContent = existingTagHelperText;
    } else {
      showElem('#tag-target-selector');
      tagHelper.textContent = value ? newTagHelperText : defaultTagHelperText;
    }

    const title_input = document.getElementById('release-title');
    if (!title_input.value || previousTag === title_input.value) {
      title_input.value = value;
    }
    previousTag = value;
  }, {signal});
}

async function initRepoReleaseEditor(run) {
  const editor = document.querySelector(
    '.repository.new.release .combo-markdown-editor',
  );
  if (!editor || editor.closest(contributionRootSelector)) {
    return;
  }
  const instance = await initComboMarkdownEditor(editor, {signal: run.signal});
  const release = run.own(() => instance.destroy());
  if (!run.current() || !editor.isConnected) release();
}

function initAddExternalLinkButton(signal) {
  const addExternalLinkButton = document.getElementById('add-external-link');
  if (!addExternalLinkButton) return;

  addExternalLinkButton.addEventListener('click', () => {
    newAttachmentCount += 1;
    const attachmentTemplate = document.getElementById('attachment-template');

    const newAttachment = attachmentTemplate.cloneNode(true);
    newAttachment.id = `attachment-N${newAttachmentCount}`;
    newAttachment.classList.remove('tw-hidden');

    const attachmentName = newAttachment.querySelector(
      'input[name="attachment-template-new-name"]',
    );
    attachmentName.name = `attachment-new-name-${newAttachmentCount}`;
    attachmentName.required = true;

    const attachmentExtUrl = newAttachment.querySelector(
      'input[name="attachment-template-new-exturl"]',
    );
    attachmentExtUrl.name = `attachment-new-exturl-${newAttachmentCount}`;
    attachmentExtUrl.required = true;

    const attachmentDel = newAttachment.querySelector('.remove-rel-attach');
    attachmentDel.addEventListener('click', () => {
      newAttachment.remove();
    }, {signal});

    attachmentTemplate.parentNode.insertBefore(
      newAttachment,
      attachmentTemplate,
    );
  }, {signal});
}
