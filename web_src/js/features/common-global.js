import $ from 'jquery';
import '../vendor/jquery.are-you-sure.js';
import {clippie} from 'clippie';
import {createDropzone} from './dropzone.js';
import {showGlobalErrorMessage} from '../bootstrap.js';
import {handleGlobalEnterQuickSubmit} from './comp/QuickSubmit.js';
import {svg} from '../svg.js';
import {hideElem, showElem, toggleElem, resetForms, initSubmitEventPolyfill, submitEventSubmitter} from '../utils/dom.js';
import {htmlEscape} from 'escape-goat';
import {showTemporaryTooltip} from '../modules/tippy.js';
import {confirmModal} from './comp/ConfirmModal.js';
import {showErrorToast} from '../modules/toast.js';
import {request, POST, GET} from '../modules/fetch.js';
import '../htmx.js';
import {initTab} from '../modules/tab.ts';
import {contributionRootSelector, createPageFeatureRun} from '../native-lifecycle.js';
import {createFeatureRun} from '../native-feature-run.js';
import {getNativeModalRun, trackNativeModal} from '../modules/modal.ts';

const {appUrl, appSubUrl, i18n} = window.config;

// Dirty-form guards bind per form element, so they follow the page lifetime:
// fresh forms are guarded by enhanceNativeNodes and retired forms are dropped
// with their nodes (the vendor plugin has no destroy; its listeners die with
// the replaced node). Sign-in pages and 'ignore-dirty' forms stay exempt.
const enhancedForms = new Set();
const enhancedDropzones = new Map();

function scopedElements(root, selector) {
  const found = Array.from(root.querySelectorAll(selector));
  if (root.nodeType === 1 && root.matches(selector)) found.unshift(root);
  return found;
}

function enhanceNativeForms(root) {
  if ($('.user.signin').length) return;
  for (const el of scopedElements(root, 'form:not(.ignore-dirty)')) {
    if (enhancedForms.has(el) || el.closest(contributionRootSelector)) continue;
    enhancedForms.add(el);
    $(el).areYouSure();
  }
}

function enhanceNativeDropzones(root) {
  for (const el of scopedElements(root, '.dropzone')) {
    if (enhancedDropzones.has(el) || el.dropzone || el.closest(contributionRootSelector)) continue;
    initDropzone(el).catch((error) => {
      if (error.name !== 'AbortError') console.error(error);
    });
  }
}

export function initGlobalFormDirtyLeaveConfirm() {
  enhanceNativeForms(document);
}

let navbarToggleBound = false;
export function initHeadNavbarContentToggle() {
  if (navbarToggleBound) return;
  const navbar = document.getElementById('navbar');
  const btn = document.getElementById('navbar-expand-toggle');
  if (!navbar || !btn) return;
  navbarToggleBound = true;

  btn.addEventListener('click', () => {
    const isExpanded = btn.classList.contains('active');
    navbar.classList.toggle('navbar-menu-open', !isExpanded);
    btn.classList.toggle('active', !isExpanded);
  });
}

let languageMenuBound = false;
export function initFootLanguageMenu() {
  if (languageMenuBound) return;
  languageMenuBound = true;

  async function linkLanguageAction() {
    const $this = $(this);
    await GET($this.data('url'));
    window.location.reload();
  }

  $('.language-menu a[lang]').on('click', linkLanguageAction);
}

let quickSubmitBound = false;
export function initGlobalEnterQuickSubmit() {
  if (quickSubmitBound) return;
  quickSubmitBound = true;
  $(document).on('keydown', '.js-quick-submit', (e) => {
    if (e.target.closest(contributionRootSelector)) return;
    if (((e.ctrlKey && !e.altKey) || e.metaKey) && (e.key === 'Enter')) {
      handleGlobalEnterQuickSubmit(e.target);
      return false;
    }
  });
}

let buttonClickOnEnterBound = false;
export function initGlobalButtonClickOnEnter() {
  if (buttonClickOnEnterBound) return;
  buttonClickOnEnterBound = true;
  $(document).on('keypress', 'div.ui.button,span.ui.button', (e) => {
    if (e.target.closest(contributionRootSelector)) return;
    if (e.code === ' ' || e.code === 'Enter') {
      e.target.click();
      e.preventDefault();
    }
  });
}

