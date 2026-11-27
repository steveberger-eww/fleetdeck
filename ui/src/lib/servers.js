// Server form helpers shared by "Add server" and a server's Settings tab.
export const serverBody = (v) => ({
  name: v.name, host: v.host, port: Number(v.port), user: v.user, tags: v.tags, notes: v.notes, key_id: v.key_id || null,
  ...(v.sudo_password ? { sudo_password: v.sudo_password } : {}),
});
// What a connection test's `privilege` means for the user.
export const PRIV_TEXT = {
  root: 'Logged in as root.', sudo: 'Passwordless sudo works.', password: 'sudo works with the stored password.',
  'wrong-password': 'The server rejected the sudo password. Fix it in the server settings.',
  none: 'No sudo: monitoring works, updates and admin actions will fail. Add a sudo password in the server settings or allow passwordless sudo.',
};
