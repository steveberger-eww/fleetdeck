// Navigation: one source for the sidebar, the command palette, breadcrumbs and role guards.
import { can, isSuper, me } from './store.js';

// [group label | null, [[label, path, icon, lowest role that may open it]]]
export const NAV = [
  [null, [['Overview', '/', 'overview'], ['Docker', '/docker', 'box'], ['Alerts', '/alerts', 'bell', 'admin']]],
  ['Automation', [['Jobs', '/jobs', 'jobs'], ['Scripts', '/scripts', 'scripts'], ['Schedules', '/schedules', 'clock', 'admin'], ['Repositories', '/repos', 'git', 'admin'], ['Variables', '/variables', 'vars', 'admin']]],
  ['Admin', [['SSH keys', '/keys', 'key', 'owner'], ['Audit log', '/audit', 'audit', 'admin'], ['Settings', '/settings', 'gear']]],
];
// The sidebar for the current role (empty groups dropped).
export const navFor = () => NAV.map(([g, items]) => [g, items.filter(([, , , r]) => !r || can(r))]).filter(([, items]) => items.length);

// Server page tabs: [key (URL segment), label]. Overview has no segment.
export const SERVER_TABS = [['overview', 'Overview'], ['updates', 'Updates'], ['services', 'Services'], ['logs', 'Logs'], ['processes', 'Processes'], ['containers', 'Containers'], ['network', 'Network'], ['terminal', 'Terminal'], ['jobs', 'Jobs'], ['settings', 'Settings']];
export const SERVER_TAB_LABEL = Object.fromEntries(SERVER_TABS);

// Settings: [path under /settings, label, group]. Group decides who sees it: account = everyone, org = admins, instance = super admins.
export const SETTINGS_SECTIONS = [
  ['', 'Profile', 'account'], ['security', 'Sign-in & security', 'account'], ['preferences', 'Preferences', 'account'],
  ['org', 'General', 'org'], ['org/people', 'People', 'org'], ['org/sso', 'Single sign-on', 'org'],
  ['instance', 'General', 'instance'], ['instance/orgs', 'Organizations', 'instance'], ['instance/accounts', 'Accounts', 'instance'], ['sso', 'Single sign-on', 'instance'],
];
export const SETTINGS_GROUPS = [['account', 'Your account'], ['org', 'Organization'], ['instance', 'Instance']];
export const settingsVisible = (g) => g === 'account' || (g === 'org' ? can('admin') : isSuper());
// A personal organization has nobody to invite and no single sign-on of its own.
const PERSONAL_HIDES = ['org/people', 'org/sso'];
export const settingsSections = () => SETTINGS_SECTIONS.filter(([p, , g]) => settingsVisible(g) && !(me.value?.tenant?.personal && PERSONAL_HIDES.includes(p)));
// For the palette and breadcrumbs: [label, path, group label] of every section you can open.
export const settingsPages = () => settingsSections()
  .map(([p, l, g]) => [l, `/settings${p ? `/${p}` : ''}`, SETTINGS_GROUPS.find(([k]) => k === g)[1]]);
