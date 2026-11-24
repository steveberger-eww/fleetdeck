import { html, useState, useMemo, api, useLoad, toast, confirmBox, Btn, Empty, Field, Modal, Loading, ErrorNote, Copy, when } from '../ui.js';

export function KeysPage() {
  const keys = useLoad(() => api('/api/keys'));
  const [dialog, setDialog] = useState(null); // 'generate' | 'import'
  const del = async (k) => {
    if (!await confirmBox({ title: `Delete key "${k.name}"?`, body: 'The private key is destroyed. Servers that still authorize its public key keep it in authorized_keys until you remove it there.', confirm: 'Delete key', danger: true })) return;
    await api(`/api/keys/${k.id}`, { method: 'DELETE' });
    toast('Key deleted');
    keys.reload(true);
  };
  return html`<div class="page">
    <div class="page-h"><div><h1>SSH keys</h1><p class="sub">Private keys are encrypted at rest and only decrypted in memory to open connections. They are never shown again.</p></div>
      <div class="row right"><${Btn} onClick=${() => setDialog('import')}>Import key</${Btn}><${Btn} kind="primary" icon="plus" onClick=${() => setDialog('generate')}>Generate key</${Btn}></div></div>
    ${keys.loading ? html`<${Loading} />` : keys.error ? html`<${ErrorNote} error=${keys.error} retry=${keys.reload} />`
      : !keys.data.length ? html`<section class="panel"><${Empty} title="No keys stored">Generate an ed25519 key here, or import one you already use. Servers without a key fall back to the ssh-agent of the Fleetdeck host.</${Empty}></section>`
      : keys.data.map((k) => html`<section class="panel">
          <div class="panel-h"><h2>${k.name}</h2><span class="small muted">added ${when(k.created_at)}${k.has_passphrase ? ', passphrase protected' : ''}</span>
            <${Btn} size="sm" kind="ghost" icon="trash" class="right" title="Delete key" disabled=${k.used_by.length > 0} onClick=${() => del(k)} /></div>
          <div class="panel-b stack" style=${{ gap: '10px' }}>
            <${Copy} text=${k.public_key} label="Copy public key" />
            <p class="small ink2">${k.used_by.length ? html`Used by ${k.used_by.map((s, i) => html`${i ? ', ' : ''}<a href=${`#/servers/${s.id}`}>${s.name}</a>`)}` : 'Not assigned to any server.'}</p>
          </div></section>`)}
    <${AuthorizedPanel} />
    ${dialog && html`<${KeyDialog} mode=${dialog} onClose=${() => setDialog(null)} onSaved=${() => { setDialog(null); keys.reload(true); }} />`}
  </div>`;
}