// fetchActionDoRedirect does real redirection to bypass the browser's limitations of "location"
// more details are in the backend's fetch-redirect handler
function fetchActionDoRedirect(redirect) {
  const form = document.createElement('form');
  const input = document.createElement('input');
  form.method = 'post';
  form.action = `${appSubUrl}/-/fetch-redirect`;
  input.type = 'hidden';
  input.name = 'redirect';
  input.value = redirect;
  form.append(input);
  document.body.append(form);
  form.submit();
}

async function fetchActionDoRequest(actionElem, url, opt, run = createPageFeatureRun()) {
  try {
    const resp = await request(url, opt);
    if (!run.current()) return;
    if (resp.status === 200) {
      let {redirect} = await resp.json();
      if (!run.current()) return;
      redirect = redirect || actionElem.getAttribute('data-redirect');
      actionElem.classList.remove('dirty'); // remove the areYouSure check before reloading
      if (redirect) {
        fetchActionDoRedirect(redirect);
      } else {
        window.location.reload();
      }
      return;
    } else if (resp.status >= 400 && resp.status < 500) {
      const data = await resp.json();
      if (!run.current()) return;
      // the code was quite messy, sometimes the backend uses "err", sometimes it uses "error", and even "user_error"
      // but at the moment, as a new approach, we only use "errorMessage" here, backend can use JSONError() to respond.
      if (data.errorMessage) {
        showErrorToast(data.errorMessage, {useHtmlBody: data.renderFormat === 'html'});
      } else {
        showErrorToast(`server error: ${resp.status}`);
      }
    } else {
      showErrorToast(`server error: ${resp.status}`);
    }
  } catch (e) {
    if (run.current() && e.name !== 'AbortError') {
      console.error('error when doRequest', e);
      showErrorToast(`${i18n.network_error} ${e}`);
    }
  }
  if (run.current()) actionElem.classList.remove('is-loading', 'loading-icon-2px');
}

async function formFetchAction(e) {
  if (!e.target.classList.contains('form-fetch-action') || e.target.closest(contributionRootSelector)) return;

  e.preventDefault();
  const formEl = e.target;
  if (formEl.classList.contains('is-loading')) return;

  formEl.classList.add('is-loading');
  if (formEl.clientHeight < 50) {
    formEl.classList.add('loading-icon-2px');
  }

  const formMethod = formEl.getAttribute('method') || 'get';
  const formActionUrl = formEl.getAttribute('action');
  const formData = new FormData(formEl);
  const formSubmitter = submitEventSubmitter(e);
  const [submitterName, submitterValue] = [formSubmitter?.getAttribute('name'), formSubmitter?.getAttribute('value')];
  if (submitterName) {
    formData.append(submitterName, submitterValue || '');
  }

  let reqUrl = formActionUrl;
  const reqOpt = {method: formMethod.toUpperCase()};
  if (formMethod.toLowerCase() === 'get') {
    const params = new URLSearchParams();
    for (const [key, value] of formData) {
      params.append(key, value.toString());
    }
    const pos = reqUrl.indexOf('?');
    if (pos !== -1) {
      reqUrl = reqUrl.slice(0, pos);
    }
    reqUrl += `?${params.toString()}`;
  } else {
    reqOpt.body = formData;
  }

  await fetchActionDoRequest(formEl, reqUrl, reqOpt);
}

// Document-level listeners: registered once as a global feature, never
// retired or re-attached across native transitions. Repeat calls are safe:
// both listeners are module-level function references, so the DOM ignores
// duplicate addEventListener registrations.
export function initGlobalCommonListeners() {
  initSubmitEventPolyfill();
  document.addEventListener('submit', formFetchAction);
  document.addEventListener('click', linkAction);
}

// Elements already enhanced. Retire drops transplanted ones so the outgoing
// region's instances are destroyed and never pinned; live chrome stays.
const enhancedDropdowns = new Set();
const enhancedTabs = new Set();

