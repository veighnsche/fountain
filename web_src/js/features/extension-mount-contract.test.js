import {vi} from 'vitest';
import {mountContribution, parseMountDataset, mountIdentity, mountOwnerKey} from './extension-mount.js';

function mountRoot({mode = 'page', prefix = '', owner = ''} = {}) {
  const root = document.createElement('div');
  Object.assign(root.dataset, {
    appSubUrl: prefix,
    extensionId: 'example',
    extensionEntry: `${prefix}/-/native/assets/example/main.js`,
    extensionAssetBase: `${prefix}/-/native/assets/example/`,
    extensionApiBase: `${prefix}/-/native/${mode}s/example/home/api/`,
    extensionSessionGeneration: 'session-a',
    extensionContractVersion: '1',
  });
  root.dataset[mode === 'page' ? 'extensionPageId' : 'extensionPanelId'] = 'home';
  if (owner) root.dataset.extensionOwnerId = owner;
  return root;
}

test('mount identity tracks the admitted definition and ignores repository presentation', () => {
  const parsed = parseMountDataset(mountRoot());
  expect(parsed.ok).toBe(true);
  const original = mountIdentity(parsed);
  expect(mountIdentity({...parsed, repositoryId: '7'})).toBe(original);
  expect(mountIdentity({...parsed, directPatterns: ['/feed']})).toBe(original);
  for (const change of [
    {mode: 'panel'}, {extensionId: 'another'}, {contributionId: 'another'}, {ownerId: 'shared'},
    {entryHref: `${parsed.assetHref}next.js`}, {apiHref: `${parsed.apiHref}changed/`},
    {assetHref: `${parsed.assetHref}changed/`}, {appSubUrl: '/forge'},
    {sessionGeneration: 'session-b'}, {contractVersion: '2'},
  ]) {
    expect(mountIdentity({...parsed, ...change})).not.toBe(original);
  }
});

test('equivalent normalized mount URLs and Unicode app prefixes have one identity', () => {
  const raw = mountRoot({prefix: '/équipe'});
  const encoded = mountRoot({prefix: '/%C3%A9quipe'});
  encoded.dataset.extensionEntry = new URL(encoded.dataset.extensionEntry, window.location.origin).href;
  expect(parseMountDataset(raw).ok).toBe(true);
  expect(parseMountDataset(encoded).ok).toBe(true);
  expect(mountIdentity(parseMountDataset(raw))).toBe(mountIdentity(parseMountDataset(encoded)));
});

test('owner keys include the extension namespace and require an admitted mount', () => {
  const parsed = parseMountDataset(mountRoot({owner: 'shared'}));
  const key = mountOwnerKey(parsed);
  expect(key).not.toBeNull();
  expect(mountOwnerKey({...parsed, extensionId: 'another'})).not.toBe(key);
  expect(mountOwnerKey({...parsed, ownerId: 'another'})).not.toBe(key);
  expect(mountOwnerKey({...parsed, ok: false})).toBeNull();
  expect(mountOwnerKey(parseMountDataset(mountRoot()))).toBeNull();
});

test.each(['2', undefined])('version %s retains only structural facts for skew detection and never mounts', async (version) => {
  const root = mountRoot();
  const valid = parseMountDataset(root);
  if (version === undefined) delete root.dataset.extensionContractVersion;
  else root.dataset.extensionContractVersion = version;
  const parsed = parseMountDataset(root);
  expect(parsed.ok).toBe(false);
  expect(mountIdentity(parsed)).toBeNull();
  expect(mountIdentity(parsed, {includeContractVersion: false})).toBe(mountIdentity(valid, {includeContractVersion: false}));
  const importer = vi.fn();
  const fetcher = vi.fn();
  expect(await mountContribution(root, {importer, fetcher})).toBeNull();
  expect(importer).not.toHaveBeenCalled();
  expect(fetcher).not.toHaveBeenCalled();
});

test('optional direct route facts preserve old version-one mounts and reject malformed declarations', () => {
  const root = mountRoot();
  expect(parseMountDataset(root).directPatterns).toEqual([]);
  root.dataset.extensionDirectPatterns = JSON.stringify(['/feed', '/feed/requests/{request}']);
  expect(parseMountDataset(root).directPatterns).toEqual(['/feed', '/feed/requests/{request}']);
  for (const value of ['{', '{}', 'null', '[1]', '["feed"]', '["/feed/../escape"]']) {
    root.dataset.extensionDirectPatterns = value;
    const parsed = parseMountDataset(root);
    expect(parsed.ok).toBe(false);
    expect(mountIdentity(parsed, {includeContractVersion: false})).toBeNull();
  }
});

test.each(['', '/forge', '/équipe'])('linked panels accept exactly a same-extension global page API under %s', (prefix) => {
  const root = mountRoot({mode: 'panel', prefix, owner: 'shared'});
  root.dataset.extensionApiBase = `${prefix}/-/native/pages/example/spaces/api/`;
  expect(parseMountDataset(root).ok).toBe(true);
});

test.each([
  '/extra/-/native/pages/example/spaces/api/',
  '/user/settings/-/native/pages/example/spaces/api/',
  '/-/native/pages/foreign/spaces/api/',
  '/-/native/pages/example/spaces/extra/api/',
  '/-/native/pages/example/spaces/api/extra/api/',
  '/-/native/pages/example//api/',
  '/-/native/pages/example/Bad/api/',
  '/-/native/pages/example/spaces%2Fextra/api/',
  '/-/native/pages/example/spaces/api/?token=private',
])('linked panels refuse noncanonical API %s before importing', async (apiPath) => {
  const root = mountRoot({mode: 'panel', prefix: '/forge', owner: 'shared'});
  root.dataset.extensionApiBase = `/forge${apiPath}`;
  const importer = vi.fn();
  const fetcher = vi.fn();
  expect(parseMountDataset(root).ok).toBe(false);
  expect(await mountContribution(root, {importer, fetcher})).toBeNull();
  expect(importer).not.toHaveBeenCalled();
  expect(fetcher).not.toHaveBeenCalled();
});

test('ambiguous contribution kind and malformed identities expose no reusable identity', () => {
  for (const change of [{extensionPanelId: 'other'}, {extensionId: '../example'}, {extensionPageId: 'home/extra'}]) {
    const root = mountRoot();
    Object.assign(root.dataset, change);
    const parsed = parseMountDataset(root);
    expect(parsed.ok).toBe(false);
    expect(mountIdentity(parsed, {includeContractVersion: false})).toBeNull();
  }
});
