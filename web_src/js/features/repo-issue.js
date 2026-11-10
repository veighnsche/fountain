import $ from 'jquery';
import {htmlEscape} from 'escape-goat';
import {showTemporaryTooltip, createTippy} from '../modules/tippy.js';
import {hideElem, showElem, toggleElem} from '../utils/dom.js';
import {setFileFolding} from './file-fold.js';
import {getComboMarkdownEditor, initComboMarkdownEditor} from './comp/ComboMarkdownEditor.js';
import {toAbsoluteUrl} from '../utils.js';
import {initDropzone} from './common-global.js';
import {POST, GET} from '../modules/fetch.js';
import {showErrorToast} from '../modules/toast.js';
import {contributionRootSelector, createPageFeatureRun} from '../native-lifecycle.js';

const {appSubUrl} = window.config;

// Document-delegated handlers query the live DOM at event time, so they
// are installed once per document and never retired or stacked.
const installedDocumentHandlers = new Set();
function installDocumentHandlersOnce(key, install) {
  if (installedDocumentHandlers.has(key)) return;
  installedDocumentHandlers.add(key);
  install();
}

// Element-bound listeners and widget instances owned by the current page,
// grouped by feature bucket so each retire removes exactly its own init.
const nativeBindings = new Map();
const jqueryBindings = new Map();
const widgetInstances = new Map();
let pullUpdateRun = null;
let wipToggleRun = null;
let reviewRun = null;
let commentEditorsRun = null;
let issueCommentsRun = null;
let assignMeRun = null;

function bucket(map, name) {
  if (!map.has(name)) map.set(name, []);
  return map.get(name);
}

function bindNative(name, el, type, handler) {
  if (bucket(nativeBindings, name).some(([boundEl, boundType]) => boundEl === el && boundType === type)) return;
  el.addEventListener(type, handler);
  bucket(nativeBindings, name).push([el, type, handler]);
}

function unbindNative(name) {
  for (const [el, type, handler] of bucket(nativeBindings, name)) {
    el.removeEventListener(type, handler);
  }
  nativeBindings.delete(name);
}

function bindJQuery(name, $el, type, handler) {
  const bindings = bucket(jqueryBindings, name);
  for (const el of $el.get()) {
    if (bindings.some(([boundEl, boundType]) => boundEl === el && boundType === type)) continue;
    bindings.push([el, type, handler]);
    $(el).on(type, handler);
  }
}

function unbindJQuery(name) {
  for (const [el, type, handler] of bucket(jqueryBindings, name)) {
    $(el).off(type, handler);
  }
  jqueryBindings.delete(name);
}

function trackWidget(name, el, destroy) {
  if (!el || bucket(widgetInstances, name).some(([trackedEl]) => trackedEl === el)) return;
  bucket(widgetInstances, name).push([el, destroy]);
}

function destroyWidgets(name) {
  for (const [el, destroy] of bucket(widgetInstances, name)) {
    destroy(el);
  }
  widgetInstances.delete(name);
}

// if there are draft comments, confirm before reloading, to avoid losing comments
export function reloadConfirmDraftComment() {
  const commentTextareas = [
    document.querySelector('.edit-content-zone:not(.tw-hidden) textarea'),
    document.querySelector('#comment-form textarea'),
  ];
  for (const textarea of commentTextareas) {
    // Most users won't feel too sad if they lose a comment with 10 chars, they can re-type these in seconds.
    // But if they have typed more (like 50) chars and the comment is lost, they will be very unhappy.
    if (textarea && textarea.value.trim().length > 10) {
      textarea.parentElement.scrollIntoView();
      if (!window.confirm('Page will be reloaded, but there are draft comments. Continuing to reload will discard the comments. Continue?')) {
        return;
      }
      break;
    }
  }
  window.location.reload();
}

export function initRepoIssueTimeTracking() {
  installDocumentHandlersOnce('repo-issue-time-tracking', () => {
    $(document).on('click', '.issue-add-time', () => {
      $('.issue-start-time-modal').modal({
        duration: 200,
        onApprove() {
          document.getElementById('add_time_manual_form').requestSubmit();
        },
      }).modal('show');
      $('.issue-start-time-modal input').on('keydown', (e) => {
        if ((e.keyCode || e.key) === 13) {
          document.getElementById('add_time_manual_form').requestSubmit();
        }
      });
    });
    $(document).on('click', '.issue-start-time, .issue-stop-time', () => {
      document.getElementById('toggle_stopwatch_form').requestSubmit();
    });
    $(document).on('click', '.issue-cancel-time', () => {
      document.getElementById('cancel_stopwatch_form').requestSubmit();
    });
    $(document).on('click', 'button.issue-delete-time', function () {
      const sel = `.issue-delete-time-modal[data-id="${$(this).data('id')}"]`;
      $(sel).modal({
        duration: 200,
        onApprove() {
          document.querySelector(`${sel} form`).requestSubmit();
        },
      }).modal('show');
    });
  });
}