// Enhances fresh nodes only: fomantic dropdowns and tabs inside root.
// Called with document at boot and after every native transition through the
// page seam; already-enhanced nodes are skipped, never double-bound.
export function enhanceNativeNodes(root = document) {
  // Semantic UI modules.
  // "custom" dropdowns are managed by their own feature code: they never
  // enter the enhancement tracker, so retire never destroys them.
  const $uiDropdowns = $(root).find('.ui.dropdown').addBack('.ui.dropdown')
    .filter((_, el) => !el.classList.contains('custom') && !enhancedDropdowns.has(el) && !el.closest(contributionRootSelector));

  $uiDropdowns.dropdown();

  // The "jump" means this dropdown is mainly used for "menu" purpose,
  // clicking an item will jump to somewhere else or trigger an action/function.
  // When a dropdown is used for non-refresh actions with tippy,
  // it must have this "jump" class to hide the tippy when dropdown is closed.
  $uiDropdowns.filter('.jump').dropdown({
    action: 'hide',
    onShow() {
      // hide associated tooltip while dropdown is open
      this._tippy?.hide();
      this._tippy?.disable();
    },
    onHide() {
      this._tippy?.enable();

      // hide all tippy elements of items after a while. eg: use Enter to click "Copy Link" in the Issue Context Menu
      setTimeout(() => {
        const $dropdown = $(this);
        if ($dropdown.dropdown('is hidden')) {
          $(this).find('.menu > .item').each((_, item) => {
            item._tippy?.hide();
          });
        }
      }, 2000);
    },
  });

  // Special popup-directions, prevent Fomantic from guessing the popup direction.
  // With default "direction: auto", if the viewport height is small, Fomantic would show the popup upward,
  //   if the dropdown is at the beginning of the page, then the top part would be clipped by the window view.
  //   eg: Issue List "Sort" dropdown
  // But we can not set "direction: downward" for all dropdowns, because there is a bug in dropdown menu positioning when calculating the "left" position,
  //   which would make some dropdown popups slightly shift out of the right viewport edge in some cases.
  //   eg: the "Create New Repo" menu on the navbar.
  $uiDropdowns.filter('.upward').dropdown('setting', 'direction', 'upward');
  $uiDropdowns.filter('.downward').dropdown('setting', 'direction', 'downward');

  for (const el of $uiDropdowns.get()) {
    enhancedDropdowns.add(el);
  }

  for (const el of root.querySelectorAll('.tabular.menu')) {
    if (enhancedTabs.has(el) || el.closest(contributionRootSelector)) continue;
    initTab(el);
    enhancedTabs.add(el);
  }

  // Form helpers share the page lifetime: fresh forms, disabled modal inputs
  // and dropzones in the incoming region are enhanced exactly once.
  enhanceNativeForms(root);
  initDisabledInputs(root);
  enhanceNativeDropzones(root);
}

// Destroys fomantic instances in the outgoing transplant region (.page-content)
// and drops all detached tracked nodes so retired pages are never pinned.
// Live chrome and persistent contribution homes are untouched.
export function retireNativeNodes() {
  let firstError;
  // Tooltip poppers are appended outside their target, so removing the
  // outgoing region alone would orphan them; destroy region-owned
  // instances while the region is still attached (retire precedes swap).
  for (const el of document.querySelectorAll('.page-content, .page-content *')) {
    if (el.closest(contributionRootSelector)) continue;
    el._tippy?.destroy();
  }
  for (const el of enhancedDropdowns) {
    if (el.closest('.page-content')) {
      if (el.isConnected) $(el).dropdown('destroy');
      enhancedDropdowns.delete(el);
    } else if (!el.isConnected) {
      enhancedDropdowns.delete(el);
    }
  }
  // Tab listeners are element-scoped anonymous closures that die with their
  // nodes; retire/swap is synchronous so no event can interleave. Dropping
  // the tracked region nodes releases the whole subtree for collection.
  for (const el of enhancedTabs) {
    if (!el.isConnected || el.closest('.page-content')) {
      enhancedTabs.delete(el);
    }
  }
  for (const el of enhancedForms) {
    if (!el.isConnected || el.closest('.page-content')) {
      enhancedForms.delete(el);
    }
  }
  for (const [el, state] of enhancedDropzones) {
    if (!el.isConnected || el.closest('.page-content')) {
      enhancedDropzones.delete(el);
      try {
        state.run.dispose();
      } catch (error) {
        firstError ??= error;
      }
    }
  }
  if (firstError) throw firstError;
}

