let lifecycle;
let reportError;

beforeEach(async () => {
  vi.resetModules();
  lifecycle = await import('./native-lifecycle.js');
  reportError = vi.spyOn(console, 'error').mockImplementation(() => {});
});

afterEach(() => {
  lifecycle.retirePageFeatures();
  vi.restoreAllMocks();
});

test('the registry records a run before invoking a reentrant initializer', () => {
  let initialized = 0;
  lifecycle.registerPageFeature('reentrant', {
    init: () => {
      initialized++;
      if (initialized === 1) lifecycle.initPageFeatures();
    },
  });

  lifecycle.initPageFeatures();
  expect(initialized).toBe(1);
});

test('retirement during initialization cleans the already recorded partial run', () => {
  const cleanup = vi.fn();
  const retire = vi.fn();
  let run;
  lifecycle.registerPageFeature('retire-during-init', {
    init: (active) => {
      run = active;
      run.own(cleanup);
      lifecycle.retirePageFeatures();
    },
    retire,
  });

  lifecycle.initPageFeatures();
  expect(run.current()).toBe(false);
  expect(run.signal.aborted).toBe(true);
  expect(cleanup).toHaveBeenCalledExactlyOnceWith();
  expect(retire).toHaveBeenCalledExactlyOnceWith();
});

test('page retirement aborts and disposes runs created by delegated controls without a registered initializer', () => {
  const run = lifecycle.createPageFeatureRun();
  const cleanup = vi.fn();
  run.own(cleanup);
  lifecycle.retirePageFeatures();
  lifecycle.retirePageFeatures();
  expect(run.current()).toBe(false);
  expect(run.signal.aborted).toBe(true);
  expect(cleanup).toHaveBeenCalledTimes(1);
});

test('a pending initializer does not delay other features or double-initialize on reentry', async () => {
  const pending = Promise.withResolvers();
  const init = vi.fn(() => pending.promise);
  const following = vi.fn();
  lifecycle.registerPageFeature('pending', {init});
  lifecycle.registerPageFeature('following', {init: following});

  expect(lifecycle.initPageFeatures()).toBeUndefined();
  lifecycle.initPageFeatures();
  expect(init).toHaveBeenCalledTimes(1);
  expect(following).toHaveBeenCalledTimes(1);

  pending.resolve();
  await pending.promise;
  expect(reportError).not.toHaveBeenCalled();
});

test('a current async failure is observed and cleans partially acquired resources', async () => {
  const pending = Promise.withResolvers();
  const failure = new Error('initialization failed');
  const cleanup = vi.fn();
  const retire = vi.fn();
  let run;
  lifecycle.registerPageFeature('async-failure', {
    init: (active) => {
      run = active;
      run.own(cleanup);
      return pending.promise;
    },
    retire,
  });
  lifecycle.initPageFeatures();

  pending.reject(failure);
  await Promise.resolve();

  expect(run.current()).toBe(false);
  expect(run.signal.aborted).toBe(true);
  expect(cleanup).toHaveBeenCalledExactlyOnceWith();
  expect(retire).toHaveBeenCalledExactlyOnceWith();
  expect(reportError).toHaveBeenCalledExactlyOnceWith('Could not initialize native feature "async-failure"', failure);
});

test('an async failure still retires the feature when its owned cleanup also fails', async () => {
  const pending = Promise.withResolvers();
  const failure = new Error('initializer failed');
  const cleanupFailure = new Error('cleanup failed');
  const remainingCleanup = vi.fn();
  const retire = vi.fn();
  lifecycle.registerPageFeature('async-cleanup-failure', {
    init: (run) => {
      run.own(remainingCleanup);
      run.own(() => { throw cleanupFailure });
      return pending.promise;
    },
    retire,
  });
  lifecycle.initPageFeatures();

  pending.reject(failure);
  await Promise.resolve();

  expect(remainingCleanup).toHaveBeenCalledExactlyOnceWith();
  expect(retire).toHaveBeenCalledExactlyOnceWith();
  expect(reportError.mock.calls).toEqual([
    ['Could not retire native feature "async-cleanup-failure"', cleanupFailure],
    ['Could not initialize native feature "async-cleanup-failure"', failure],
  ]);
});

test('a stale async failure cannot retire the replacement run of the same feature', async () => {
  const pending = Promise.withResolvers();
  const failure = new Error('outgoing initialization failed');
  const cleanups = [vi.fn(), vi.fn()];
  const runs = [];
  const retire = vi.fn();
  const init = vi.fn((run) => {
    run.own(cleanups[runs.length]);
    runs.push(run);
    if (runs.length === 1) return pending.promise;
    return undefined;
  });
  lifecycle.registerPageFeature('replacement', {init, retire});
  lifecycle.initPageFeatures();
  lifecycle.retirePageFeatures();
  lifecycle.initPageFeatures();

  pending.reject(failure);
  await Promise.resolve();

  expect(runs[0].current()).toBe(false);
  expect(runs[1].current()).toBe(true);
  expect(runs[1].signal.aborted).toBe(false);
  expect(cleanups[0]).toHaveBeenCalledExactlyOnceWith();
  expect(cleanups[1]).not.toHaveBeenCalled();
  expect(retire).toHaveBeenCalledExactlyOnceWith();
  lifecycle.initPageFeatures();
  expect(init).toHaveBeenCalledTimes(2);
  lifecycle.retirePageFeatures();
  expect(cleanups[1]).toHaveBeenCalledExactlyOnceWith();
  expect(retire).toHaveBeenCalledTimes(2);
});

test('a synchronous initialization failure preserves the original throw and cleans its partial run', () => {
  const failure = new Error('synchronous initializer failed');
  const cleanupFailure = new Error('owned cleanup failed');
  const retire = vi.fn();
  const following = vi.fn();
  let run;
  lifecycle.registerPageFeature('sync-failure', {
    init: (active) => {
      run = active;
      run.own(() => { throw cleanupFailure });
      throw failure;
    },
    retire,
  });
  lifecycle.registerPageFeature('following', {init: following});

  expect(() => lifecycle.initPageFeatures()).toThrow(failure);
  expect(run.current()).toBe(false);
  expect(run.signal.aborted).toBe(true);
  expect(retire).toHaveBeenCalledExactlyOnceWith();
  expect(following).not.toHaveBeenCalled();
  expect(reportError).toHaveBeenCalledExactlyOnceWith('Could not retire native feature "sync-failure"', cleanupFailure);
});

test('retirement drains every feature and its resources in reverse order despite cleanup failure', () => {
  const released = [];
  const failure = new Error('second feature cleanup failed');
  lifecycle.registerPageFeature('first', {
    init: (run) => {
      run.own(() => released.push('first resource'));
    },
    retire: () => released.push('first retire'),
  });
  lifecycle.registerPageFeature('second', {
    init: (run) => {
      run.own(() => released.push('second resource 1'));
      run.own(() => {
        released.push('second resource 2');
        throw failure;
      });
    },
    retire: () => released.push('second retire'),
  });
  lifecycle.initPageFeatures();

  expect(() => lifecycle.retirePageFeatures()).toThrow(failure);
  expect(released).toEqual(['second resource 2', 'second resource 1', 'second retire', 'first resource', 'first retire']);
  lifecycle.retirePageFeatures();
  expect(released).toHaveLength(5);
});
