// bootstrap module must be the first one to be imported, it handles webpack lazy-loading and global errors
import './bootstrap.js';

import {initRepoActivityTopAuthorsChart, retireRepoActivityTopAuthorsChart} from './features/repo-activity-top-authors.ts';
import {initDashboardRepoList, retireDashboardRepoList} from './features/dashboard-repo-list.ts';

import {initGlobalCopyToClipboardListener} from './features/clipboard.js';
import {initContextPopups, retireContextPopups} from './features/contextpopup.js';
import {initRepoGraphGit, retireRepoGraphGit} from './features/repo-graph.js';
import {initHeatmap, retireHeatmap} from './features/heatmap.js';
import {initImageDiff, retireImageDiff} from './features/imagediff.js';
import {initRepoMigration, retireRepoMigration} from './features/repo-migration.js';
import {initRepoProject, retireRepoProject} from './features/repo-projects.js';
import {initTableSort, retireTableSort} from './features/tablesort.js';
import {initAutoFocusEnd} from './features/autofocus-end.js';
import {initAdminUserListSearchForm, retireAdminUserListSearchForm} from './features/admin/users.js';
import {initGlobalFeatures, initPageFeatures, readNativePageData, registerGlobalFeature, registerPageFeature, setPageConfig} from './native-lifecycle.js';
import {initNativeHtmx, retireNativeHtmx, setNativeSwapEnhancer} from './htmx.js';
import {initAdminConfigs, retireAdminConfigs} from './features/admin/config.js';
import {initMarkupAnchors, retireMarkupAnchors} from './markup/anchors.js';
import {initNotificationCount, initNotificationsTable, retireNotificationsTable} from './features/notification.js';
import {initRepoIssueContentHistory, retireRepoIssueContentHistory} from './features/repo-issue-content.js';
import {initStopwatch} from './features/stopwatch.js';
import {initFindFileInRepo, retireFindFileInRepo} from './features/repo-findfile.js';
import {initCommentContent, retireCommentContent, initMarkupContent, retireMarkupContent} from './markup/content.js';