async function updateDeadline(deadlineString) {
  hideElem('#deadline-err-invalid-date');
  document.getElementById('deadline-loader')?.classList.add('is-loading');

  let realDeadline = null;
  if (deadlineString !== '') {
    const newDate = Date.parse(deadlineString);

    if (Number.isNaN(newDate)) {
      document.getElementById('deadline-loader')?.classList.remove('is-loading');
      showElem('#deadline-err-invalid-date');
      return false;
    }
    realDeadline = new Date(newDate);
  }

  try {
    const response = await POST(document.getElementById('update-issue-deadline-form').getAttribute('action'), {
      data: {due_date: realDeadline},
    });

    if (response.ok) {
      window.location.reload();
    } else {
      throw new Error('Invalid response');
    }
  } catch (error) {
    console.error(error);
    document.getElementById('deadline-loader').classList.remove('is-loading');
    showElem('#deadline-err-invalid-date');
  }
}

export function initRepoIssueDue() {
  installDocumentHandlersOnce('repo-issue-due', () => {
    $(document).on('click', '.issue-due-edit', () => {
      toggleElem('#deadlineForm');
    });
    $(document).on('click', '.issue-due-remove', () => {
      updateDeadline('');
    });
    $(document).on('submit', '.issue-due-form', () => {
      updateDeadline($('#deadlineDate').val());
      return false;
    });
  });
}

export function initRepoIssueCommentDelete() {
  installDocumentHandlersOnce('repo-issue-comment-delete', () => {
  // Delete comment
    document.addEventListener('click', async (e) => {
      if (!e.target.matches('.delete-comment')) return;
      e.preventDefault();

      const deleteButton = e.target;
      if (window.confirm(deleteButton.getAttribute('data-locale'))) {
        try {
          const response = await POST(deleteButton.getAttribute('data-url'));
          if (!response.ok) throw new Error('Failed to delete comment');

          const conversationHolder = deleteButton.closest('.conversation-holder');
          const parentTimelineItem = deleteButton.closest('.timeline-item');
          const parentTimelineGroup = deleteButton.closest('.timeline-item-group');

        // Check if this was a pending comment.
          if (conversationHolder?.querySelector('.pending-label')) {
            const counter = document.querySelector('#review-box .review-comments-counter');
            let num = parseInt(counter?.getAttribute('data-pending-comment-number')) - 1 || 0;
            num = Math.max(num, 0);
            counter.setAttribute('data-pending-comment-number', num);
            counter.textContent = String(num);
          }

          document.getElementById(deleteButton.getAttribute('data-comment-id'))?.remove();

          if (conversationHolder && !conversationHolder.querySelector('.comment')) {
            const path = conversationHolder.getAttribute('data-path');
            const side = conversationHolder.getAttribute('data-side');
            const idx = conversationHolder.getAttribute('data-idx');
            const lineType = conversationHolder.closest('tr')?.getAttribute('data-line-type');

          // the conversation holder could appear either on the "Conversation" page, or the "Files Changed" page
          // on the Conversation page, there is no parent "tr", so no need to do anything for "add-code-comment"
            if (lineType) {
              if (lineType === 'same') {
                document.querySelector(`[data-path="${path}"] .add-code-comment[data-idx="${idx}"]`).classList.remove('tw-invisible');
              } else {
                document.querySelector(`[data-path="${path}"] .add-code-comment[data-side="${side}"][data-idx="${idx}"]`).classList.remove('tw-invisible');
              }
            }
            conversationHolder.remove();
          }

        // Check if there is no review content, move the time avatar upward to avoid overlapping the content below.
          if (!parentTimelineGroup?.querySelector('.timeline-item.comment') && !parentTimelineItem?.querySelector('.conversation-holder')) {
            const timelineAvatar = parentTimelineGroup?.querySelector('.timeline-avatar');
            timelineAvatar?.classList.remove('timeline-avatar-offset');
          }
        } catch (error) {
          console.error(error);
        }
      }
    });
  });
}

