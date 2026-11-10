export function createFeatureRun(isCurrent = () => true) {
  const controller = new AbortController();
  const cleanups = [];
  let disposed = false;
  const current = () => !disposed && isCurrent();
  return {
    signal: controller.signal,
    current,
    own(cleanup) {
      let released = false;
      const release = () => {
        if (released) return;
        released = true;
        const index = cleanups.indexOf(release);
        if (index !== -1) cleanups.splice(index, 1);
        cleanup();
      };
      if (current()) cleanups.push(release);
      else release();
      return release;
    },
    dispose() {
      if (disposed) return;
      disposed = true;
      controller.abort();
      let firstError;
      for (const release of cleanups.splice(0).reverse()) {
        try {
          release();
        } catch (error) {
          firstError ??= error;
        }
      }
      if (firstError) throw firstError;
    },
  };
}
