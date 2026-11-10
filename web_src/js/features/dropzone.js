function retiredDropzoneError() {
  return new DOMException('Dropzone initialization was retired', 'AbortError');
}

export async function createDropzone(el, opts, lifetime = {}) {
  const current = () => !lifetime.signal?.aborted && (lifetime.current?.() ?? true);
  if (!current()) throw retiredDropzoneError();
  const [{Dropzone}] = await Promise.all([
    import(/* webpackChunkName: "dropzone" */'dropzone'),
    import(/* webpackChunkName: "dropzone" */'dropzone/dist/dropzone.css'),
  ]).catch((error) => {
    if (!current()) throw retiredDropzoneError();
    throw error;
  });
  if (!current()) throw retiredDropzoneError();
  const zone = new Dropzone(el, opts);
  if (!current()) {
    const error = retiredDropzoneError();
    try { zone.destroy() } catch (cause) { error.cause = cause }
    throw error;
  }
  return zone;
}