import {initUserAuthOauth2, initUserAuth} from './features/user-auth.js';
import {
  initRepoIssueDue,
  initRepoIssueReferenceRepositorySearch,
  retireRepoIssueReferenceRepositorySearch,
  initRepoIssueTimeTracking,
  initRepoIssueWipTitle,
  retireRepoIssueWipTitle,
  initRepoPullRequestAllowMaintainerEdit,
  retireRepoPullRequestAllowMaintainerEdit,
  initRepoPullRequestReview,
  retireRepoPullRequestReview,
  initArchivedLabelHandler,
  initRepoIssueCommentDelete,
  initRepoIssueDependencyDelete,
  initRepoIssueCodeCommentCancel,
  initRepoIssueReferenceIssue,
  initRepoPullRequestUpdate,
  retireRepoPullRequestUpdate,
  initRepoIssueComments,
  retireRepoIssueComments,
  initRepoIssueWipToggle,
  retireRepoIssueWipToggle,
  initRepoIssueTitleEdit,
  retireRepoIssueTitleEdit,
  initRepoIssueBranchSelect,
  retireRepoIssueBranchSelect,
  initRepoIssueAssignMe,
  retireRepoIssueAssignMe,
} from './features/repo-issue.js';
import {initRepoIssueSidebarList, retireRepoIssueSidebarList} from './features/repo-issue-sidebar-list.ts';
import {initRepoEllipsisButton, retireRepoEllipsisButton, initCommitStatuses, retireCommitStatuses, initCommitNotes, retireCommitNotes} from './features/repo-commit.js';
import {
  initFootLanguageMenu,
  initGlobalButtonClickOnEnter,
  initGlobalButtons,
  initGlobalCommonListeners,
  enhanceNativeNodes,
  retireNativeNodes,
  initGlobalEnterQuickSubmit,
  initGlobalLinkActions,
  initHeadNavbarContentToggle,
} from './features/common-global.js';
import {initRepoTopicBar, retireRepoTopicBar} from './features/repo-home.js';
import {initAdminEmails, retireAdminEmails} from './features/admin/emails.js';
import {initAdminCommon, retireAdminCommon} from './features/admin/common.js';
import {initAdminExtensions, retireAdminExtensions} from './features/admin-extensions.ts';
import {initRepoTemplateSearch, retireRepoTemplateSearch} from './features/repo-template.js';
import {initRepoCodeView, retireRepoCodeView} from './features/repo-code.ts';
import {initSshKeyFormParser, retireSshKeyFormParser} from './features/sshkey-helper.js';
import {initRepoArchiveLinks, initRepoCloneLink, retireRepoCommon} from './features/repo-common.js';
import {initRepoMigrationStatusChecker, retireRepoMigrationStatusChecker} from './features/repo-migrate.js';
import {
  initRepoSettingGitHook,
  retireRepoSettingGitHook,
  initRepoSettingsCollaboration,
  retireRepoSettingsCollaboration,
  initRepoSettingSearchTeamBox,
  retireRepoSettingSearchTeamBox,
  initRepoSettingBranches,
  retireRepoSettingBranches,
} from './features/repo-settings.js';
import {initRepoDiffView, retireRepoDiffView, initRepoDiffConversationNav, retireRepoDiffConversationNav} from './features/repo-diff.js';
import {initOrgTeamSearchRepoBox, retireOrgTeamSearchRepoBox} from './features/org-team.js';
import {initUserAuthWebAuthn, initUserAuthWebAuthnRegister} from './features/user-auth-webauthn.js';
import {initRepoRelease, retireRepoRelease, initRepoReleaseNew, retireRepoReleaseNew} from './features/repo-release.js';
import {initRepoEditor, retireRepoEditor} from './features/repo-editor.js';
import {initCompSearchUserBox, retireCompSearchUserBox} from './features/comp/SearchUserBox.js';
import {initInstall} from './features/install.js';
import {initCompWebHookEditor, retireCompWebHookEditor} from './features/comp/WebHookEditor.js';
import {initRepoBranchButton, retireRepoBranchButton} from './features/repo-branch.ts';
import {initRepoBranchTagSelector, retireRepoBranchTagSelector} from './features/repo-branch-tag-selector.js';
import {initUnicodeEscapeButton} from './features/repo-unicode-escape.js';
import {initCommonOrganization, retireCommonOrganization} from './features/common-organization.js';
import {initRepoWikiForm, retireRepoWikiForm} from './features/repo-wiki.js';
import {initRepoCommentForm, retireRepoCommentForm, initRepository, retireRepository} from './features/repo-legacy.js';
import {initCopyContent, retireCopyContent} from './features/copycontent.js';
import {initCaptcha} from './features/captcha.js';
import {initRepositoryActionView, retireRepositoryActionView} from './features/repo-action-view.ts';
import {initRepoActionsList} from './features/repo-actions-list.js';
import {initGlobalTooltips} from './modules/tippy.js';
import {initDropdowns} from './modules/dropdown.ts';
import {initGiteaFomantic} from './modules/fomantic.js';
import {onDomReady} from './utils/dom.js';
import {initRepoIssueList, retireRepoIssueList} from './features/repo-issue-list.js';
import {initCommonIssueListQuickGoto, retireCommonIssueListQuickGoto} from './features/common-issue-list.js';
import {initRepoContributors, retireRepoContributors} from './features/contributors.js';
import {initRepoCodeFrequency, retireRepoCodeFrequency} from './features/code-frequency.js';
import {initRepoRecentCommits, retireRepoRecentCommits} from './features/recent-commits.js';
import {initRepoDiffCommitBranchesAndTags, retireRepoDiffCommitBranchesAndTags} from './features/repo-diff-commit.js';
import {initRepoPullRequestMergeForm, retireRepoPullRequestMergeForm} from './features/repo-issue-pr-form.js';
import {initRepoPullRequestCommitStatus, retireRepoPullRequestCommitStatus} from './features/repo-issue-pr-status.js';
import {initDirAuto} from './modules/dirauto.js';
import {initRepositorySearch, retireRepositorySearch} from './features/repo-search.js';
import {initColorPickers, retireColorPickers} from './features/colorpicker.js';
import {initRepoMilestoneEditor, retireRepoMilestoneEditor} from './features/repo-milestone.js';
import {initModalClose, retireNativeModals} from './modules/modal.ts';
import {initExtensionPages, initExtensionWorkspace} from './features/extensions.js';

