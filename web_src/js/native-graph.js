import {GET} from './modules/fetch.js';

function waitForGraph(signal) {
  return new Promise((resolve, reject) => {
    let timer;
    const abort = () => {
      clearTimeout(timer);
      signal.removeEventListener('abort', abort);
      reject(new DOMException('Graph request retired', 'AbortError'));
    };
    timer = setTimeout(() => {
      signal.removeEventListener('abort', abort);
      resolve();
    }, 1000);
    if (signal.aborted) abort();
    else signal.addEventListener('abort', abort, {once: true});
  });
}

export function createGraphRequest() {
  let active;
  let disposed = false;
  return {
    dispose() {
      disposed = true;
      active?.abort();
      active = undefined;
    },
    async run(url, {apply, error, loading}) {
      if (disposed) return;
      active?.abort();
      const controller = new AbortController();
      active = controller;
      const current = () => !disposed && active === controller && !controller.signal.aborted;
      loading(true);
      try {
        let response;
        do {
          if (!current()) return;
          response = await GET(url, {signal: controller.signal});
          if (!current()) return;
          if (response.status === 202) await waitForGraph(controller.signal);
        } while (response.status === 202 && current());
        if (!current()) return;
        if (response.ok) {
          const data = await response.json();
          if (current()) apply(data);
        } else {
          error(response.statusText);
        }
      } catch (err) {
        if (current()) error(err.message);
      } finally {
        if (current()) loading(false);
      }
    },
  };
}