export function initRepoIssueDependencyDelete() {
  installDocumentHandlersOnce('repo-issue-dependency-delete', () => {
  // Delete Issue dependency
    $(document).on('click', '.delete-dependency-button', (e) => {
      const id = e.currentTarget.getAttribute('data-id');
      const type = e.currentTarget.getAttribute('data-type');

      $('.remove-dependency').modal({
        closable: false,
        duration: 200,
        onApprove: () => {
          $('#removeDependencyID').val(id);
          $('#dependencyType').val(type);
          document.getElementById('removeDependencyForm').requestSubmit();
        },
      }).modal('show');
    });
  });
}

export function initRepoIssueCodeCommentCancel() {
  installDocumentHandlersOnce('repo-issue-code-comment-cancel', () => {
  // Cancel inline code comment
    document.addEventListener('click', (e) => {
      if (!e.target.matches('.cancel-code-comment')) return;

      const form = e.target.closest('form');
      if (form?.classList.contains('comment-form')) {
        hideElem(form);
        showElem(form.closest('.comment-code-cloud')?.querySelectorAll('button.comment-form-reply'));
      } else {
        form.closest('.comment-code-cloud')?.remove();
      }
    });
  });
}

export function initRepoPullRequestUpdate(run = createPageFeatureRun()) {
  // Pull Request update button
  const pullUpdateButton = document.querySelector('.update-button > button');
  if (!pullUpdateButton || pullUpdateButton.closest(contributionRootSelector)) return;
  if (bucket(nativeBindings, 'pull-update').some(([element]) => element === pullUpdateButton)) return;
  retireRepoPullRequestUpdate();
  pullUpdateRun = run;

  bindNative('pull-update', pullUpdateButton, 'click', async (e) => {
    e.preventDefault();
    if (!run.current() || pullUpdateButton.classList.contains('is-loading')) return;
    const redirect = pullUpdateButton.getAttribute('data-redirect');
    pullUpdateButton.classList.add('is-loading');
    let response;
    try {
      response = await POST(pullUpdateButton.getAttribute('data-do'));
    } catch (error) {
      if (run.current()) console.error(error);
    } finally {
      if (run.current()) pullUpdateButton.classList.remove('is-loading');
    }
    if (!run.current()) return;
    let data;
    try {
      data = await response?.json(); // the response is probably not a JSON
    } catch (error) {
      if (run.current()) console.error(error);
    }
    if (!run.current()) return;
    if (data?.redirect) {
      window.location.href = data.redirect;
    } else if (redirect) {
      window.location.href = redirect;
    } else {
      window.location.reload();
    }
  });

  const $updateDropdown = $('.update-button > .dropdown');
  $updateDropdown.dropdown({
    onChange(_text, _value, $choice) {
      const url = $choice[0].getAttribute('data-do');
      if (url) {
        const buttonText = pullUpdateButton.querySelector('.button-text');
        if (buttonText) {
          buttonText.textContent = $choice.text();
        }
        pullUpdateButton.setAttribute('data-do', url);
      }
    },
  });
  trackWidget('pull-update', $updateDropdown[0], (el) => $(el).dropdown('destroy'));
}

export function retireRepoPullRequestUpdate() {
  pullUpdateRun?.dispose();
  pullUpdateRun = null;
  unbindNative('pull-update');
  destroyWidgets('pull-update');
}

export function initRepoPullRequestAllowMaintainerEdit() {
  const wrapper = document.getElementById('allow-edits-from-maintainers');
  if (!wrapper || wrapper.closest(contributionRootSelector)) return;
  const checkbox = wrapper.querySelector('input[type="checkbox"]');
  bindNative('maintainer-edit', checkbox, 'input', async () => {
    const url = `${wrapper.getAttribute('data-url')}/set_allow_maintainer_edit`;
    wrapper.classList.add('is-loading');
    try {
      const resp = await POST(url, {data: new URLSearchParams({allow_maintainer_edit: checkbox.checked})});
      if (!resp.ok) {
        throw new Error('Failed to update maintainer edit permission');
      }
      const data = await resp.json();
      checkbox.checked = data.allow_maintainer_edit;
    } catch (error) {
      checkbox.checked = !checkbox.checked;
      console.error(error);
      showTemporaryTooltip(wrapper, wrapper.getAttribute('data-prompt-error'));
    } finally {
      wrapper.classList.remove('is-loading');
    }
  });
}

export function retireRepoPullRequestAllowMaintainerEdit() {
  unbindNative('maintainer-edit');
}