// Sometimes unrelated inputs are stored in forms for convenience, for example,
// modal inputs. To prevent them from blocking the forms for noJS clients they
// are disabled by default. TypeScript: root is HTMLElement
export function initDisabledInputs(root) {
  for (const el of scopedElements(root, 'input.js-enable[disabled]')) {
    if (el.closest(contributionRootSelector)) continue;
    el.removeAttribute('disabled');
  }
}

export function initGlobalDropzone() {
  enhanceNativeDropzones(document);
}

export function initDropzone(dropzoneEl, zone = undefined, run = undefined) {
  if (!dropzoneEl) return Promise.resolve();
  const existing = enhancedDropzones.get(dropzoneEl);
  if (existing) return existing.promise;
  run ??= getNativeModalRun(dropzoneEl) ?? (dropzoneEl.closest('.page-content') ? createPageFeatureRun() : createFeatureRun());
  const state = {run};
  enhancedDropzones.set(dropzoneEl, state);
  state.promise = mountDropzone(dropzoneEl, zone, run).catch((error) => {
    if (enhancedDropzones.get(dropzoneEl) === state) enhancedDropzones.delete(dropzoneEl);
    throw error;
  });
  return state.promise;
}

async function mountDropzone(dropzoneEl, zone, run) {
  let disableRemovedfileEvent = false; // when resetting the dropzone (removeAllFiles), disable the "removedfile" event
  let fileUuidDict = {}; // to record: if a comment has been saved, then the uploaded files won't be deleted from server when clicking the Remove in the dropzone

  const initFilePreview = (file, data, isReload = false) => {
    if (!run.current()) return;
    file.uuid = data.uuid;
    fileUuidDict[file.uuid] = {submitted: isReload};
    const input = document.createElement('input');
    input.id = data.uuid;
    input.name = 'files';
    input.type = 'hidden';
    input.value = data.uuid;
    const inputPath = document.createElement('input');
    inputPath.name = `files_fullpath[${data.uuid}]`;
    inputPath.type = 'hidden';
    inputPath.value = htmlEscape(file.fullPath || file.name);
    dropzoneEl.querySelector('.files').append(input, inputPath);

    // Create a "Copy Link" element, to conveniently copy the image
    // or file link as Markdown to the clipboard
    const copyLinkElement = document.createElement('div');
    copyLinkElement.className = 'tw-text-center';
    // The a element has a hardcoded cursor: pointer because the default is overridden by .dropzone
    copyLinkElement.innerHTML = `<a href="#" style="cursor: pointer;">${svg('octicon-copy', 14, 'copy link')} Copy link</a>`;
    copyLinkElement.addEventListener('click', async (e) => {
      e.preventDefault();
      const name = file.name.slice(0, file.name.lastIndexOf('.'));
      let fileMarkdown = `[${name}](/attachments/${file.uuid})`;
      if (file.type.startsWith('image/')) {
        fileMarkdown = `!${fileMarkdown}`;
      } else if (file.type.startsWith('video/')) {
        fileMarkdown = `<video src="/attachments/${file.uuid}" title="${htmlEscape(name)}" controls></video>`;
      }
      try {
        const success = await clippie(fileMarkdown);
        if (run.current()) showTemporaryTooltip(e.target, success ? i18n.copy_success : i18n.copy_error);
      } catch (error) {
        if (run.current()) console.error(error);
      }
    });
    file.previewTemplate.append(copyLinkElement);
  };
  const updateDropzoneState = () => {
    if (!run.current()) return;
    dropzoneEl.classList.toggle('dz-started', dropzoneEl.querySelector('.dz-preview'));
  };

  const dz = await createDropzone(dropzoneEl, {
    url: dropzoneEl.getAttribute('data-upload-url'),
    maxFiles: dropzoneEl.getAttribute('data-max-file'),
    maxFilesize: dropzoneEl.getAttribute('data-max-size'),
    acceptedFiles: (['*/*', ''].includes(dropzoneEl.getAttribute('data-accepts')) ? null : dropzoneEl.getAttribute('data-accepts')),
    addRemoveLinks: true,
    dictDefaultMessage: dropzoneEl.getAttribute('data-default-message'),
    dictInvalidFileType: dropzoneEl.getAttribute('data-invalid-input-type'),
    dictFileTooBig: dropzoneEl.getAttribute('data-file-too-big'),
    dictRemoveFile: dropzoneEl.getAttribute('data-remove-file'),
    timeout: 0,
    thumbnailMethod: 'contain',
    thumbnailWidth: 480,
    thumbnailHeight: 480,
    init() {
      this.on('success', initFilePreview);
      this.on('removedfile', async (file) => {
        if (!run.current() || disableRemovedfileEvent) return;
        document.getElementById(file.uuid)?.remove();
        document.querySelector(`input[name="files_fullpath[${file.uuid}]"]`)?.remove();
        if (dropzoneEl.getAttribute('data-remove-url') && !fileUuidDict[file.uuid].submitted) {
          try {
            await POST(dropzoneEl.getAttribute('data-remove-url'), {data: new URLSearchParams({file: file.uuid})});
          } catch (error) {
            if (run.current()) console.error(error);
          }
        }
        updateDropzoneState();
      });
      this.on('error', function (file, message) {
        if (!run.current()) return;
        showErrorToast(message);
        this.removeFile(file);
      });
      this.on('reload', async () => {
        if (!run.current() || !zone || !dz.removeAllFiles) return;
        try {
          const response = await GET(zone.getAttribute('data-attachment-url'), {signal: run.signal});
          const data = await response.json();
          if (!run.current()) return;
          // do not trigger the "removedfile" event, otherwise the attachments would be deleted from server
          disableRemovedfileEvent = true;
          dz.removeAllFiles(true);
          dropzoneEl.querySelector('.files').innerHTML = '';
          for (const element of dropzoneEl.querySelectorAll('.dz-preview')) element.remove();
          fileUuidDict = {};
          disableRemovedfileEvent = false;

          for (const attachment of data) {
            attachment.type = attachment.mime_type;
            dz.emit('addedfile', attachment);
            dz.emit('complete', attachment);
            if (attachment.type.startsWith('image/')) {
              const imgSrc = `${dropzoneEl.getAttribute('data-link-url')}/${attachment.uuid}`;
              dz.emit('thumbnail', attachment, imgSrc);
            }
            initFilePreview(attachment, {uuid: attachment.uuid}, true);
            fileUuidDict[attachment.uuid] = {submitted: true};
          }
        } catch (error) {
          if (run.current()) console.error(error);
        }
        updateDropzoneState();
      });
      this.on('create-thumbnail', (attachment, file) => {
        if (!run.current()) return;
        if (attachment.type && /image.*/.test(attachment.type)) {
          // When a new issue is created, a thumbnail cannot be fetch, so we need to create it locally.
          // The implementation is took from the dropzone library (`dropzone.js` > `_processThumbnailQueue()`)
          dz.createThumbnail(
            file,
            dz.options.thumbnailWidth,
            dz.options.thumbnailHeight,
            dz.options.thumbnailMethod,
            true,
            (dataUrl) => {
              if (run.current()) dz.emit('thumbnail', attachment, dataUrl);
            },
          );
        }
      });
    },
  }, run);
  run.own(() => {
    disableRemovedfileEvent = true;
    dz.destroy();
  });
}