// Every key in every authorized_keys file, grouped by fingerprint: "where can this key still log in?"
function AuthorizedPanel() {
  const [scan, setScan] = useState(null);
  const [busy, setBusy] = useState(false);
  const [q, setQ] = useState('');
  const load = async () => { setBusy(true); try { setScan(await api('/api/accounts')); } catch (e) { toast(e.message, 'bad'); } finally { setBusy(false); } };
  const groups = useMemo(() => {
    const m = new Map();
    for (const s of scan?.servers || []) for (const u of s.users || []) for (const k of u.keys) {
      const g = m.get(k.fp) || m.set(k.fp, { ...k, comments: new Set(), where: [] }).get(k.fp);
      if (k.comment) g.comments.add(k.comment);
      g.where.push({ server_id: s.server_id, server_name: s.server_name, user: u.name, own: scan.own[s.server_id] === k.fp && u.name === s.login_user,
        maybeOwn: !scan.own[s.server_id] && u.name === s.login_user });
    }
    const n = q.toLowerCase();
    return [...m.values()].map((g) => ({ ...g, known: scan.keys.find((k) => k.fp === g.fp) }))
      .filter((g) => !n || `${g.fp} ${[...g.comments].join(' ')} ${g.known?.name || ''} ${g.where.map((w) => `${w.user}@${w.server_name}`).join(' ')}`.toLowerCase().includes(n))
      .sort((a, b) => b.where.length - a.where.length);
  }, [scan, q]);
  const revoke = async (g, list) => {
    const names = list.map((w) => `${w.user}@${w.server_name}`).join(', ');
    const risky = list.some((w) => w.maybeOwn);
    if (!await confirmBox({ title: `Remove this key from ${list.length === 1 ? names : `${list.length} accounts`}?`, danger: true, confirm: 'Remove key',
      body: `${g.type} ${g.fp}${g.comments.size ? ` (${[...g.comments].join(', ')})` : ''} is deleted from ${names}.${risky ? ' Careful: Fleetdeck logs in to some of these servers with the ssh-agent or a ~/.ssh key, so it can\'t tell whether this is that key.' : ''}` })) return;
    const failed = [];
    for (const w of list) {
      try { await api(`/api/servers/${w.server_id}/authorized-keys/revoke`, { method: 'POST', body: { user: w.user, blob: g.blob } }); } catch (e) { failed.push(`${w.user}@${w.server_name}: ${e.message}`); }
    }
    failed.length ? toast(`Not removed from ${failed.join('; ')}`, 'bad') : toast('Key removed', 'ok');
    await load();
  };
  const errors = (scan?.servers || []).filter((s) => s.error);
  return html`<section class="panel">
    <div class="panel-h"><h2>Authorized on servers</h2><span class="small muted">Every key in the users' authorized_keys files, across the fleet.</span>
      ${scan && html`<input type="search" placeholder="Fingerprint, comment, user, server" value=${q} onInput=${(e) => setQ(e.target.value)} aria-label="Filter keys" />`}
      <${Btn} size="sm" class="right" icon="refresh" disabled=${busy} onClick=${load}>${busy ? 'Scanning…' : scan ? 'Scan again' : 'Scan servers'}</${Btn}></div>
    ${!scan ? html`<${Empty} title="Find every key that can log in">Fleetdeck reads authorized_keys on all active servers (needs sudo) and shows where each key still has access, so old laptops and ex-colleagues' keys stand out.</${Empty}>`
      : html`${errors.length > 0 && html`<div class="panel-b"><${ErrorNote} error=${`Couldn't read: ${errors.map((s) => `${s.server_name} (${s.error})`).join('; ')}`} /></div>`}
      ${!groups.length ? html`<${Empty} title=${q ? 'No keys match' : 'No authorized keys found'} />` : html`<div class="table-wrap"><table class="t">
        <thead><tr><th>Key</th><th>Can log in as</th><th class="act"></th></tr></thead>
        <tbody>${groups.map((g) => html`<tr>
          <td><div class="row nw">${g.known ? html`<b>${g.known.name}</b><span class="tag">Fleetdeck key</span>` : html`<b>${[...g.comments][0] || 'no comment'}</b>`}</div>
            <div class="mono small muted">${g.type.replace(/^ssh-/, '')} ${g.fp}</div>
            ${g.options && html`<div class="mono small ink2 trunc" style=${{ maxWidth: '360px' }} title=${g.options}>${g.options}</div>`}</td>
          <td><div class="row">${g.where.map((w) => html`<span class="row nw"><a class="tag" href=${`#/servers/${w.server_id}`}>${w.user}@${w.server_name}</a>${w.own
            ? html`<span class="small muted" title="Fleetdeck logs in with this key">(Fleetdeck)</span>`
            : html`<${Btn} size="sm" kind="ghost" icon="x" title=${`Remove from ${w.user}@${w.server_name}`} onClick=${() => revoke(g, [w])} />`}</span>`)}</div></td>
          <td class="act">${g.where.filter((w) => !w.own).length > 1 && html`<${Btn} size="sm" kind="danger" onClick=${() => revoke(g, g.where.filter((w) => !w.own))}>Remove everywhere</${Btn}>`}</td>
        </tr>`)}</tbody></table></div>`}`}
  </section>`;
}

function KeyDialog({ mode, onClose, onSaved }) {
  const [v, setV] = useState({ name: '', private_key: '', passphrase: '' });
  const [err, setErr] = useState('');
  const save = async () => {
    setErr('');
    try {
      await api('/api/keys', { method: 'POST', body: mode === 'generate' ? { name: v.name, generate: true } : v });
      toast(mode === 'generate' ? 'Key generated' : 'Key imported', 'ok');
      onSaved();
    } catch (e) { setErr(e.message); }
  };
  return html`<${Modal} title=${mode === 'generate' ? 'Generate SSH key' : 'Import SSH key'} onClose=${onClose}
    footer=${html`<button class="btn" onClick=${onClose}>Cancel</button><${Btn} kind="primary" onClick=${save}>${mode === 'generate' ? 'Generate key' : 'Import key'}</${Btn}>`}>
    <${Field} label="Name"><input value=${v.name} onInput=${(e) => setV({ ...v, name: e.target.value })} placeholder="homelab" autofocus /></${Field}>
    ${mode === 'generate' ? html`<p class="small ink2">Creates an ed25519 key pair. Copy the public key to each server's <span class="mono">~/.ssh/authorized_keys</span>, or use the "Authorize SSH public key" script.</p>`
      : html`<${Field} label="Private key" hint="OpenSSH or PEM format."><textarea spellcheck="false" placeholder="-----BEGIN OPENSSH PRIVATE KEY-----" value=${v.private_key} onInput=${(e) => setV({ ...v, private_key: e.target.value })} /></${Field}>
        <${Field} label="Passphrase" hint="Only if the key has one. Stored encrypted."><input type="password" autocomplete="off" value=${v.passphrase} onInput=${(e) => setV({ ...v, passphrase: e.target.value })} /></${Field}>`}
    ${err && html`<p class="err">${err}</p>`}
  </${Modal}>`;
}

// ---------- shared variables ----------
export function VariablesPage() {
  const vars = useLoad(() => api('/api/variables'));
  const [edit, setEdit] = useState(null);
  const del = async (v) => {
    if (!await confirmBox({ title: `Delete ${v.name}?`, confirm: 'Delete variable', danger: true })) return;
    await api(`/api/variables/${v.id}`, { method: 'DELETE' });
    toast('Variable deleted');
    vars.reload(true);
  };
  return html`<div class="page">
    <div class="page-h"><div><h1>Variables</h1><p class="sub">Shared values for scripts and playbooks, such as API tokens, passwords or domains. Stored encrypted. A script only gets the variables you tick in it, as environment variables (Ansible: extra vars). Secret values are never shown again and are masked in job output.</p></div>
      <div class="right"><${Btn} kind="primary" icon="plus" onClick=${() => setEdit({})}>New variable</${Btn}></div></div>
    <section class="panel">
      ${vars.loading ? html`<${Loading} />` : vars.error ? html`<${ErrorNote} error=${vars.error} retry=${vars.reload} />`
        : !vars.data.length ? html`<${Empty} title="No variables yet">Add a token or password once and use it from any script instead of pasting it into each one.</${Empty}>`
        : html`<div class="table-wrap"><table class="t"><thead><tr><th>Name</th><th>Value</th><th>Used by</th><th class="act"></th></tr></thead>
          <tbody>${vars.data.map((v) => html`<tr>
            <td><span class="mono"><b>${v.name}</b></span>${v.secret && html` <span class="tag">secret</span>`}<div class="small muted">${v.description}</div></td>
            <td class="mono small ink2 trunc" style=${{ maxWidth: '260px' }}>${v.secret ? '••••••' : v.value}</td>
            <td class="small ink2">${v.used_by.map((s) => s.name).join(', ') || '–'}</td>
            <td class="act"><div class="row nw"><${Btn} size="sm" kind="ghost" icon="edit" title="Edit" onClick=${() => setEdit(v)} />
              <${Btn} size="sm" kind="ghost" icon="trash" title=${v.used_by.length ? 'Used by scripts' : 'Delete'} disabled=${v.used_by.length > 0} onClick=${() => del(v)} /></div></td></tr>`)}</tbody></table></div>`}
    </section>
    ${edit && html`<${VariableDialog} variable=${edit} onClose=${() => setEdit(null)} onSaved=${() => { setEdit(null); vars.reload(true); }} />`}
  </div>`;
}

function VariableDialog({ variable, onClose, onSaved }) {
  const [v, setV] = useState({ name: '', description: '', secret: true, ...variable, value: variable.value || '' });
  const [err, setErr] = useState('');
  const save = async () => {
    setErr('');
    try {
      await api(variable.id ? `/api/variables/${variable.id}` : '/api/variables', { method: variable.id ? 'PUT' : 'POST', body: v });
      toast('Variable saved', 'ok');
      onSaved();
    } catch (e) { setErr(e.message); }
  };
  return html`<${Modal} title=${variable.id ? `Edit ${variable.name}` : 'New variable'} onClose=${onClose}
    footer=${html`<button class="btn" onClick=${onClose}>Cancel</button><${Btn} kind="primary" onClick=${save}>Save variable</${Btn}>`}>
    <${Field} label="Name" hint=${variable.id ? 'Scripts refer to it by name, so it can\'t be renamed.' : 'Becomes $NAME in shell scripts and {{ NAME }} in playbooks.'}>
      <input class="mono" value=${v.name} disabled=${!!variable.id} autofocus=${!variable.id} placeholder="CLOUDFLARE_TOKEN" onInput=${(e) => setV({ ...v, name: e.target.value })} /></${Field}>
    <${Field} label="Value" hint=${variable.id && variable.secret ? 'Saved. Leave empty to keep it.' : null}>
      <textarea class="mono" spellcheck="false" style=${{ minHeight: '70px' }} value=${v.value} onInput=${(e) => setV({ ...v, value: e.target.value })} /></${Field}>
    <${Field} label="Description"><input value=${v.description} onInput=${(e) => setV({ ...v, description: e.target.value })} /></${Field}>
    <label class="check"><input type="checkbox" checked=${v.secret} onChange=${(e) => setV({ ...v, secret: e.target.checked })} />Secret: never show the value again, and mask it in job output</label>
    ${err && html`<p class="err">${err}</p>`}
  </${Modal}>`;
}