export function initRepoIssueReferenceRepositorySearch() {
  const $search = $('.issue_reference_repository_search').filter((_, el) => !el.closest(contributionRootSelector));
  const tracked = new Set(bucket(widgetInstances, 'reference-repository-search').map(([el]) => el));
  const $fresh = $search.filter((_, el) => !tracked.has(el));
  if (!$fresh.length) return;
  $fresh
    .dropdown({
      apiSettings: {
        url: `${appSubUrl}/repo/search?q={query}&limit=20`,
        onResponse(response) {
          const filteredResponse = {success: true, results: []};
          for (const repo of response.data) {
            filteredResponse.results.push({
              name: htmlEscape(repo.repository.full_name),
              value: repo.repository.full_name,
            });
          }
          return filteredResponse;
        },
        cache: false,
      },
      onChange(_value, _text, $choice) {
        const $form = $choice.closest('form');
        if (!$form.length) return;

        $form[0].setAttribute('action', `${appSubUrl}/${_text}/issues/new`);
      },
      fullTextSearch: true,
    });
  for (const el of $fresh.get()) {
    trackWidget('reference-repository-search', el, (widgetEl) => $(widgetEl).dropdown('destroy'));
  }
}

export function retireRepoIssueReferenceRepositorySearch() {
  destroyWidgets('reference-repository-search');
}

export function initRepoIssueWipTitle() {
  bindJQuery('wip-title', $('.title_wip_desc > a'), 'click', (e) => {
    e.preventDefault();

    const issueTitleEl = document.getElementById('issue_title');
    issueTitleEl.focus();
    const value = issueTitleEl.value.trim().toUpperCase();

    const wipPrefixes = $('.title_wip_desc').data('wip-prefixes');
    for (const prefix of wipPrefixes) {
      if (value.startsWith(prefix.toUpperCase())) {
        return;
      }
    }

    issueTitleEl.value = `${wipPrefixes[0]} ${issueTitleEl.value}`;
  });
}

export function retireRepoIssueWipTitle() {
  unbindJQuery('wip-title');
}

export async function updateIssuesMeta(url, action, issue_ids, id, run = undefined) {
  if (run && !run.current()) return;
  try {
    const response = await POST(url, {data: new URLSearchParams({action, issue_ids, id})});
    if (!response.ok) {
      throw new Error('Failed to update issues meta');
    }
  } catch (error) {
    if (!run || run.current()) console.error(error);
  }
}

export function initRepoIssueComments(run = createPageFeatureRun()) {
  if (!$('.repository.view.issue .timeline').length) return;
  if (issueCommentsRun?.current()) run = issueCommentsRun;
  issueCommentsRun = run;

  bindJQuery('issue-comments', $('.re-request-review'), 'click', async function (e) {
    if (!run.current()) return;
    e.preventDefault();
    const url = this.getAttribute('data-update-url');
    const issueId = this.getAttribute('data-issue-id');
    const id = this.getAttribute('data-id');
    const isChecked = this.classList.contains('checked');

    await updateIssuesMeta(url, isChecked ? 'detach' : 'attach', issueId, id, run);
    if (run.current()) window.location.reload();
  });

  installDocumentHandlersOnce('issue-comments-target', () => {
    document.addEventListener('click', (e) => {
      const urlTarget = document.querySelector(':target');
      if (!urlTarget) return;

      const urlTargetId = urlTarget.id;
      if (!urlTargetId) return;

      if (!/^(issue|pull)(comment)?-\d+$/.test(urlTargetId)) return;

      if (!e.target.closest(`#${urlTargetId}`)) {
        const scrollPosition = $(window).scrollTop();
        window.location.hash = '';
        $(window).scrollTop(scrollPosition);
        window.history.pushState(null, null, ' ');
      }
    });
  });
}

export function retireRepoIssueComments() {
  issueCommentsRun?.dispose();
  issueCommentsRun = null;
  unbindJQuery('issue-comments');
}

export async function handleReply($el, run = reviewRun ?? createPageFeatureRun()) {
  if (!run.current() || !$el[0]?.isConnected || $el[0].closest(contributionRootSelector)) return;
  hideElem($el);
  const $form = $el.closest('.comment-code-cloud').find('.comment-form');
  showElem($form);

  const $textarea = $form.find('textarea');
  let editor = getComboMarkdownEditor($textarea);
  if (!editor) {
    // FIXME: the initialization of the dropzone is not consistent.
    // When the page is loaded, the dropzone is initialized by initGlobalDropzone, but the editor is not initialized.
    // When the form is submitted and partially reload, none of them is initialized.
    const dropzone = $form.find('.dropzone')[0];
    if (dropzone && !dropzone.dropzone) {
      await initDropzone(dropzone, undefined, run);
      if (!run.current() || !$form[0].isConnected) return;
    }
  }
  editor = await initComboMarkdownEditor($form.find('.combo-markdown-editor'), {signal: run.signal});
  if (!run.current() || !$form[0].isConnected) {
    editor.destroy();
    return;
  }
  editor.focus();
  return editor;
}

