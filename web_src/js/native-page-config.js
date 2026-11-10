export function normalizeMentionValues(values) {
  return Array.from(new Map((values ?? []).map((value) => [value.name, value])).values());
}

export function setPageConfig(pageData) {
  window.config.pageData = pageData ?? {};
  window.config.mentionValues = normalizeMentionValues(window.config.pageData.mentionValues);
}
