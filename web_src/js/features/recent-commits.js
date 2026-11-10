import {contributionRootSelector, createPageFeatureRun} from '../native-lifecycle.js';
import {createNativeVueMount} from '../native-vue.js';

const nativeMount = createNativeVueMount();

export function retireRepoRecentCommits() {
  nativeMount.dispose();
}

export async function initRepoRecentCommits(run = createPageFeatureRun()) {
  const el = document.getElementById('repo-recent-commits-chart');
  if (!el?.closest('.page-content') || el.closest(contributionRootSelector)) return;
  const props = {
    repoLink: window.config.pageData.repoLink || '',
    locale: {
      loadingTitle: el.getAttribute('data-locale-loading-title'),
      loadingTitleFailed: el.getAttribute('data-locale-loading-title-failed'),
      recentCommitsTitle: el.getAttribute('data-locale-recent-commits-title'),
      loadingInfo: el.getAttribute('data-locale-loading-info'),
    },
  };
  const failedMessage = el.getAttribute('data-locale-component-failed-to-load');
  return nativeMount.mount([{element: el, props, current: () => Boolean(el.closest('.page-content'))}], run,
    () => import(/* webpackChunkName: "recent-commits-graph" */'../components/RepoRecentCommits.vue'), {
      onError(error) {
        console.error('RepoRecentCommits failed to load', error);
        el.textContent = failedMessage;
      },
    });
}
