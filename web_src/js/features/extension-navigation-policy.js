import navigationPolicy from '../../../sdk/navigationpolicy/policy.json';

const credentialKeys = new Set(navigationPolicy.credential_query_keys);
const encoder = new TextEncoder();
const slug = /^[a-z0-9]+(?:-[a-z0-9]+)*$/;

function decodedSegments(pathname) {
  try {
    return pathname.split('/').map((segment) => decodeURIComponent(segment));
  } catch {
    return null;
  }
}

export function isAuthPath(pathname) {
  const segments = decodedSegments(pathname);
  return segments !== null && navigationPolicy.auth_path_segments.some((auth) =>
    segments.some((_, start) => auth.every((segment, i) => segments[start + i] === segment)));
}

export function hasCredentialQuery(url) {
  return Array.from(url.searchParams.keys()).some((key) => {
    key = key.toLowerCase();
    return credentialKeys.has(key) || key.endsWith(navigationPolicy.credential_query_suffix);
  });
}

export function validAppSubURL(value) {
  if (typeof value !== 'string') return false;
  if (value === '') return true;
  if (!/^\/[^/\\?#\s]+(?:\/[^/\\?#\s]+)*$/.test(value) || /%2f|%5c/i.test(value)) return false;
  const segments = decodedSegments(value);
  if (!segments || segments.some((segment) => segment === '.' || segment === '..' || /[\\\r\n]/.test(segment))) return false;
  try {
    encodeURIComponent(value);
    return true;
  } catch {
    return false;
  }
}

export function appSubURLPathname(value) {
  return validAppSubURL(value) ? (value === '' ? '' : new URL(value, 'https://extension.invalid').pathname) : null;
}

/** Classify a browser destination without changing top-level auth navigation. */
export function classifyNavigation(input, base, origin, appSubPath = '') {
  let url;
  try {
    if (typeof input === 'string' && /[\\\r\n]/.test(input)) return {kind: 'reject'};
    url = new URL(input, base);
  } catch {
    return {kind: 'reject'};
  }
  if (['mailto:', 'tel:'].includes(url.protocol)) return {kind: 'top', url: url.href};
  if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || /%2f|%5c/i.test(url.pathname)) return {kind: 'reject'};
  const prefix = appSubURLPathname(appSubPath);
  const segments = decodedSegments(url.pathname);
  if (prefix === null || segments === null || segments.some((segment) => /[\\\r\n]/.test(segment))) return {kind: 'reject'};
  if (prefix && !url.pathname.startsWith(`${prefix}/`)) return {kind: 'top', url: url.href};
  if (url.origin !== origin || isAuthPath(url.pathname) || hasCredentialQuery(url)) return {kind: 'top', url: url.href};
  if (segments.some((segment, i) => segment === '-' && segments[i + 1] === 'native')) return {kind: 'reject'};
  return {kind: 'native', url: url.href, path: `${url.pathname}${url.search}${url.hash}`};
}

export function validDirectPattern(pattern) {
  if (typeof pattern !== 'string' || !pattern.startsWith('/')) return false;
  const segments = pattern.slice(1).split('/');
  if (segments.length > 4 || !slug.test(segments[0])) return false;
  const parameters = new Set();
  return segments.slice(1).every((segment) => {
    if (slug.test(segment)) return true;
    const parameter = /^\{([a-z0-9]+(?:-[a-z0-9]+)*)\}$/.exec(segment)?.[1];
    if (!parameter || parameters.has(parameter)) return false;
    parameters.add(parameter);
    return true;
  });
}

/** Match admitted owner routes; captured parameters are decoded UTF-8 bytes. */
export function matchesDirectPattern(target, patterns) {
  if (typeof target !== 'string' || !target.startsWith('/') || /[?#\\\r\n]/.test(target) || /%2f|%5c/i.test(target) || !Array.isArray(patterns)) return false;
  const parts = decodedSegments(target.replace(/\/$/, ''));
  if (!parts || parts.some((part) => part === '.' || part === '..' || /[\\\r\n]/.test(part))) return false;
  return patterns.some((pattern) => {
    if (!validDirectPattern(pattern)) return false;
    const segments = pattern.split('/');
    if (parts.length !== segments.length) return false;
    return segments.every((segment, i) => segment.startsWith('{') ?
      parts[i] !== '' && encoder.encode(parts[i]).byteLength <= navigationPolicy.opaque_route_segment_bytes : segment === parts[i]);
  });
}
