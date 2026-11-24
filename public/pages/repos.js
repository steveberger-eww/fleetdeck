import { html, useState, api, useLoad, useDirty, toast, confirmBox, Btn, Empty, Field, Modal, Footer, Loading, ErrorNote, ago, when } from '../ui.js';

export function ReposPage() {
  const repos = useLoad(() => api('/api/repos'), [], { poll: 15_000 });
  const tools = useLoad(() => api('/api/tools'));
  const [edit, setEdit] = useState(null);
  const sync = async (r) => { await api(`/api/repos/${r.id}/sync`, { method: 'POST' }); toast(`${r.name} synced`, 'ok'); repos.reload(true); };
  const trustKey = async (r) => {
    if (!await confirmBox({ title: 'Trust the Git server\'s new host key?', body: 'Only do this if you know why the key changed (server reinstall, new Git host). A changed key can also mean someone is intercepting the connection.', confirm: 'Trust new key', danger: true })) return;
    await api(`/api/repos/${r.id}/forget-hostkey`, { method: 'POST' });
    toast('New host key trusted and repository synced', 'ok');
    repos.reload(true);
  };
  const del = async (r) => {
    if (!await confirmBox({ title: `Delete repository "${r.name}"?`, body: 'Removes the local checkout. The remote repository is not touched.', confirm: 'Delete repository', danger: true })) return;
    await api(`/api/repos/${r.id}`, { method: 'DELETE' });
    repos.reload(true);
  };
  const t = tools.data;
  return html`<div class="page">
    <div class="page-h"><div><h1>Repositories</h1><p class="sub">Git repositories with Ansible playbooks. Fleetdeck pulls the branch before every run.</p></div>
      <div class="right"><${Btn} kind="primary" icon="plus" onClick=${() => setEdit({})}>Add repository</${Btn}></div></div>
    ${t && (!t.git || !t.ansible) && repos.data?.length > 0 && html`<div class="alert-bar warn"><span class="grow">
      ${!t.git && html`<b>git</b> is not installed on the Fleetdeck host. `}${!t.ansible && html`<b>ansible-playbook</b> is not installed on the Fleetdeck host, so playbooks can't run. Install it with <span class="mono">pipx install ansible-core</span> or your package manager (the Docker image includes it).`}</span></div>`}
    ${repos.loading ? html`<${Loading} />` : repos.error ? html`<${ErrorNote} error=${repos.error} retry=${repos.reload} />`
      : !repos.data.length ? html`<section class="panel"><${Empty} title="No repositories yet" action=${html`<${Btn} icon="plus" onClick=${() => setEdit({})}>Add repository</${Btn}>`}>
          Connect a Git repository with your playbooks, then create an Ansible script that points at a playbook in it.
          ${t && (!t.git || !t.ansible) && html`<br /><br /><span class="ink2">First install ${[!t.git && 'git', !t.ansible && 'ansible-playbook'].filter(Boolean).join(' and ')} on the Fleetdeck host (<span class="mono">pipx install ansible-core</span>; the Docker image includes both).</span>`}</${Empty}></section>`
      : repos.data.map((r) => html`<section class="panel">
          <div class="panel-h"><h2>${r.name}</h2><span class="tag">${r.branch}</span>
            ${r.last_error ? html`<span class="chip bad">Sync failed</span>` : r.last_commit ? html`<span class="chip ok">Synced</span>` : html`<span class="chip">Not synced</span>`}
            <div class="row right"><${Btn} size="sm" icon="refresh" onClick=${() => sync(r)}>Sync now</${Btn}>
              <${Btn} size="sm" kind="ghost" icon="edit" title="Edit" onClick=${() => setEdit(r)} /><${Btn} size="sm" kind="quiet-danger" icon="trash" title=${r.used_by.length ? 'Used by scripts' : 'Delete'} disabled=${r.used_by.length > 0} onClick=${() => del(r)} /></div></div>
          <div class="panel-b stack sm">
            <span class="mono small wrap-any">${r.url}</span>
            ${r.last_error && html`<div class="row"><p class="err grow">${r.last_error}</p>
              ${r.last_error.startsWith('Host key verification failed') && html`<${Btn} size="sm" kind="danger" onClick=${() => trustKey(r)}>Trust new host key</${Btn}>`}</div>`}
            <dl class="kv">
              <dt>Last commit</dt><dd>${r.last_commit ? html`<span class="mono">${r.last_commit.split(' ')[0]}</span> ${r.last_commit.split(' ').slice(1).join(' ')}` : '–'}</dd>
              <dt>Last sync</dt><dd title=${when(r.last_sync)}>${ago(r.last_sync)}</dd>
              <dt>Access</dt><dd>${{ none: 'Public, or the ssh-agent of the Fleetdeck host', ssh: 'SSH key', token: `Access token as ${r.username}` }[r.auth]}</dd>
              <dt>Used by</dt><dd>${r.used_by.length ? r.used_by.map((s) => s.name).join(', ') : 'No scripts yet'}</dd>
            </dl></div></section>`)}
    ${edit && html`<${RepoDialog} repo=${edit} onClose=${() => setEdit(null)} onSaved=${() => { setEdit(null); repos.reload(true); }} />`}
  </div>`;
}