// Register first so reverse retirement cleans HTMX after other native
// features release their outgoing DOM, before A replaces the content.
registerPageFeature('native-htmx', {
  init: initNativeHtmx,
  retire: retireNativeHtmx,
});
registerPageFeature('admin-user-list-search-form', {
  init: initAdminUserListSearchForm,
  retire: retireAdminUserListSearchForm,
});

// Document-delegated handlers: each initializes once per document and stays
// single across native transitions; they query live DOM at event time.
registerGlobalFeature('native-dropdowns', initDropdowns);
registerGlobalFeature('native-tooltips', initGlobalTooltips);
registerGlobalFeature('native-modal-close', initModalClose);
registerGlobalFeature('native-common-listeners', initGlobalCommonListeners);
// Navbar and footer live in the persistent shell chrome, so their handlers
// attach once and survive content transplants.
registerGlobalFeature('native-navbar-toggle', initHeadNavbarContentToggle);
registerGlobalFeature('native-language-menu', initFootLanguageMenu);
// Common controls are document-delegated: one handler each, live-DOM queries
// at event time, so transplanted controls act exactly once.
registerGlobalFeature('native-clipboard', initGlobalCopyToClipboardListener);
registerGlobalFeature('native-quick-submit', initGlobalEnterQuickSubmit);
registerGlobalFeature('native-button-enter', initGlobalButtonClickOnEnter);
registerGlobalFeature('native-global-buttons', initGlobalButtons);
registerGlobalFeature('native-link-actions', initGlobalLinkActions);
// B05 document-lifetime set: handlers delegated to document install once
// and are never retired by navigation; they query live DOM at event time.
// Repeated page entry must not stack them or spawn new workers.
registerGlobalFeature('repo-issue-time-tracking', initRepoIssueTimeTracking);
registerGlobalFeature('repo-issue-due', initRepoIssueDue);
registerGlobalFeature('repo-issue-comment-delete', initRepoIssueCommentDelete);
registerGlobalFeature('repo-issue-dependency-delete', initRepoIssueDependencyDelete);
registerGlobalFeature('repo-issue-code-comment-cancel', initRepoIssueCodeCommentCancel);
registerGlobalFeature('repo-issue-reference-issue', initRepoIssueReferenceIssue);
// Notification count owns a SharedWorker / poller for the document lifetime;
// registering it as a page feature would leak a worker per transition.
registerGlobalFeature('notification-count', initNotificationCount);
registerGlobalFeature('stopwatch', initStopwatch);
registerGlobalFeature('repo-unicode-escape', initUnicodeEscapeButton);
registerGlobalFeature('repo-actions-list', initRepoActionsList);
// HTMX swaps bypass the page seam; enhance their fresh native nodes through
// the same tracked enhancement used for transplants.
setNativeSwapEnhancer((root) => enhanceNativeNodes(root));
// Fresh-node enhancement flows through the page seam so every transplant
// enhances its incoming region with no transition-side calls.
registerPageFeature('native-node-enhancement', {
  init: () => enhanceNativeNodes(document),
  retire: retireNativeNodes,
});
registerPageFeature('native-dialogs', {init: () => {}, retire: retireNativeModals});
// Sortable table headers on admin list pages (/admin/users, /admin/orgs).
registerPageFeature('native-table-sort', {
  init: initTableSort,
  retire: retireTableSort,
});

// B07 closure: B04 browsing/editor page features (7 + conversation-nav).
// Diff sub-features (file tree, commit select, viewed files) retire through
// repo-diff-view; image diff and branch/tag loader are page-owned here.
registerPageFeature('repo-code-view', {init: initRepoCodeView, retire: retireRepoCodeView});
registerPageFeature('repo-editor', {init: initRepoEditor, retire: retireRepoEditor});
registerPageFeature('repo-diff-view', {init: initRepoDiffView, retire: retireRepoDiffView});
registerPageFeature('repo-diff-conversation-nav', {init: initRepoDiffConversationNav, retire: retireRepoDiffConversationNav});
registerPageFeature('repo-wiki-form', {init: initRepoWikiForm, retire: retireRepoWikiForm});
registerPageFeature('repo-graph', {init: initRepoGraphGit, retire: retireRepoGraphGit});
registerPageFeature('repo-find-file', {init: initFindFileInRepo, retire: retireFindFileInRepo});
registerPageFeature('repo-image-diff', {init: initImageDiff, retire: retireImageDiff});
registerPageFeature('repo-diff-commit-branches-and-tags', {init: initRepoDiffCommitBranchesAndTags, retire: retireRepoDiffCommitBranchesAndTags});

