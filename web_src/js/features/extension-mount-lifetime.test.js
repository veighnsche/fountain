import {createScopedStream, mountContribution} from './extension-mount.js';

function closeSocket() {
  this.readyState = 2;
}

class Socket extends EventTarget {
  readyState = 1;
  close = vi.fn(closeSocket);

  constructor(url) {
    super();
    this.url = url;
  }

  finishClose() {
    this.readyState = 3;
    this.dispatchEvent(new Event('close'));
  }
}

function pageRoot() {
  const root = document.createElement('div');
  Object.assign(root.dataset, {
    appSubUrl: '', extensionId: 'example', extensionPageId: 'home',
    extensionEntry: '/-/native/assets/example/main.js',
    extensionAssetBase: '/-/native/assets/example/',
    extensionApiBase: '/-/native/pages/example/home/api/',
    extensionSessionGeneration: 'session-a', extensionContractVersion: '1',
  });
  return root;
}

function mountOwner(handle = {}, hooks = {}) {
  return mountContribution(pageRoot(), {importer: async () => ({mount: () => handle}), websocketClass: Socket, ...hooks});
}

afterEach(() => vi.restoreAllMocks());

test('a retained owner forgets remotely closed streams while preserving open streams across suspension', async () => {
  const suspend = vi.fn();
  const resume = vi.fn();
  const owner = await mountOwner({suspend, resume});
  const closed = owner.context.stream('completed').socket;
  const live = owner.context.stream('live').socket;
  closed.finishClose();

  owner.suspend();
  owner.resume({sessionGeneration: 'session-a'});

  expect(closed.close).not.toHaveBeenCalled();
  expect(live.close).not.toHaveBeenCalled();
  expect(live.readyState).toBe(1);
  expect(suspend).toHaveBeenCalledExactlyOnceWith();
  expect(resume).toHaveBeenCalledExactlyOnceWith({sessionGeneration: 'session-a'});
  owner.retire('removed');
  expect(closed.close).not.toHaveBeenCalled();
  expect(live.close).toHaveBeenCalledExactlyOnceWith();
});

test('completed consumer streams detach their abort listener and are not closed again during owner cleanup', async () => {
  const consumer = new AbortController();
  const owner = await mountOwner();
  const stream = owner.context.stream('completed', {signal: consumer.signal});
  stream.close(1000, 'complete');
  stream.socket.finishClose();

  consumer.abort();
  owner.dispose();

  expect(stream.socket.close).toHaveBeenCalledExactlyOnceWith(1000, 'complete');
});

test('a standalone scoped stream releases its caller cancellation link after remote close', () => {
  const consumer = new AbortController();
  const stream = createScopedStream(`${window.location.origin}/-/native/pages/example/home/api/`, Socket)('events', {signal: consumer.signal});
  stream.socket.finishClose();

  consumer.abort();

  expect(stream.socket.close).not.toHaveBeenCalled();
});

test('live caller cancellation still closes the exact stream once', () => {
  const consumer = new AbortController();
  const stream = createScopedStream(`${window.location.origin}/-/native/pages/example/home/api/`, Socket)('events', {signal: consumer.signal});

  consumer.abort();
  consumer.abort();

  expect(stream.socket.close).toHaveBeenCalledExactlyOnceWith();
});

test('terminal cleanup revokes authority before callbacks and attempts every socket and contribution cleanup', async () => {
  let context;
  const callbackError = new Error('contribution retire failed');
  const retired = vi.fn(() => {
    expect(() => context.request('private')).toThrow('retired');
    expect(() => context.stream('later')).toThrow('retired');
    expect(() => context.navigate('/later')).toThrow('retired');
    throw callbackError;
  });
  const disposed = vi.fn();
  const fetcher = vi.fn().mockResolvedValue({status: 200});
  const owner = await mountOwner({retire: retired, dispose: disposed}, {fetcher});
  context = owner.context;
  await context.request('private');
  const first = context.stream('first').socket;
  const second = context.stream('second').socket;
  first.close.mockImplementation(() => {
    expect(fetcher.mock.lastCall[1].signal.aborted).toBe(true);
    expect(() => context.stream('during-close')).toThrow('retired');
    throw new Error('socket close failed');
  });

  expect(() => owner.retire('authority-revoked')).toThrow(callbackError);

  expect(first.close).toHaveBeenCalledExactlyOnceWith();
  expect(second.close).toHaveBeenCalledExactlyOnceWith();
  expect(retired).toHaveBeenCalledExactlyOnceWith('authority-revoked');
  expect(disposed).toHaveBeenCalledExactlyOnceWith();
  owner.dispose();
  owner.retire('again');
  expect(disposed).toHaveBeenCalledExactlyOnceWith();
});

