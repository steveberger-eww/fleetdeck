import { html, useState, api, useLoad, toast, confirmBox, Btn, Empty, Field, Modal, Loading, ErrorNote, Copy, when } from '../ui.js';

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
    ${dialog && html`<${KeyDialog} mode=${dialog} onClose=${() => setDialog(null)} onSaved=${() => { setDialog(null); keys.reload(true); }} />`}
  </div>`;
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