// B07 closure: B05 issue/PR/comment page features (19).
registerPageFeature('repo-issue-list', {init: initRepoIssueList, retire: retireRepoIssueList});
registerPageFeature('common-issue-list-quick-goto', {init: initCommonIssueListQuickGoto, retire: retireCommonIssueListQuickGoto});
registerPageFeature('repo-issue-content-history', {init: initRepoIssueContentHistory, retire: retireRepoIssueContentHistory});
registerPageFeature('repo-issue-sidebar-list', {init: initRepoIssueSidebarList, retire: retireRepoIssueSidebarList});
registerPageFeature('repo-comment-form', {init: initRepoCommentForm, retire: retireRepoCommentForm});
registerPageFeature('repo-pull-request-update', {init: initRepoPullRequestUpdate, retire: retireRepoPullRequestUpdate});
registerPageFeature('repo-pull-request-allow-maintainer-edit', {init: initRepoPullRequestAllowMaintainerEdit, retire: retireRepoPullRequestAllowMaintainerEdit});
registerPageFeature('repo-issue-reference-repository-search', {init: initRepoIssueReferenceRepositorySearch, retire: retireRepoIssueReferenceRepositorySearch});
registerPageFeature('repo-issue-wip-title', {init: initRepoIssueWipTitle, retire: retireRepoIssueWipTitle});
registerPageFeature('repo-issue-comments', {init: initRepoIssueComments, retire: retireRepoIssueComments});
registerPageFeature('repo-pull-request-review', {init: initRepoPullRequestReview, retire: retireRepoPullRequestReview});
registerPageFeature('repo-issue-wip-toggle', {init: initRepoIssueWipToggle, retire: retireRepoIssueWipToggle});
registerPageFeature('repo-issue-title-edit', {init: initRepoIssueTitleEdit, retire: retireRepoIssueTitleEdit});
registerPageFeature('repo-issue-branch-select', {init: initRepoIssueBranchSelect, retire: retireRepoIssueBranchSelect});
registerPageFeature('repo-issue-assign-me', {init: initRepoIssueAssignMe, retire: retireRepoIssueAssignMe});
registerPageFeature('repo-pull-request-merge-form', {init: initRepoPullRequestMergeForm, retire: retireRepoPullRequestMergeForm});
registerPageFeature('repo-pull-request-commit-status', {init: initRepoPullRequestCommitStatus, retire: retireRepoPullRequestCommitStatus});
registerPageFeature('repo-settings-collaboration', {init: initRepoSettingsCollaboration, retire: retireRepoSettingsCollaboration});
registerPageFeature('archived-label-handler', {init: initArchivedLabelHandler});

// B07 closure: B06 account/org/admin page features (7). LabelEdit retires
// through common-organization, so it is not registered separately.
registerPageFeature('admin-common', {init: initAdminCommon, retire: retireAdminCommon});
registerPageFeature('admin-extensions', {init: initAdminExtensions, retire: retireAdminExtensions});
registerPageFeature('admin-configs', {init: initAdminConfigs, retire: retireAdminConfigs});
registerPageFeature('admin-emails', {init: initAdminEmails, retire: retireAdminEmails});
registerPageFeature('common-organization', {init: initCommonOrganization, retire: retireCommonOrganization});
registerPageFeature('notifications-table', {init: initNotificationsTable, retire: retireNotificationsTable});
registerPageFeature('org-team-search-repo-box', {init: initOrgTeamSearchRepoBox, retire: retireOrgTeamSearchRepoBox});
registerPageFeature('ssh-key-form-parser', {init: initSshKeyFormParser, retire: retireSshKeyFormParser});

