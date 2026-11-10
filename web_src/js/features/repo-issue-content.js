import $ from 'jquery';
import {svg} from '../svg.js';
import {showErrorToast} from '../modules/toast.js';
import {GET, POST} from '../modules/fetch.js';
import {showElem} from '../utils/dom.js';
import {contributionRootSelector, createPageFeatureRun} from '../native-lifecycle.js';
import {createFeatureRun} from '../native-feature-run.js';

const {appSubUrl} = window.config;
let historyRun = null;
const historyDialogs = new Set();
const historyMenus = new WeakMap();

function showContentHistoryDetail(context, commentId, historyId, itemTitleHtml) {
  const {run, issueBaseUrl, i18n} = context;
  if (!run.current() || historyDialogs.size) return;
  const dialogRun = createFeatureRun(run.current);

  const $dialog = $(`
<div class="ui modal content-history-detail-dialog">
  ${svg('octicon-x', 16, 'close icon inside')}
  <div class="header tw-flex tw-items-center tw-justify-between">
    <div>${itemTitleHtml}</div>
    <div class="ui dropdown dialog-header-options tw-mr-8 tw-hidden">
      ${i18n.textOptions}
      ${svg('octicon-triangle-down', 14, 'dropdown icon')}
      <div class="menu">
        <div class="item red text" data-option-item="delete">${i18n.textDeleteFromHistory}</div>
      </div>
    </div>
  </div>
  <div class="comment-diff-data is-loading"></div>
</div>`);
  historyDialogs.add($dialog[0]);
  const release = run.own(() => dialogRun.dispose());
  dialogRun.own(() => {
    historyDialogs.delete($dialog[0]);
    try {
      $dialog.find('.dialog-header-options').dropdown('destroy');
    } finally {
      try {
        $dialog.modal('hide').modal('destroy');
      } finally {
        $dialog.remove();
      }
    }
  });
  $dialog.appendTo($('body'));
  $dialog.find('.dialog-header-options').dropdown({
    showOnFocus: false,
    allowReselection: true,
    async onChange(_value, _text, $item) {
      if (!dialogRun.current()) return;
      const optionItem = $item.data('option-item');
      if (optionItem === 'delete') {
        if (window.confirm(i18n.textDeleteFromHistoryConfirm)) {
          try {
            const params = new URLSearchParams();
            params.append('comment_id', commentId);
            params.append('history_id', historyId);

            const response = await POST(`${issueBaseUrl}/content-history/soft-delete?${params.toString()}`);
            const resp = await response.json();
            if (!dialogRun.current()) return;

            if (resp.ok) {
              $dialog.modal('hide');
            } else {
              showErrorToast(resp.message);
            }
          } catch (error) {
            if (dialogRun.current()) {
              console.error('Error:', error);
              showErrorToast('An error occurred while deleting the history.');
            }
          }
        }
      } else { // required by eslint
        showErrorToast(`unknown option item: ${optionItem}`);
      }
    },
    onHide() {
      $(this).dropdown('clear', true);
    },
  });
  $dialog.modal({
    async onShow() {
      if (!dialogRun.current()) return;
      try {
        const params = new URLSearchParams();
        params.append('comment_id', commentId);
        params.append('history_id', historyId);

        const url = `${issueBaseUrl}/content-history/detail?${params.toString()}`;
        const response = await GET(url, {signal: dialogRun.signal});
        const resp = await response.json();
        if (!dialogRun.current()) return;

        const commentDiffData = $dialog.find('.comment-diff-data')[0];
        commentDiffData?.classList.remove('is-loading');
        commentDiffData.innerHTML = resp.diffHtml;
        // there is only one option "item[data-option-item=delete]", so the dropdown can be entirely shown/hidden.
        if (resp.canSoftDelete) {
          showElem($dialog.find('.dialog-header-options'));
        }
      } catch (error) {
        if (dialogRun.current()) console.error('Error:', error);
      }
    },
    onHidden() {
      release();
    },
  }).modal('show');
}

