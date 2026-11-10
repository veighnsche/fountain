import {vi} from 'vitest';
import {GET} from './modules/fetch.js';
import {createGraphRequest} from './native-graph.js';

vi.mock('./modules/fetch.js', () => ({GET: vi.fn()}));

function deferred() {
  let resolve, reject;
  const promise = new Promise((accept, refuse) => { resolve = accept; reject = refuse });
  return {promise, resolve, reject};
}

function callbacks() {
  return {apply: vi.fn(), error: vi.fn(), loading: vi.fn()};
}

beforeEach(() => {
  vi.useFakeTimers();
  GET.mockReset();
});

afterEach(() => vi.useRealTimers());

test('graph preparation retries 202 after one second and decodes only the ready response', async () => {
  const pendingJSON = vi.fn();
  GET.mockResolvedValueOnce({status: 202, json: pendingJSON})
    .mockResolvedValueOnce({status: 200, ok: true, json: async () => ({week: 7})});
  const graph = createGraphRequest();
  const updates = callbacks();
  const pending = graph.run('/old/repo/activity/data', updates);
  await vi.advanceTimersByTimeAsync(0);
  expect(GET).toHaveBeenCalledTimes(1);
  await vi.advanceTimersByTimeAsync(999);
  expect(GET).toHaveBeenCalledTimes(1);
  await vi.advanceTimersByTimeAsync(1);
  await pending;
  expect(GET).toHaveBeenCalledTimes(2);
  expect(GET.mock.calls.map(([url]) => url)).toEqual(['/old/repo/activity/data', '/old/repo/activity/data']);
  expect(pendingJSON).not.toHaveBeenCalled();
  expect(updates.apply).toHaveBeenCalledExactlyOnceWith({week: 7});
  expect(updates.error).not.toHaveBeenCalled();
  expect(updates.loading.mock.calls).toEqual([[true], [false]]);
});

test('retiring a graph cancels the polling wait without another request or stale UI write', async () => {
  GET.mockResolvedValue({status: 202});
  const graph = createGraphRequest();
  const updates = callbacks();
  const pending = graph.run('/repo/activity/data', updates);
  await vi.advanceTimersByTimeAsync(0);
  expect(vi.getTimerCount()).toBe(1);
  graph.dispose();
  await pending;
  expect(GET.mock.calls[0][1].signal.aborted).toBe(true);
  expect(vi.getTimerCount()).toBe(0);
  await vi.advanceTimersByTimeAsync(5000);
  expect(GET).toHaveBeenCalledOnce();
  expect(updates.apply).not.toHaveBeenCalled();
  expect(updates.error).not.toHaveBeenCalled();
  expect(updates.loading.mock.calls).toEqual([[true]]);
});

test.each(['resolve', 'reject'])('a replaced response body cannot %s into the new request state', async (completion) => {
  const oldBody = deferred();
  const nextResponse = deferred();
  const oldJSON = vi.fn(() => oldBody.promise);
  GET.mockResolvedValueOnce({status: 200, ok: true, json: oldJSON}).mockReturnValueOnce(nextResponse.promise);
  const graph = createGraphRequest();
  const updates = callbacks();
  const old = graph.run('/old/repo/data', updates);
  await vi.advanceTimersByTimeAsync(0);
  expect(oldJSON).toHaveBeenCalledOnce();
  const current = graph.run('/new/repo/data', updates);
  expect(GET.mock.calls[0][1].signal.aborted).toBe(true);
  oldBody[completion](completion === 'resolve' ? {source: 'old'} : new Error('old body failed'));
  await old;
  expect(updates.apply).not.toHaveBeenCalled();
  expect(updates.error).not.toHaveBeenCalled();
  expect(updates.loading.mock.calls).toEqual([[true], [true]]);
  nextResponse.resolve({status: 200, ok: true, json: async () => ({source: 'new'})});
  await current;
  expect(updates.apply).toHaveBeenCalledExactlyOnceWith({source: 'new'});
  expect(updates.loading.mock.calls).toEqual([[true], [true], [false]]);
});

test('a retired late 202 never starts polling and a disposed graph cannot restart', async () => {
  const response = deferred();
  GET.mockReturnValue(response.promise);
  const graph = createGraphRequest();
  const updates = callbacks();
  const pending = graph.run('/repo/data', updates);
  graph.dispose();
  response.resolve({status: 202});
  await pending;
  await graph.run('/another/repo/data', updates);
  expect(GET).toHaveBeenCalledOnce();
  expect(vi.getTimerCount()).toBe(0);
  expect(updates.error).not.toHaveBeenCalled();
  expect(updates.loading.mock.calls).toEqual([[true]]);
});

test('current HTTP and transform errors finish only the active loading state', async () => {
  GET.mockResolvedValueOnce({status: 403, ok: false, statusText: 'Forbidden'})
    .mockResolvedValueOnce({status: 200, ok: true, json: async () => ({week: 7})});
  const graph = createGraphRequest();
  const updates = callbacks();
  await graph.run('/repo/data', updates);
  expect(updates.error).toHaveBeenCalledExactlyOnceWith('Forbidden');
  updates.apply.mockImplementation(() => { throw new Error('invalid graph data') });
  await graph.run('/repo/data', updates);
  expect(updates.error).toHaveBeenLastCalledWith('invalid graph data');
  expect(updates.loading.mock.calls).toEqual([[true], [false], [true], [false]]);
});
