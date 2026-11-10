import {translateMonth, translateDay} from '../utils.js';
import {contributionRootSelector, createPageFeatureRun} from '../native-lifecycle.js';
import {createNativeVueMount} from '../native-vue.js';

const nativeMount = createNativeVueMount();

export function retireHeatmap() {
  nativeMount.dispose();
}

export async function initHeatmap(run = createPageFeatureRun()) {
  const el = document.getElementById('user-heatmap');
  if (!el || el.closest(contributionRootSelector) || nativeMount.matches([el])) return;
  try {
    const heatmap = {};
    for (const {contributions, timestamp} of JSON.parse(el.getAttribute('data-heatmap-data'))) {
      // Convert to user timezone and sum contributions by date
      const dateStr = new Date(timestamp * 1000).toDateString();
      heatmap[dateStr] = (heatmap[dateStr] || 0) + contributions;
    }
    const values = Object.keys(heatmap).map((value) => ({date: new Date(value), count: heatmap[value]}));
    const locale = {
      months: new Array(12).fill().map((_, idx) => translateMonth(idx)),
      days: new Array(7).fill().map((_, idx) => translateDay(idx)),
      contributions_in_the_last_12_months: el.getAttribute('data-locale-total-contributions'),
      contributions_zero: el.getAttribute('data-locale-contributions-zero'),
      contributions_format: el.getAttribute('data-locale-contributions-format'),
      contributions_one: el.getAttribute('data-locale-contributions-one'),
      contributions_few: el.getAttribute('data-locale-contributions-few'),
      more: el.getAttribute('data-locale-more'),
      less: el.getAttribute('data-locale-less'),
    };
    return nativeMount.mount([{element: el, props: {values, locale}}], run,
      () => import(/* webpackChunkName: "activity-heatmap" */'../components/ActivityHeatmap.vue'), {
        onMounted: () => el.classList.remove('is-loading'),
        onError(error) {
          console.error('Heatmap failed to load', error);
          el.textContent = 'Heatmap failed to load';
        },
      });
  } catch (error) {
    if (!run.current() || !el.isConnected) return;
    console.error('Heatmap failed to load', error);
    el.textContent = 'Heatmap failed to load';
    run.dispose();
  }
}