function showContentHistoryMenu(context, $item, commentId) {
  const {run, issueBaseUrl, i18n} = context;
  const $headerLeft = $item.find('.comment-header-left');
  const menuHtml = `
  <div class="ui dropdown interact-fg content-history-menu" data-comment-id="${commentId}">
    &bull; ${i18n.textEdited}${svg('octicon-triangle-down', 14, 'dropdown icon')}
    <div class="menu">
    </div>
  </div>`;

  for (const menu of $headerLeft.find('.content-history-menu')) historyMenus.get(menu)?.();
  const $menu = $(menuHtml);
  $headerLeft.append($menu);
  const release = run.own(() => {
    historyMenus.delete($menu[0]);
    try {
      $menu.off('.contentHistory').dropdown('destroy');
    } finally {
      $menu.remove();
    }
  });
  historyMenus.set($menu[0], release);

  // Should be run before fomantic's dropdown component.
  $menu.on('click.contentHistory', function (ev) {
    if (!run.current()) return;
    if (ev.target !== this || this.classList.contains('active')) {
      return;
    }
    this.classList.add('is-loading');
  });

  $menu.dropdown({
    action: 'hide',
    apiSettings: {
      cache: false,
      url: `${issueBaseUrl}/content-history/list?comment_id=${commentId}`,
    },
    saveRemoteData: false,
    onHide() {
      $(this).dropdown('change values', null);
    },
    onChange(value, itemHtml, $item) {
      if (!run.current()) return;
      if (value && !$item.find('[data-history-is-deleted=1]').length) {
        showContentHistoryDetail(context, commentId, value, itemHtml);
      }
    },
    onShow() {
      this.classList.remove('is-loading');
    },
  });
}

export async function initRepoIssueContentHistory(run = historyRun?.current() ? historyRun : createPageFeatureRun()) {
  const issueIndex = $('#issueIndex').val();
  if (!issueIndex) return;

  const $itemIssue = $('.repository.issue .timeline-item.comment.first').filter((_, element) => !element.closest(contributionRootSelector)); // issue(PR) main content
  const $comments = $('.repository.issue .comment-list .comment'); // includes: issue(PR) comments, review comments, code comments
  if (!$itemIssue.length && !$comments.length) return;

  const repoLink = $('#repolink').val();
  const issueBaseUrl = `${appSubUrl}/${repoLink}/issues/${issueIndex}`;
  if (historyRun && historyRun !== run) historyRun.dispose();
  historyRun = run;
  const comments = new Map(Array.from($comments).filter((element) => !element.closest(contributionRootSelector)).map((element) => [element.id, element]));

  try {
    const response = await GET(`${issueBaseUrl}/content-history/overview`, {signal: run.signal});
    // The page retired while the overview loaded: its menus must not be
    // built into the outgoing (or a fresh) document.
    if (!run.current()) return;
    const resp = await response.json();
    if (!run.current()) return;
    const context = {run, issueBaseUrl, i18n: resp.i18n};

    if (resp.editedHistoryCountMap[0] && $itemIssue[0]?.isConnected && !$itemIssue[0].closest(contributionRootSelector)) {
      showContentHistoryMenu(context, $itemIssue, '0');
    }
    for (const [commentId, _editedCount] of Object.entries(resp.editedHistoryCountMap)) {
      if (commentId === '0') continue;
      const element = comments.get(`issuecomment-${commentId}`);
      if (!element?.isConnected) continue;
      showContentHistoryMenu(context, $(element), commentId);
    }
  } catch (error) {
    if (run.current()) console.error('Error:', error);
  }
}

// Menus and dialogs created here are page-owned. Retire destroys their
// dropdown widgets and removes them so nothing history-related is pinned
// (or left rendering) after the native content is swapped out.
export function retireRepoIssueContentHistory() {
  const run = historyRun;
  historyRun = null;
  run?.dispose();
}
