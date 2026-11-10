import {createFeatureRun} from './native-feature-run.js';

test('a feature run aborts before releasing resources in reverse order exactly once', () => {
  const run = createFeatureRun();
  const released = [];
  const first = run.own(() => released.push(['first', run.current(), run.signal.aborted]));
  const second = run.own(() => released.push(['second', run.current(), run.signal.aborted]));

  expect(run.current()).toBe(true);
  expect(run.signal.aborted).toBe(false);
  run.dispose();
  run.dispose();
  first();
  second();

  expect(released).toEqual([['second', false, true], ['first', false, true]]);
});

test('a resource released early is not released again when its run retires', () => {
  const run = createFeatureRun();
  const cleanup = vi.fn();
  const release = run.own(cleanup);

  release();
  release();
  run.dispose();

  expect(cleanup).toHaveBeenCalledExactlyOnceWith();
});

test('a resource acquired after disposal is released immediately and exactly once', () => {
  const run = createFeatureRun();
  const cleanup = vi.fn();
  run.dispose();

  const release = run.own(cleanup);
  expect(cleanup).toHaveBeenCalledExactlyOnceWith();
  release();
  run.dispose();
  expect(cleanup).toHaveBeenCalledExactlyOnceWith();
});

test('a superseded run refuses a late resource before disposal', () => {
  let current = true;
  const run = createFeatureRun(() => current);
  const retained = vi.fn();
  const late = vi.fn();
  run.own(retained);
  current = false;

  expect(run.current()).toBe(false);
  const release = run.own(late);
  expect(late).toHaveBeenCalledExactlyOnceWith();
  expect(retained).not.toHaveBeenCalled();

  run.dispose();
  release();
  expect(retained).toHaveBeenCalledExactlyOnceWith();
  expect(late).toHaveBeenCalledExactlyOnceWith();
});

test('failing cleanup drains every resource and preserves the first failure in reverse order', () => {
  const run = createFeatureRun();
  const released = [];
  const firstError = new Error('last acquired cleanup failed');
  const laterError = new Error('earlier cleanup failed');
  run.own(() => {
    released.push('first');
    throw laterError;
  });
  run.own(() => released.push('second'));
  const releaseThird = run.own(() => {
    released.push('third');
    throw firstError;
  });

  expect(() => run.dispose()).toThrow(firstError);
  expect(released).toEqual(['third', 'second', 'first']);
  expect(run.signal.aborted).toBe(true);
  expect(run.current()).toBe(false);
  expect(() => run.dispose()).not.toThrow();
  expect(() => releaseThird()).not.toThrow();
  expect(released).toEqual(['third', 'second', 'first']);
});