async function linkAction(e) {
  // A "link-action" can post AJAX request to its "data-url"
  // Then the browser is redirected to: the "redirect" in response, or "data-redirect" attribute, or current URL by reloading.
  // If the "link-action" has "data-modal-confirm" attribute, a confirm modal dialog will be shown before taking action.
  const el = e.target.closest('.link-action');
  if (!el || el.closest(contributionRootSelector)) return;

  e.preventDefault();
  const run = createPageFeatureRun();
  const url = el.getAttribute('data-url');
  const doRequest = async () => {
    if (!run.current()) return;
    el.disabled = true;
    await fetchActionDoRequest(el, url, {method: 'POST'}, run);
    if (run.current()) el.disabled = false;
  };

  const modalConfirmContent = htmlEscape(el.getAttribute('data-modal-confirm') || '');
  if (!modalConfirmContent) {
    await doRequest();
    return;
  }

  const isRisky = el.classList.contains('red') || el.classList.contains('yellow') || el.classList.contains('orange') || el.classList.contains('negative');
  if (await confirmModal({content: modalConfirmContent, buttonColor: isRisky ? 'orange' : 'primary'}, run)) {
    await doRequest();
  }
}

function showDeletePopup(e, trigger = e.target) {
  if (trigger.closest(contributionRootSelector)) return;
  e.preventDefault();
  const run = createPageFeatureRun();
  const $this = $(trigger);
  const dataArray = $this.data();

  const modalID = $this[0].getAttribute('data-modal-id');
  if (!modalID) {
    throw new Error('This button does not specify which modal it wants to open.');
  }

  const $dialog = $(`#${modalID}`);
  const current = trackNativeModal($dialog[0], () => {
    $dialog.modal('hide').modal('destroy');
  });
  if (!current()) return;
  $dialog.find('.name').text($this.data('name'));
  for (const [key, value] of Object.entries(dataArray)) {
    if (key && key.startsWith('data')) {
      $dialog.find(`.${key}`).text(value);
    }
  }

  $dialog.modal({
    closable: false,
    onApprove: async () => {
      if (!run.current() || !current()) return false;
      if ($this.data('type') === 'form') {
        document.querySelector($this.data('form')).requestSubmit();
        return;
      }
      if ($this[0].getAttribute('hx-confirm')) {
        e.detail.issueRequest(true);
        return;
      }
      const postData = new FormData();
      for (const [key, value] of Object.entries(dataArray)) {
        if (key && key.startsWith('data')) {
          postData.append(key.slice(4), value);
        }
        if (key === 'id') {
          postData.append('id', value);
        }
      }

      try {
        const response = await POST($this.data('url'), {data: postData});
        if (!run.current()) return;
        if (response.ok) {
          const data = await response.json();
          if (run.current()) window.location.href = data.redirect;
        }
      } catch (error) {
        if (run.current()) console.error(error);
      }
    },
  }).modal('show');
}