export function initRepoPullRequestReview(run = createPageFeatureRun()) {
  if (reviewRun?.current()) return;
  retireRepoPullRequestReview();
  reviewRun = run;
  if (window.location.hash && window.location.hash.startsWith('#issuecomment-')) {
    // set scrollRestoration to 'manual' when there is a hash in url, so that the scroll position will not be remembered after refreshing
    if (window.history.scrollRestoration !== 'manual') {
      window.history.scrollRestoration = 'manual';
    }
    const commentDiv = document.querySelector(window.location.hash);
    if (commentDiv) {
      // get the name of the parent id
      const groupID = commentDiv.closest('div[id^="code-comments-"]')?.getAttribute('id');
      if (groupID && groupID.startsWith('code-comments-')) {
        const id = groupID.slice(14);
        const ancestorDiffBox = commentDiv.closest('.diff-file-box');
        // on pages like conversation, there is no diff header
        const diffHeader = ancestorDiffBox?.querySelector('.diff-file-header');

        // offset is for scrolling
        let offset = 30;
        if (diffHeader) {
          offset += $('.diff-detail-box').outerHeight() + $(diffHeader).outerHeight();
        }

        hideElem(`#show-outdated-${id}`);
        showElem(`#code-comments-${id}, #code-preview-${id}, #hide-outdated-${id}`);
        // if the comment box is folded, expand it
        if (ancestorDiffBox?.getAttribute('data-folded') === 'true') {
          setFileFolding(ancestorDiffBox, ancestorDiffBox.querySelector('.fold-file'), false);
        }

        window.scrollTo({
          top: $(commentDiv).offset().top - offset,
          behavior: 'instant',
        });
      }
    }
  } else if (window.history.scrollRestoration === 'manual') {
    // reset scrollRestoration to 'auto' if there is no hash in url and we set it to 'manual' before
    window.history.scrollRestoration = 'auto';
  }

  installDocumentHandlersOnce('pr-review-outdated', () => {
    $(document).on('click', '.show-outdated', function (e) {
      e.preventDefault();
      const id = this.getAttribute('data-comment');
      hideElem(this);
      showElem(`#code-comments-${id}`);
      showElem(`#code-preview-${id}`);
      showElem(`#hide-outdated-${id}`);
    });

    $(document).on('click', '.hide-outdated', function (e) {
      e.preventDefault();
      const id = this.getAttribute('data-comment');
      hideElem(this);
      hideElem(`#code-comments-${id}`);
      hideElem(`#code-preview-${id}`);
      showElem(`#show-outdated-${id}`);
    });

    $(document).on('click', 'button.comment-form-reply', async function (e) {
      e.preventDefault();
      const run = reviewRun;
      if (!run?.current()) return;
      try {
        await handleReply($(this), run);
      } catch (error) {
        if (run.current()) console.error(error);
      }
    });
  });

  const $reviewBox = $('.review-box-panel');
  let initialized;
  if ($reviewBox.length === 1 && !getComboMarkdownEditor($reviewBox.find('.combo-markdown-editor'))) {
    initialized = initComboMarkdownEditor($reviewBox.find('.combo-markdown-editor'), {signal: run.signal}).then((editor) => {
      if (!run.current() || !$reviewBox[0].isConnected) editor.destroy();
    });
  }

  // The following part is only for diff views
  if (!$('.repository.pull.diff').length) return initialized;

  const $reviewBtn = $reviewBox.parent().find('.js-btn-review');
  const $panel = $reviewBox.parent().find('.review-box-panel');
  const $closeBtn = $panel.find('.close');

  if ($reviewBtn.length && $panel.length) {
    const tippy = createTippy($reviewBtn[0], {
      content: $panel[0],
      placement: 'bottom',
      trigger: 'click',
      maxWidth: 'none',
      interactive: true,
      hideOnClick: true,
    });
    trackWidget('pr-review', $reviewBtn[0], () => tippy.destroy());

    bindJQuery('pr-review', $closeBtn, 'click', (e) => {
      e.preventDefault();
      tippy.hide();
    });
  }

  installDocumentHandlersOnce('pr-review-code-comment', () => {
    $(document).on('click', '.add-code-comment', async function (e) {
      if (e.target.classList.contains('btn-add-single')) return; // https://github.com/go-gitea/gitea/issues/4745
      e.preventDefault();
      const run = reviewRun;
      if (!run?.current() || this.closest(contributionRootSelector)) return;

      const isSplit = this.closest('.code-diff')?.classList.contains('code-diff-split');
      const side = this.getAttribute('data-side');
      const idx = this.getAttribute('data-idx');
      const path = this.closest('[data-path]')?.getAttribute('data-path');
      const tr = this.closest('tr');
      const lineType = tr.getAttribute('data-line-type');

      const ntr = tr.nextElementSibling;
      let $ntr = $(ntr);
      if (!ntr?.classList.contains('add-comment')) {
        $ntr = $(`
        <tr class="add-comment" data-line-type="${lineType}">
          ${isSplit ? `
            <td class="add-comment-left" colspan="4"></td>
            <td class="add-comment-right" colspan="4"></td>
          ` : `
            <td class="add-comment-left add-comment-right" colspan="5"></td>
          `}
        </tr>`);
        $(tr).after($ntr);
      }

      const $td = $ntr.find(`.add-comment-${side}`);
      const $commentCloud = $td.find('.comment-code-cloud');
      if (!$commentCloud.length && !$ntr.find('button[name="pending_review"]').length) {
        try {
          const response = await GET(this.closest('[data-new-comment-url]')?.getAttribute('data-new-comment-url'), {signal: run.signal});
          if (!run.current()) return;
          const html = await response.text();
          if (!run.current() || !$td[0]?.isConnected) return;
          $td.html(html);
          $td.find("input[name='line']").val(idx);
          $td.find("input[name='side']").val(side === 'left' ? 'previous' : 'proposed');
          $td.find("input[name='path']").val(path);

          await initDropzone($td.find('.dropzone')[0], undefined, run);
          if (!run.current() || !$td[0].isConnected) return;
          const editor = await initComboMarkdownEditor($td.find('.combo-markdown-editor'), {signal: run.signal});
          if (!run.current() || !$td[0].isConnected) {
            editor.destroy();
            return;
          }
          editor.focus();
        } catch (error) {
          if (run.current()) console.error(error);
        }
      }
    });
  });
  return initialized;
}

