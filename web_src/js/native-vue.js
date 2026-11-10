import {createApp} from 'vue';
import {contributionRootSelector} from './native-lifecycle.js';

function currentNativeRoot({element, id, current}) {
  return element.isConnected && (!id || document.getElementById(id) === element) &&
    !element.closest(contributionRootSelector) && (!current || current());
}

export function createNativeVueMount() {
  let active = null;
  const dispose = () => active?.run.dispose();
  const matches = (elements) => Boolean(active?.run.current() && elements.length === active.records.length &&
    elements.every((element, index) => active.records[index].element === element));
  return {
    dispose,
    matches,
    async mount(records, run, load, {onError, onMounted} = {}) {
      if (!records.length || !run.current()) return;
      if (matches(records.map(({element}) => element))) return;
      dispose();
      const mount = {records, run, apps: []};
      active = mount;
      const current = () => active === mount && run.current();
      const roots = records.map((record) => ({...record, id: record.element.id}));
      const release = run.own(() => {
        if (active === mount) active = null;
        let firstError;
        for (const owned of mount.apps.splice(0).reverse()) {
          try {
            if (owned.ready) owned.app.unmount();
          } catch (error) {
            firstError ??= error;
          }
        }
        if (firstError) throw firstError;
      });
      try {
        if (!current()) return;
        const {default: component} = await load();
        if (!current()) return;
        for (const root of roots) {
          if (!current()) return;
          if (!currentNativeRoot(root)) continue;
          const app = createApp(component, root.props);
          const owned = {app, ready: false};
          mount.apps.push(owned);
          app.mount(root.element);
          owned.ready = true;
          if (!current()) {
            app.unmount();
            return;
          }
          if (current() && currentNativeRoot(root)) onMounted?.(root.element);
        }
        if (!mount.apps.length) run.dispose();
      } catch (error) {
        const wasCurrent = current();
        try {
          release();
          if (!wasCurrent || !run.current() || active !== null || !roots.some(currentNativeRoot)) return;
          if (onError) onError(error);
          else throw error;
        } finally {
          run.dispose();
        }
      } finally {
        if (!run.current()) release();
      }
    },
  };
}