// Document-delegated and registered once as a global feature: fresh
// transplanted delete buttons work without rebinding, and repeated init
// calls (e.g. from the comment editor) can never stack a second handler.
let linkActionsBound = false;
export function initGlobalLinkActions() {
  if (linkActionsBound) return;
  linkActionsBound = true;
  document.addEventListener('click', (e) => {
    const trigger = e.target.closest('.delete-button');
    if (trigger) showDeletePopup(e, trigger);
  });

  document.addEventListener('htmx:confirm', (e) => {
    if ((e.detail?.elt ?? e.target).closest?.(contributionRootSelector)) return;
    e.preventDefault();
    // htmx:confirm is triggered for every HTMX request, even those that don't
    // have the `hx-confirm` attribute specified. To avoid opening modals for
    // those elements, check if 'e.detail.question' is empty, which contains the
    // value of the `hx-confirm` attribute.
    if (!e.detail.question) {
      e.detail.issueRequest(true);
    } else {
      showDeletePopup(e);
    }
  });
}

// A ".show-modal" button shows the modal defined by its "data-modal" attribute.
// Delegated once on the document, so transplanted buttons need no rebinding
// and repeated init calls cannot stack duplicate modal actions.
function showModalAction(e) {
  const trigger = e.target.closest('.show-modal');
  if (!trigger || trigger.closest(contributionRootSelector)) return;
  e.preventDefault();
  const modalSelector = trigger.getAttribute('data-modal');
  const $modal = $(modalSelector);
  if (!$modal.length) {
    throw new Error('no modal for this action');
  }
  const current = trackNativeModal($modal[0], () => {
    $modal.modal('hide').modal('destroy');
  });
  if (!current()) return;
  const modalAttrPrefix = 'data-modal-';
  for (const attrib of trigger.attributes) {
    if (!attrib.name.startsWith(modalAttrPrefix)) {
      continue;
    }

    const attrTargetCombo = attrib.name.substring(modalAttrPrefix.length);
    const [attrTargetName, attrTargetAttr] = attrTargetCombo.split('.');
    // try to find target by: "#target" -> ".target" -> "target tag"
    let $attrTarget = $modal.find(`#${attrTargetName}`);
    if (!$attrTarget.length) $attrTarget = $modal.find(`.${attrTargetName}`);
    if (!$attrTarget.length) $attrTarget = $modal.find(`${attrTargetName}`);
    if (!$attrTarget.length) continue; // TODO: show errors in dev mode to remind developers that there is a bug

    if (attrTargetAttr) {
      $attrTarget[0][attrTargetAttr] = attrib.value;
    } else if ($attrTarget[0].matches('input, textarea')) {
      $attrTarget.val(attrib.value); // FIXME: add more supports like checkbox
    } else {
      $attrTarget.text(attrib.value); // FIXME: it should be more strict here, only handle div/span/p
    }
  }

  $modal.modal('setting', {
    onApprove: () => {
      if (!current()) return false;
      // "form-fetch-action" can handle network errors gracefully,
      // so keep the modal dialog to make users can re-submit the form if anything wrong happens.
      if ($modal.find('.form-fetch-action').length) return false;
    },
  }).modal('show');
}