export function retireRepoPullRequestReview() {
  reviewRun?.dispose();
  reviewRun = null;
  unbindJQuery('pr-review');
  destroyWidgets('pr-review');
}

export function initRepoIssueReferenceIssue() {
  installDocumentHandlersOnce('repo-issue-reference-issue', () => {
  // Reference issue
    $(document).on('click', '.reference-issue', function (event) {
      const $this = $(this);
      const content = $(`#${$this.data('target')}`).text();
      const poster = $this.data('poster-username');
      const reference = toAbsoluteUrl($this.data('reference'));
      const $modal = $($this.data('modal'));
      $modal.find('textarea[name="content"]').val(`${content}\n\n_Originally posted by @${poster} in ${reference}_`);
      $modal.modal('show');

      event.preventDefault();
    });
  });
}

export function findWipPrefix(string, wipPrefixes) {
  return wipPrefixes.find((prefix) => string.toUpperCase().startsWith(prefix.toUpperCase()));
}

export function initRepoIssueWipToggle(run = createPageFeatureRun()) {
  if (bucket(jqueryBindings, 'wip-toggle').length) return;
  wipToggleRun = run;
  // Toggle WIP
  bindJQuery('wip-toggle', $('.toggle-wip a, .toggle-wip button'), 'click', async (e) => {
    e.preventDefault();
    if (!run.current()) return;
    const toggleWip = e.currentTarget.closest('.toggle-wip');
    const title = toggleWip.getAttribute('data-title');
    const wipPrefixes = JSON.parse(toggleWip.getAttribute('data-wip-prefixes'));
    const updateUrl = toggleWip.getAttribute('data-update-url');
    const prefix = findWipPrefix(title, wipPrefixes);

    try {
      const params = new URLSearchParams();
      params.append('title', prefix !== undefined ? title.slice(prefix.length).trim() : `${wipPrefixes[0].trim()} ${title}`);

      const response = await POST(updateUrl, {data: params});
      if (!run.current()) return;
      if (!response.ok) {
        throw new Error('Failed to toggle WIP status');
      }
      window.location.reload();
    } catch (error) {
      if (run.current()) console.error(error);
    }
  });
}

