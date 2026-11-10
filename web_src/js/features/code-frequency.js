import {contributionRootSelector, createPageFeatureRun} from '../native-lifecycle.js';
import {createNativeVueMount} from '../native-vue.js';

const nativeMount = createNativeVueMount();

export function retireRepoCodeFrequency() {
  nativeMount.dispose();
}

export async function initRepoCodeFrequency(run = createPageFeatureRun()) {
  const el = document.getElementById('repo-code-frequency-chart');
  if (!el?.closest('.page-content') || el.closest(contributionRootSelector)) return;
  const props = {
    repoLink: window.config.pageData.repoLink || '',
    locale: {
      loadingTitle: el.getAttribute('data-locale-loading-title'),
      loadingTitleFailed: el.getAttribute('data-locale-loading-title-failed'),
      loadingInfo: el.getAttribute('data-locale-loading-info'),
      codeFrequencyTitle: el.getAttribute('data-locale-code-frequency-title'),
    },
  };
  const failedMessage = el.getAttribute('data-locale-component-failed-to-load');
  return nativeMount.mount([{element: el, props, current: () => Boolean(el.closest('.page-content'))}], run,
    () => import(/* webpackChunkName: "code-frequency-graph" */'../components/RepoCodeFrequency.vue'), {
      onError(error) {
        console.error('RepoCodeFrequency failed to load', error);
        el.textContent = failedMessage;
      },
    });
}