let showModalBound = false;
export function initGlobalShowModal() {
  if (showModalBound) return;
  showModalBound = true;
  document.addEventListener('click', showModalAction);
}

// Panel toggles query live DOM at event time, so one delegated document
// handler covers transplanted buttons with no per-element rebinding.
function panelAction(e) {
  if (e.target.closest(contributionRootSelector)) return;
  const showButton = e.target.closest('.show-panel');
  if (showButton) {
    // a '.show-panel' element can show a panel, by `data-panel="selector"`
    // if it has "toggle" class, it toggles the panel
    e.preventDefault();
    const sel = showButton.getAttribute('data-panel');
    if (showButton.classList.contains('toggle')) {
      toggleElem(sel);
    } else {
      showElem(sel);
    }
    return;
  }
  const hideButton = e.target.closest('.hide-panel');
  if (!hideButton) return;
  // a `.hide-panel` element can hide a panel, by `data-panel="selector"` or `data-panel-closest="selector"`
  e.preventDefault();
  let sel = hideButton.getAttribute('data-panel');
  if (sel) {
    const element = document.querySelector(sel);
    hideElem(element);
    resetForms(element);
    return;
  }
  sel = hideButton.getAttribute('data-panel-closest');
  if (sel) {
    const element = hideButton.closest(sel);
    hideElem(element);
    resetForms(element);
    return;
  }
  // should never happen, otherwise there is a bug in code
  showErrorToast('Nothing to hide');
}

let globalButtonsBound = false;
export function initGlobalButtons() {
  if (!globalButtonsBound) {
    globalButtonsBound = true;
    // There are many "cancel button" elements in modal dialogs, Fomantic UI expects they are button-like elements but never submit a form.
    // However, Gitea misuses the modal dialog and put the cancel buttons inside forms, so we must prevent the form submission.
    // There are a few cancel buttons in non-modal forms, and there are some dynamically created forms (eg: the "Edit Issue Content")
    document.addEventListener('click', (e) => {
      if (e.target.closest(contributionRootSelector)) return;
      if (e.target.matches('form button.ui.cancel.button')) {
        e.preventDefault();
      }
    });
    document.addEventListener('click', panelAction);
  }

  initGlobalShowModal();
}

/**
 * Too many users set their ROOT_URL to wrong value, and it causes a lot of problems:
 *   * Cross-origin API request without correct cookie
 *   * Incorrect href in <a>
 *   * ...
 * So we check whether current URL starts with AppUrl(ROOT_URL).
 * If they don't match, show a warning to users.
 */
export function checkAppUrl() {
  const curUrl = window.location.href;
  // some users visit "https://domain/gitea" while appUrl is "https://domain/gitea/", there should be no warning
  if (curUrl.startsWith(appUrl) || `${curUrl}/` === appUrl) {
    return;
  }

  showGlobalErrorMessage(i18n.incorrect_root_url);
}