export function retireRepoIssueWipToggle() {
  wipToggleRun?.dispose();
  wipToggleRun = null;
  unbindJQuery('wip-toggle');
}

export function initRepoIssueTitleEdit() {
  const issueTitleDisplay = document.querySelector('#issue-title-display');
  const issueTitleEditor = document.querySelector('#issue-title-editor');
  if (!issueTitleEditor || issueTitleEditor.closest(contributionRootSelector)) return;

  const issueTitleInput = issueTitleEditor.querySelector('input');
  const oldTitle = issueTitleInput.getAttribute('data-old-title');
  const normalModeElements = [issueTitleDisplay, '#pull-desc-display', '#agit-label', '#editable-label'];
  bindNative('title-edit', issueTitleDisplay.querySelector('#issue-title-edit-show'), 'click', () => {
    for (const element of normalModeElements) {
      hideElem(element);
    }
    showElem(issueTitleEditor);
    showElem('#pull-desc-editor');
    if (!issueTitleInput.value.trim()) {
      issueTitleInput.value = oldTitle;
    }
    issueTitleInput.focus();
  });
  bindNative('title-edit', issueTitleEditor.querySelector('.ui.cancel.button'), 'click', () => {
    hideElem(issueTitleEditor);
    hideElem('#pull-desc-editor');
    for (const element of normalModeElements) {
      showElem(element);
    }
  });

  const pullDescEditor = document.querySelector('#pull-desc-editor'); // it may not exist for a merged PR
  const prTargetUpdateUrl = pullDescEditor?.getAttribute('data-target-update-url');

  const editSaveButton = issueTitleEditor.querySelector('.ui.primary.button');
  const saveAndRefresh = async () => {
    const newTitle = issueTitleInput.value.trim();
    try {
      if (newTitle && newTitle !== oldTitle) {
        const resp = await POST(editSaveButton.getAttribute('data-update-url'), {data: new URLSearchParams({title: newTitle})});
        if (!resp.ok) {
          throw new Error(`Failed to update issue title: ${resp.statusText}`);
        }
      }
      if (prTargetUpdateUrl) {
        const newTargetBranch = document.querySelector('#pull-target-branch').getAttribute('data-branch');
        const oldTargetBranch = document.querySelector('#branch_target').textContent;
        if (newTargetBranch !== oldTargetBranch) {
          const resp = await POST(prTargetUpdateUrl, {data: new URLSearchParams({target_branch: newTargetBranch})});
          if (!resp.ok) {
            throw new Error(`Failed to update PR target branch: ${resp.statusText}`);
          }
        }
      }
      window.location.reload();
    } catch (error) {
      console.error(error);
      showErrorToast(error.message);
    }
  };
  bindNative('title-edit', editSaveButton, 'click', saveAndRefresh);
  bindNative('title-edit', issueTitleEditor.querySelector('input'), 'ce-quick-submit', saveAndRefresh);
}

export function retireRepoIssueTitleEdit() {
  unbindNative('title-edit');
}

export function initRepoIssueBranchSelect() {
  const branchSelect = document.querySelector('#branch-select');
  if (!branchSelect || branchSelect.closest(contributionRootSelector)) return;
  bindNative('branch-select', branchSelect, 'click', (e) => {
    const el = e.target.closest('.item[data-branch]');
    if (!el) return;
    const pullTargetBranch = document.querySelector('#pull-target-branch');
    const baseName = pullTargetBranch.getAttribute('data-basename');
    const branchNameNew = el.getAttribute('data-branch');
    const branchNameOld = pullTargetBranch.getAttribute('data-branch');
    pullTargetBranch.textContent = pullTargetBranch.textContent.replace(`${baseName}:${branchNameOld}`, `${baseName}:${branchNameNew}`);
    pullTargetBranch.setAttribute('data-branch', branchNameNew);
  });
}

export function retireRepoIssueBranchSelect() {
  unbindNative('branch-select');
}