test.each([undefined, {sessionGeneration: 'session-b'}])('resume remains fail-closed for invalid validation %j', async (validation) => {
  const resume = vi.fn();
  const retire = vi.fn();
  const owner = await mountOwner({resume, retire});
  const socket = owner.context.stream('live').socket;
  owner.suspend();

  owner.resume(validation);
  owner.resume({sessionGeneration: 'session-a'});

  expect(resume).not.toHaveBeenCalled();
  expect(retire).toHaveBeenCalledExactlyOnceWith('session-changed');
  expect(socket.close).toHaveBeenCalledExactlyOnceWith();
  expect(() => owner.context.request('private')).toThrow('retired');
});

test('a cancelled late mount drains retire and dispose and reports rejected cleanup through its mount promise', async () => {
  const controller = new AbortController();
  const mounted = Promise.withResolvers();
  let context;
  const pending = mountContribution(pageRoot(), {
    websocketClass: Socket,
    signal: controller.signal,
    importer: async () => ({mount: (_root, admitted) => {
      context = admitted;
      return mounted.promise;
    }}),
  });
  await Promise.resolve();
  const socket = context.stream('pending').socket;
  controller.abort();
  const retire = vi.fn(() => {
    expect(() => context.stream('late')).toThrow('retired');
    throw new Error('late retirement failed');
  });
  const cleanupError = new Error('late disposal failed');
  const dispose = vi.fn(() => { throw cleanupError });

  mounted.resolve({retire, dispose});
  await expect(pending).rejects.toThrow(cleanupError);

  expect(socket.close).toHaveBeenCalledExactlyOnceWith();
  expect(retire).toHaveBeenCalledExactlyOnceWith('authority-revoked');
  expect(dispose).toHaveBeenCalledExactlyOnceWith();
});

test('host abort observes callback failures even when the owner settles before its host receives the promise', async () => {
  const controller = new AbortController();
  const callbackError = new Error('Retirement callback failed');
  const retire = vi.fn(() => { throw callbackError });
  const dispose = vi.fn();
  const report = vi.spyOn(console, 'error').mockImplementation(() => {});
  const pending = mountOwner({retire, dispose}, {signal: controller.signal});
  await Promise.resolve();
  await Promise.resolve();

  controller.abort();
  const owner = await pending;

  expect(retire).toHaveBeenCalledExactlyOnceWith('authority-revoked');
  expect(dispose).toHaveBeenCalledTimes(1);
  expect(report).toHaveBeenCalledExactlyOnceWith('Could not retire aborted contribution', callbackError);
  expect(() => owner.context.request('private')).toThrow('retired');
});

test.each(['dispose', 'retire'])('terminal %s removes the host abort listener', async (method) => {
  const controller = new AbortController();
  const add = vi.spyOn(controller.signal, 'addEventListener');
  const remove = vi.spyOn(controller.signal, 'removeEventListener');
  const owner = await mountOwner({}, {signal: controller.signal});
  const abort = add.mock.calls.find(([name]) => name === 'abort')[1];

  owner[method]('removed');

  expect(remove).toHaveBeenCalledExactlyOnceWith('abort', abort);
});

test.each(['rejected import', 'missing mount'])('an unavailable owner releases its host abort listener after %s', async (failure) => {
  const controller = new AbortController();
  const add = vi.spyOn(controller.signal, 'addEventListener');
  const remove = vi.spyOn(controller.signal, 'removeEventListener');
  vi.spyOn(console, 'error').mockImplementation(() => {});
  const owner = await mountContribution(pageRoot(), {
    signal: controller.signal,
    websocketClass: Socket,
    importer: async () => {
      if (failure === 'rejected import') throw new Error('import failed');
      return {};
    },
  });
  const abort = add.mock.calls.find(([name]) => name === 'abort')[1];

  expect(owner).toBeNull();
  expect(remove).toHaveBeenCalledExactlyOnceWith('abort', abort);
});
