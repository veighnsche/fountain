import {appSubURLPathname, classifyNavigation, hasCredentialQuery, isAuthPath, matchesDirectPattern, validAppSubURL, validDirectPattern} from './extension-navigation-policy.js';

const origin = 'https://forge.example';
const base = `${origin}/forge/team/repo`;

test('direct routes measure decoded UTF-8 bytes at the 255-byte boundary', () => {
  for (const [value, accepted] of [
    ['a'.repeat(255), true], ['a'.repeat(256), false],
    ['界'.repeat(85), true], [`${'界'.repeat(85)}a`, false],
    [`${'😀'.repeat(63)}abc`, true], ['😀'.repeat(64), false],
  ]) {
    for (const segment of [value, encodeURIComponent(value)]) {
      for (const trailing of ['', '/']) {
        expect(matchesDirectPattern(`/applications/requests/${segment}${trailing}`, ['/applications/requests/{request}'])).toBe(accepted);
      }
    }
  }
  for (const segment of ['', 'one/two', '%2f', '%5c', '%ZZ', '.', '..', '%2e', '%2e%2e']) {
    expect(matchesDirectPattern(`/applications/requests/${segment}`, ['/applications/requests/{request}'])).toBe(false);
  }
});

test('owner patterns match only their declared slots and complete segments', () => {
  const owner = ['/applications', '/applications/requests/{request}'];
  expect(matchesDirectPattern('/applications', owner)).toBe(true);
  expect(matchesDirectPattern('/applications/', owner)).toBe(true);
  expect(matchesDirectPattern('/applications/requests/ABC-123', owner)).toBe(true);
  for (const target of ['/workspace', '/applications-old', '/applications/requests', '/applications/requests/x/extra', '/team/repo/applications', '/applications/requests/x?token=secret']) {
    expect(matchesDirectPattern(target, owner)).toBe(false);
  }
  for (const pattern of ['applications', '//applications', '/applications/../other', '/applications/{x}/{x}', '/applications/{Bad Name}', '/applications/a/b/c/d']) {
    expect(validDirectPattern(pattern)).toBe(false);
  }
});

test('app prefixes cannot normalize into a different deployment root', () => {
  for (const prefix of ['', '/forge', '/forge/nested', '/café', '/caf%C3%A9']) expect(validAppSubURL(prefix)).toBe(true);
  for (const prefix of ['/', '//forge', '/forge/', '/forge//nested', '/forge/../other', '/forge/%2e%2e', '/%2e', '/forge%2fother', '/forge%5cother', '/forge%ZZ', '/forge%0aother', '/forge?x=1']) {
    expect(validAppSubURL(prefix)).toBe(false);
  }
  expect(appSubURLPathname('/café')).toBe('/caf%C3%A9');
  expect(appSubURLPathname('/caf%C3%A9')).toBe('/caf%C3%A9');
  expect(classifyNavigation('/forge/team/repo', base, origin, '/forge').kind).toBe('native');
  expect(classifyNavigation('/forge-other/team/repo', base, origin, '/forge').kind).toBe('top');
  expect(classifyNavigation('/forge/../elsewhere', base, origin, '/forge').kind).toBe('top');
  expect(classifyNavigation('/café/team/repo', base, origin, '/café').kind).toBe('native');
  expect(classifyNavigation('/caf%C3%A9/team/repo', base, origin, '/caf%C3%A9').kind).toBe('native');
});

test('auth routes and credential queries retain top-level classification', () => {
  for (const path of [
    '/user/login', '/user/logout', '/user/sign_up', '/user/forgot_password', '/user/forget_password', '/user/reset_password',
    '/user/recover_account', '/user/activate', '/user/activate_email', '/user/two_factor', '/user/webauthn', '/user/oauth2',
    '/user/openid', '/user/link_account', '/user/link_account_signin', '/user/link_account_signup',
    '/user/settings/change_password', '/user/settings/security/two_factor', '/user/settings/applications', '/user/settings/keys',
    '/install', '/login/oauth/callback', '/login/openid', '/oauth2/authorize', '/openid', '/us%65r/login',
  ]) {
    expect(isAuthPath(path)).toBe(true);
    expect(classifyNavigation(`/forge${path}`, base, origin, '/forge').kind).toBe('top');
  }
  for (const key of ['token', 'PASSWORD', 'secret', 'client_secret', 'AUTHORIZATION', 'auth', 'code', 'credential', 'session', 'api_key', 'private_key', 'access_token', '_TOKEN']) {
    const url = new URL(`/forge/repo?${key}=secret`, base);
    expect(hasCredentialQuery(url)).toBe(true);
    expect(classifyNavigation(url.href, base, origin, '/forge').kind).toBe('top');
  }
  for (const path of ['/user/login-extra', '/user/settings/security-extra', '/users/login', '/repo/installations', '/login/oauth-extra', '/team/repo/issues']) {
    expect(isAuthPath(path)).toBe(false);
    expect(classifyNavigation(`/forge${path}?monkey=usual`, base, origin, '/forge').kind).toBe('native');
  }
});

test('URL parsing keeps external destinations top-level and unsafe targets rejected', () => {
  for (const target of ['https://other.example/path', 'mailto:person@example.com', 'tel:+123456']) {
    expect(classifyNavigation(target, base, origin, '/forge').kind).toBe('top');
  }
  for (const target of [['javascript', 'alert(1)'].join(':'), 'data:text/html,unsafe', 'https://person:secret@forge.example/forge/repo', '/forge/team%2frepo', '/forge/team%5crepo', '/forge/team\\repo', '/forge/%ZZ', '/forge/-/native/session', '/forge/%2d/native/session']) {
    expect(classifyNavigation(target, base, origin, '/forge').kind).toBe('reject');
  }
});