export function initRepoIssueAssignMe(run = createPageFeatureRun()) {
  // Assign to me button
  const assignMe = document.querySelector('.ui.assignees.list .item.no-select .select-assign-me');
  if (!assignMe || assignMe.closest(contributionRootSelector)) return;
  if (assignMeRun?.current()) run = assignMeRun;
  assignMeRun = run;
  bindNative('assign-me', assignMe, 'click', (e) => {
    if (!run.current()) return;
    e.preventDefault();
    const selectMe = e.target;
    const noSelect = selectMe.parentElement;
    const selectorList = document.querySelector('.ui.select-assignees .menu');

    if (selectMe.getAttribute('data-action') === 'update') {
      (async () => {
        await updateIssuesMeta(
          selectMe.getAttribute('data-update-url'),
          selectMe.getAttribute('data-action'),
          selectMe.getAttribute('data-issue-id'),
          selectMe.getAttribute('data-id'),
          run,
        );
        if (run.current()) reloadConfirmDraftComment();
      })();
    } else {
      for (const item of selectorList.querySelectorAll('.item')) {
        if (item.getAttribute('data-id') === selectMe.getAttribute('data-id')) {
          item.classList.add('checked');
          item.querySelector('.octicon-check').classList.remove('tw-invisible');
        }
      }
      document.querySelector(selectMe.getAttribute('data-id-selector')).classList.remove('tw-hidden');
      noSelect.classList.add('tw-hidden');
      document.querySelector(selectorList.getAttribute('data-id')).value = selectMe.getAttribute('data-id');
      return false;
    }
  });
}

export function retireRepoIssueAssignMe() {
  assignMeRun?.dispose();
  assignMeRun = null;
  unbindNative('assign-me');
}

export function initSingleCommentEditor($commentForm, run = createPageFeatureRun()) {
  // pages:
  // * normal new issue/pr page, no status-button
  // * issue/pr view page, with comment form, has status-button
  commentEditorsRun = run;
  const opts = {signal: run.signal};
  const statusButton = document.getElementById('status-button');
  if (statusButton) {
    opts.onContentChanged = (editor) => {
      if (!run.current() || !statusButton.isConnected) return;
      const newText = statusButton.getAttribute(editor.value().trim() ? 'data-status-and-comment' : 'data-status');
      statusButton.querySelector('span').textContent = newText;
    };
  }
  const $editor = $commentForm.find('.combo-markdown-editor');
  return initComboMarkdownEditor($editor, opts).then((editor) => {
    if (!run.current() || !$commentForm[0].isConnected) editor.destroy();
  });
}

export function initIssueTemplateCommentEditors($commentForm, run = createPageFeatureRun()) {
  // pages:
  // * new issue with issue template
  const $comboFields = $commentForm.find('.combo-editor-dropzone');
  commentEditorsRun = run;

  const initCombo = async ($combo) => {
    const $dropzoneContainer = $combo.find('.form-field-dropzone');
    const $formField = $combo.find('.form-field-real');
    const $markdownEditor = $combo.find('.combo-markdown-editor');

    const editor = await initComboMarkdownEditor($markdownEditor, {
      signal: run.signal,
      onContentChanged: (editor) => {
        if (run.current() && $formField[0].isConnected) $formField.val(editor.value());
      },
    });
    if (!run.current() || !$combo[0].isConnected) {
      editor.destroy();
      return;
    }

    $formField.off('focus.repoIssueTemplateEditors').on('focus.repoIssueTemplateEditors', async () => {
      if (!run.current()) return;
      // deactivate all markdown editors
      showElem($commentForm.find('.combo-editor-dropzone .form-field-real'));
      hideElem($commentForm.find('.combo-editor-dropzone .combo-markdown-editor'));
      hideElem($commentForm.find('.combo-editor-dropzone .form-field-dropzone'));

      // activate this markdown editor
      hideElem($formField);
      showElem($markdownEditor);
      showElem($dropzoneContainer);

      try {
        await editor.switchToUserPreference();
        if (run.current() && $combo[0].isConnected) editor.focus();
      } catch (error) {
        if (run.current()) console.error(error);
      }
    });
  };

  return Promise.all(Array.from($comboFields, (el) => initCombo($(el))));
}

export function retireIssueTemplateCommentEditors() {
  commentEditorsRun?.dispose();
  commentEditorsRun = null;
  $('.combo-editor-dropzone .form-field-real').off('focus.repoIssueTemplateEditors');
}

// This function used to show and hide archived label on issue/pr
//  page in the sidebar where we select the labels
//  If we have any archived label tagged to issue and pr. We will show that
//  archived label with checked classed otherwise we will hide it
//  with the help of this function.
//  This function runs globally.
export function initArchivedLabelHandler() {
  if (!document.querySelector('.archived-label-hint')) return;
  for (const label of document.querySelectorAll('[data-is-archived]')) {
    if (label.closest(contributionRootSelector)) continue;
    toggleElem(label, label.classList.contains('checked'));
  }
}
