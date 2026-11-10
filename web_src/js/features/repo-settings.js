import $ from 'jquery';
import {minimatch} from 'minimatch';
import {onInputDebounce, toggleElem} from '../utils/dom.js';
import {POST} from '../modules/fetch.js';
import {createCodemirror} from './codemirror.ts';
import {contributionRootSelector, createPageFeatureRun} from '../native-lifecycle.js';

const {appSubUrl} = window.config;
let teamSearch = null;
let gitHook = null;
let branchController = null;
let branchDebounce = null;

export function retireRepoSettingSearchTeamBox() {
  if (teamSearch) $(teamSearch).search('destroy');
  teamSearch = null;
}

export function retireRepoSettingGitHook() {
  const current = gitHook;
  gitHook = null;
  current?.run.dispose();
}

export function retireRepoSettingBranches() {
  branchController?.abort();
  branchController = null;
  branchDebounce?.cancel();
  branchDebounce = null;
}

// Access-mode dropdowns configured for the current page; retire destroys
// exactly those instances.
const collaborationDropdowns = new Set();
const collaborationTimers = new Set();
let collaborationController = null;

export function initRepoSettingsCollaboration() {
  collaborationController ??= new AbortController();
  const {signal} = collaborationController;
  // Change collaborator access mode
  $('.page-content.repository .ui.dropdown.access-mode').each((_, el) => {
    if (collaborationDropdowns.has(el) || el.closest(contributionRootSelector)) return;
    const $dropdown = $(el);
    const $text = $dropdown.find('> .text');
    $dropdown.dropdown({
      async action(_text, value) {
        if (signal.aborted) return;
        const lastValue = el.getAttribute('data-last-value');
        try {
          el.setAttribute('data-last-value', value);
          $dropdown.dropdown('hide');
          const data = new FormData();
          data.append('uid', el.getAttribute('data-uid'));
          data.append('mode', value);
          await POST(el.getAttribute('data-url'), {data, signal});
        } catch {
          if (signal.aborted) return;
          $text.text('(error)'); // prevent from misleading users when error occurs
          el.setAttribute('data-last-value', lastValue);
        }
      },
      onChange(_value, text, _$choice) {
        if (signal.aborted) return;
        $text.text(text); // update the text when using keyboard navigating
      },
      onHide() {
        // set to the really selected value, defer to next tick to make sure `action` has finished its work because the calling order might be onHide -> action
        if (signal.aborted) return;
        const timer = setTimeout(() => {
          collaborationTimers.delete(timer);
          if (signal.aborted) return;
          const $item = $dropdown.dropdown('get item', el.getAttribute('data-last-value'));
          if ($item) {
            $dropdown.dropdown('set selected', el.getAttribute('data-last-value'));
          } else {
            $text.text('(none)'); // prevent from misleading users when the access mode is undefined
          }
        }, 0);
        collaborationTimers.add(timer);
      },
    });
    collaborationDropdowns.add(el);
  });
}

export function retireRepoSettingsCollaboration() {
  collaborationController?.abort();
  collaborationController = null;
  for (const timer of collaborationTimers) clearTimeout(timer);
  collaborationTimers.clear();
  for (const el of collaborationDropdowns) {
    $(el).dropdown('destroy');
    collaborationDropdowns.delete(el);
  }
}

export function initRepoSettingSearchTeamBox() {
  const searchTeamBox = document.getElementById('search-team-box');
  if (!searchTeamBox || searchTeamBox.closest(contributionRootSelector)) return;
  if (teamSearch === searchTeamBox) return;
  retireRepoSettingSearchTeamBox();
  teamSearch = searchTeamBox;

  $(searchTeamBox).search({
    minCharacters: 2,
    apiSettings: {
      url: `${appSubUrl}/org/${searchTeamBox.getAttribute('data-org-name')}/teams/-/search?q={query}`,
      onResponse(response) {
        const items = [];
        for (const item of response.data) {
          items.push({
            title: item.name,
            description: `${item.permission} access`, // TODO: translate this string
          });
        }
        return {results: items};
      },
    },
    searchFields: ['name', 'description'],
    showNoResults: false,
  });
}

export async function initRepoSettingGitHook(run = createPageFeatureRun()) {
  retireRepoSettingGitHook();
  const root = document.querySelector('.edit.githook');
  if (!root || root.closest(contributionRootSelector)) return;
  const filename = root.querySelector('.hook-filename').textContent;
  const element = root.querySelector('#content');
  if (!element || element.closest(contributionRootSelector)) return;
  const current = {element, run};
  gitHook = current;
  try {
    const editor = await createCodemirror(element, filename, {language: 'shell'}, run);
    const release = run.own(() => editor.view.destroy());
    if (!run.current() || !element.isConnected) release();
  } catch (error) {
    run.dispose();
    if (error?.name !== 'AbortError') throw error;
  }
}

export function initRepoSettingBranches() {
  retireRepoSettingBranches();
  const root = document.querySelector('.repository.settings.branches');
  if (!root || root.closest(contributionRootSelector)) return;
  branchController = new AbortController();
  const {signal} = branchController;

  for (const el of root.getElementsByClassName('toggle-target-enabled')) {
    if (el.closest(contributionRootSelector)) continue;
    el.addEventListener('change', function () {
      const target = root.querySelector(this.getAttribute('data-target'));
      target?.classList.toggle('disabled', !this.checked);
    }, {signal});
  }

  for (const el of root.getElementsByClassName('toggle-target-disabled')) {
    if (el.closest(contributionRootSelector)) continue;
    el.addEventListener('change', function () {
      const target = root.querySelector(this.getAttribute('data-target'));
      if (this.checked) target?.classList.add('disabled'); // only disable, do not auto enable
    }, {signal});
  }

  root.querySelector('#dismiss_stale_approvals')?.addEventListener('change', function () {
    root.querySelector('#ignore_stale_approvals_box')?.classList.toggle('disabled', this.checked);
  }, {signal});

  // show the `Matched` mark for the status checks that match the pattern
  const markMatchedStatusChecks = () => {
    if (signal.aborted) return;
    const patterns = (root.querySelector('#status_check_contexts').value || '').split(/[\r\n]+/);
    const validPatterns = patterns.map((item) => item.trim()).filter(Boolean);
    const marks = root.getElementsByClassName('status-check-matched-mark');

    for (const el of marks) {
      if (el.closest(contributionRootSelector)) continue;
      let matched = false;
      const statusCheck = el.getAttribute('data-status-check');
      for (const pattern of validPatterns) {
        if (minimatch(statusCheck, pattern)) {
          matched = true;
          break;
        }
      }
      toggleElem(el, matched);
    }
  };
  markMatchedStatusChecks();
  branchDebounce = onInputDebounce(markMatchedStatusChecks);
  root.querySelector('#status_check_contexts').addEventListener('input', branchDebounce, {signal});
}
