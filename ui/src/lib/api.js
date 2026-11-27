// JSON API client and WebAuthn helpers.
export async function api(path, { method = 'GET', body } = {}) {
  const r = await fetch(path, {
    method,
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : {},
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  const data = (r.headers.get('content-type') || '').includes('json') ? await r.json() : null;
  if (r.status === 401 && !path.startsWith('/api/auth')) dispatchEvent(new Event('fd-signed-out'));
  if (!r.ok) throw Object.assign(new Error(data?.error || `Request failed (${r.status})`), { status: r.status });
  return data;
}

// Passkeys: JSON from the server <-> the browser's ArrayBuffers.
const unb64 = (s) => Uint8Array.from(atob(s.replace(/-/g, '+').replace(/_/g, '/')), (c) => c.charCodeAt(0));
const b64 = (buf) => btoa(String.fromCharCode(...new Uint8Array(buf))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
export const passkeysSupported = () => !!window.PublicKeyCredential && window.isSecureContext;
const credJSON = (c) => ({
  id: c.id, rawId: b64(c.rawId), type: c.type, clientExtensionResults: c.getClientExtensionResults?.() || {}, authenticatorAttachment: c.authenticatorAttachment,
  response: Object.fromEntries(['clientDataJSON', 'attestationObject', 'authenticatorData', 'signature', 'userHandle']
    .filter((k) => c.response[k]).map((k) => [k, b64(c.response[k])]).concat(c.response.getTransports ? [['transports', c.response.getTransports()]] : [])),
});
export async function createPasskey({ publicKey: o }) {
  const publicKey = { ...o, challenge: unb64(o.challenge), user: { ...o.user, id: unb64(o.user.id) }, excludeCredentials: (o.excludeCredentials || []).map((x) => ({ ...x, id: unb64(x.id) })) };
  return credJSON(await navigator.credentials.create({ publicKey }));
}
export async function getPasskey({ publicKey: o }) {
  const publicKey = { ...o, challenge: unb64(o.challenge), allowCredentials: (o.allowCredentials || []).map((x) => ({ ...x, id: unb64(x.id) })) };
  return credJSON(await navigator.credentials.get({ publicKey }));
}