function RepoDialog({ repo, onClose, onSaved }) {
  const keys = useLoad(() => api('/api/keys'));
  const [v, setV] = useState({ name: '', url: '', branch: 'main', auth: 'none', key_id: '', username: 'git', token: '', ...repo });
  const dirty = useDirty(v);
  const [err, setErr] = useState('');
  const set = (k) => (e) => setV({ ...v, [k]: e.target.value });
  const save = async () => {
    setErr('');
    try {
      await api(repo.id ? `/api/repos/${repo.id}` : '/api/repos', { method: repo.id ? 'PUT' : 'POST', body: v });
      toast(repo.id ? 'Repository saved' : 'Repository added, cloning now', 'ok');
      onSaved();
    } catch (e) { setErr(e.message); }
  };
  const key = keys.data?.find((k) => k.id === v.key_id);
  return html`<${Modal} title=${repo.id ? `Edit ${repo.name}` : 'Add repository'} onClose=${onClose} dirty=${dirty}
    footer=${html`<${Footer} onClose=${onClose} onSave=${save} label=${repo.id ? 'Save repository' : 'Add repository'} disabled=${!v.url} />`}>
    <div class="cols"><${Field} label="Name"><input value=${v.name} onInput=${set('name')} placeholder="infra" autofocus /></${Field}>
      <${Field} label="Branch"><input class="mono" value=${v.branch} onInput=${set('branch')} /></${Field}></div>
    <${Field} label="URL" hint="https://github.com/org/infra.git or git@github.com:org/infra.git"><input class="mono" value=${v.url} onInput=${set('url')} /></${Field}>
    <${Field} label="Access">
      <select value=${v.auth} onChange=${set('auth')}>
        <option value="none">Public repository (or the Fleetdeck host's ssh-agent)</option>
        <option value="ssh">SSH deploy key</option>
        <option value="token">HTTPS access token</option>
      </select></${Field}>
    ${v.auth === 'ssh' && html`<${Field} label="SSH key" hint="Add its public key as a read-only deploy key on your Git host.">
        <select value=${v.key_id} onChange=${set('key_id')}><option value="">Choose a key</option>${(keys.data || []).map((k) => html`<option value=${k.id}>${k.name}</option>`)}</select></${Field}>
      ${key && html`<span class="mono small ink2 wrap-any">${key.public_key}</span>`}`}
    ${v.auth === 'token' && html`<div class="cols">
      <${Field} label="Username" hint="GitHub: x-access-token, GitLab: oauth2"><input class="mono" value=${v.username} onInput=${set('username')} /></${Field}>
      <${Field} label="Access token" hint=${repo.has_token ? 'Saved. Leave empty to keep it.' : 'Read-only scope is enough. Stored encrypted.'}><input type="password" autocomplete="off" value=${v.token} onInput=${set('token')} /></${Field}></div>`}
    <p class="small ink2">Playbooks run on the Fleetdeck host and can execute commands there too (local tasks, lookups). Only add repositories you control.</p>
    ${err && html`<p class="err" role="alert">${err}</p>`}
  </${Modal}>`;
}