// Component ownership: SearchUserBox and WebHookEditor are page-owned
// widgets/listeners; their retire destroys/removes exactly what init added.
registerPageFeature('comp-search-user-box', {init: initCompSearchUserBox, retire: retireCompSearchUserBox});
registerPageFeature('comp-webhook-editor', {init: initCompWebHookEditor, retire: retireCompWebHookEditor});
registerPageFeature('dashboard-repo-list', {init: initDashboardRepoList, retire: retireDashboardRepoList});
registerPageFeature('comment-content', {init: initCommentContent, retire: retireCommentContent});
registerPageFeature('context-popups', {init: initContextPopups, retire: retireContextPopups});
registerPageFeature('heatmap', {init: initHeatmap, retire: retireHeatmap});
registerPageFeature('markup-anchors', {init: initMarkupAnchors, retire: retireMarkupAnchors});
registerPageFeature('markup-content', {init: initMarkupContent, retire: retireMarkupContent});
registerPageFeature('autofocus-end', {init: initAutoFocusEnd});
registerPageFeature('copy-content', {init: initCopyContent, retire: retireCopyContent});
registerPageFeature('color-pickers', {init: initColorPickers, retire: retireColorPickers});
registerPageFeature('repo-activity-top-authors', {init: initRepoActivityTopAuthorsChart, retire: retireRepoActivityTopAuthorsChart});
registerPageFeature('repo-common', {
  init: () => { initRepoArchiveLinks(); initRepoCloneLink() },
  retire: retireRepoCommon,
});
registerPageFeature('repo-branch-button', {init: initRepoBranchButton, retire: retireRepoBranchButton});
registerPageFeature('repo-branch-tag-selector', {init: initRepoBranchTagSelector, retire: retireRepoBranchTagSelector});
registerPageFeature('repo-ellipsis', {init: initRepoEllipsisButton, retire: retireRepoEllipsisButton});
registerPageFeature('repo-migration', {init: initRepoMigration, retire: retireRepoMigration});
registerPageFeature('repo-migration-status', {init: initRepoMigrationStatusChecker, retire: retireRepoMigrationStatusChecker});
registerPageFeature('repo-project', {init: initRepoProject, retire: retireRepoProject});
registerPageFeature('repo-release', {init: initRepoRelease, retire: retireRepoRelease});
registerPageFeature('repo-release-new', {init: initRepoReleaseNew, retire: retireRepoReleaseNew});
registerPageFeature('repo-settings-git-hook', {init: initRepoSettingGitHook, retire: retireRepoSettingGitHook});
registerPageFeature('repo-settings-team-search', {init: initRepoSettingSearchTeamBox, retire: retireRepoSettingSearchTeamBox});
registerPageFeature('repo-settings-branches', {init: initRepoSettingBranches, retire: retireRepoSettingBranches});
registerPageFeature('repo-template-search', {init: initRepoTemplateSearch, retire: retireRepoTemplateSearch});
registerPageFeature('repo-topic-bar', {init: initRepoTopicBar, retire: retireRepoTopicBar});
registerPageFeature('repository', {init: initRepository, retire: retireRepository});
registerPageFeature('repo-action-view', {init: initRepositoryActionView, retire: retireRepositoryActionView});
registerPageFeature('repo-search', {init: initRepositorySearch, retire: retireRepositorySearch});
registerPageFeature('repo-contributors', {init: initRepoContributors, retire: retireRepoContributors});
registerPageFeature('repo-code-frequency', {init: initRepoCodeFrequency, retire: retireRepoCodeFrequency});
registerPageFeature('repo-recent-commits', {init: initRepoRecentCommits, retire: retireRepoRecentCommits});
registerPageFeature('repo-milestone-editor', {init: initRepoMilestoneEditor, retire: retireRepoMilestoneEditor});
registerPageFeature('commit-statuses', {init: initCommitStatuses, retire: retireCommitStatuses});
registerPageFeature('commit-notes', {init: initCommitNotes, retire: retireCommitNotes});

// Init Gitea's Fomantic settings
initGiteaFomantic();
initDirAuto();

onDomReady(() => {
  const pageData = readNativePageData(document);
  if (pageData) setPageConfig(pageData);
  initGlobalFeatures();
  enhanceNativeNodes(document);
  // All B03-B06 page features init through the seam so native transitions
  // retire and re-init them without stacking listeners or workers.
  initPageFeatures();

  initInstall();

  initCaptcha();

  initUserAuthOauth2();
  initUserAuthWebAuthn();
  initUserAuthWebAuthnRegister();
  initUserAuth();
  initExtensionPages();
  initExtensionWorkspace();

  // Deactivate CSS-only noJS usability supplements
  document.body.classList.remove('no-js');
});
