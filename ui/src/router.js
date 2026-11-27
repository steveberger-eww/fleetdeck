// Hash routes (#/servers/x/logs?unit=nginx): invite links and the backend's redirects use them.
import { createRouter, createWebHashHistory } from 'vue-router';
import Overview from './pages/Overview.vue';

const lazy = {
  server: () => import('./pages/server/ServerPage.vue'),
  jobs: () => import('./pages/jobs/JobsPage.vue'),
  job: () => import('./pages/jobs/JobPage.vue'),
  scripts: () => import('./pages/scripts/ScriptsPage.vue'),
  schedules: () => import('./pages/scripts/SchedulesPage.vue'),
  alerts: () => import('./pages/AlertsPage.vue'),
  docker: () => import('./pages/DockerPage.vue'),
  keys: () => import('./pages/KeysPage.vue'),
  variables: () => import('./pages/VariablesPage.vue'),
  repos: () => import('./pages/ReposPage.vue'),
  audit: () => import('./pages/AuditPage.vue'),
  settings: () => import('./pages/settings/SettingsPage.vue'),
};

// meta.role: the lowest role that may open the page (the shell shows "not available" otherwise).
export const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    { path: '/', component: Overview },
    { path: '/servers/:id/:tab?', component: lazy.server, props: (r) => ({ id: r.params.id, tab: r.params.tab || 'overview' }) },
    { path: '/jobs', component: lazy.jobs },
    { path: '/jobs/:id', component: lazy.job, props: true },
    { path: '/scripts', component: lazy.scripts },
    { path: '/schedules', component: lazy.schedules, meta: { role: 'admin' } },
    { path: '/alerts', component: lazy.alerts, meta: { role: 'admin' } },
    { path: '/docker', component: lazy.docker },
    { path: '/keys', component: lazy.keys, meta: { role: 'owner' } },
    { path: '/variables', component: lazy.variables, meta: { role: 'admin' } },
    { path: '/repos', component: lazy.repos, meta: { role: 'admin' } },
    { path: '/audit', component: lazy.audit, meta: { role: 'admin' } },
    { path: '/settings/:sub(.*)*', component: lazy.settings, props: (r) => ({ sub: [r.params.sub || []].flat().join('/') }) },
    { path: '/invite/:token', component: { render: () => null } }, // handled by App.vue outside the shell
    { path: '/:rest(.*)*', component: () => import('./pages/NotFound.vue') },
  ],
  // Filters change only the query: keep the scroll position. New pages start at the top.
  scrollBehavior: (to, from) => (to.path === from.path ? false : { top: 0 }),
});
