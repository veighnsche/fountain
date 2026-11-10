import {contributionRootSelector, createPageFeatureRun} from '../native-lifecycle.js';
import {createNativeVueMount} from '../native-vue.js';

const nativeMount = createNativeVueMount();

export function retireRepoContributors() {
  nativeMount.dispose();
}

export async function initRepoContributors(run = createPageFeatureRun()) {
  const el = document.getElementById('repo-contributors-chart');
  if (!el?.closest('.page-content') || el.closest(contributionRootSelector)) return;
  const props = {
    repoLink: el.getAttribute('data-repo-link'),
    repoDefaultBranchName: el.getAttribute('data-repo-default-branch-name'),
    locale: {
      filterLabel: el.getAttribute('data-locale-filter-label'),
      contributionType: {
        commits: el.getAttribute('data-locale-contribution-type-commits'),
        additions: el.getAttribute('data-locale-contribution-type-additions'),
        deletions: el.getAttribute('data-locale-contribution-type-deletions'),
      },
      loadingTitle: el.getAttribute('data-locale-loading-title'),
      loadingTitleFailed: el.getAttribute('data-locale-loading-title-failed'),
      loadingInfo: el.getAttribute('data-locale-loading-info'),
    },
  };
  const failedMessage = el.getAttribute('data-locale-component-failed-to-load');
  return nativeMount.mount([{element: el, props, current: () => Boolean(el.closest('.page-content'))}], run,
    () => import(/* webpackChunkName: "contributors-graph" */'../components/RepoContributors.vue'), {
      onError(error) {
        console.error('RepoContributors failed to load', error);
        el.textContent = failedMessage;
      },
    });
}
