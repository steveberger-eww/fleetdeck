// node --test: parsers, cron, crypto, TOTP, input validation.
const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

process.env.DATA_DIR = fs.mkdtempSync(path.join(os.tmpdir(), 'fleetdeck-test-'));
const { parse } = require('./lib/probe');
const { parseCron, nextRuns } = require('./lib/jobs');
const sec = require('./lib/sec');
const { shq } = require('./lib/ssh');
const { validateServer } = require('./server');
const repos = require('./lib/repos');

test('probe parser', () => {
  const s = parse([
    'hostname=web1', 'os=Debian GNU/Linux 12 (bookworm)', 'kernel=6.1.0', 'cores=4', 'virt=none', 'ips=10.0.0.5 fd00::5',
    'uptime=90061.5', 'load=0.50 0.20 0.10', 'cpu1=cpu  100 0 100 800 0 0 0 0 0 0', 'cpu2=cpu  150 0 150 900 0 0 0 0 0 0',
    'mem=1000 250 400 100', 'fs=/dev/sda2 ext4 2000 500 /srv/data', 'fs=/dev/sda1 vfat 100 90 /boot', 'fs=/dev/sda2 ext4 2000 500 /', 'net=10 20', 'dio=512 1024',
    'temp=48500', 'pkg=apt', 'updates=3', 'security=1', 'reboot=1', 'failed=0', 'cockpit=inactive', 'docker=2 5', 'probe_user=root',
  ].join('\n'));
  assert.strictEqual(s.cpu, 0.5);
  assert.strictEqual(s.mem, 0.75);
  assert.strictEqual(s.swap, 0.75);
  assert.strictEqual(s.disks.length, 2); // same device collapsed
  assert.deepStrictEqual(s.disks.map((d) => d.mount).sort(), ['/', '/boot']);
  assert.strictEqual(s.disk, 0.25); // root mount, not the fullest
  assert.strictEqual(s.temp, 48.5);
  assert.deepStrictEqual(s.docker, { running: 2, total: 5 });
  assert.deepStrictEqual(s.ips, ['10.0.0.5', 'fd00::5']);
  assert.strictEqual(s.virt, '');
  assert.strictEqual(s.reboot, true);
  assert.strictEqual(parse('hostname=x').cpu, null);
});

test('cron', () => {
  const m = parseCron('*/15 9-17 * * 1-5');
  assert.ok(m(new Date(2026, 10, 24, 9, 45))); // Tuesday
  assert.ok(!m(new Date(2026, 10, 24, 9, 46)));
  assert.ok(!m(new Date(2026, 10, 29, 9, 45))); // Sunday
  const either = parseCron('0 0 1 * 1'); // day-of-month OR Monday
  assert.ok(either(new Date(2026, 10, 1, 0, 0)));
  assert.ok(either(new Date(2026, 10, 23, 0, 0)));
  assert.ok(!either(new Date(2026, 10, 24, 0, 0)));
  assert.ok(parseCron('0 0 * * 7')(new Date(2026, 10, 29, 0, 0))); // 7 = Sunday
  assert.strictEqual(nextRuns('@daily', 3, new Date(2026, 0, 1, 12, 0).getTime()).length, 3);
  for (const bad of ['* * * *', '60 * * * *', 'a * * * *', '5-1 * * * *']) assert.throws(() => parseCron(bad));
});

test('secrets at rest', () => {
  const blob = sec.encrypt('private key');
  assert.notStrictEqual(blob, sec.encrypt('private key')); // random IV
  assert.strictEqual(sec.decrypt(blob), 'private key');
  const raw = Buffer.from(blob.slice(3), 'base64');
  raw[raw.length - 1] ^= 1;
  assert.throws(() => sec.decrypt('v1:' + raw.toString('base64'))); // GCM detects tampering
});

test('passwords', () => {
  const h = sec.hashPassword('correct horse');
  assert.ok(sec.verifyPassword('correct horse', h));
  assert.ok(!sec.verifyPassword('wrong', h));
});

test('TOTP matches RFC 6238 vector', () => {
  const secret = sec.base32(Buffer.from('12345678901234567890'));
  assert.strictEqual(secret, 'GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ');
  assert.strictEqual(sec.totp(secret, 1), '287082'); // T=59s
  assert.strictEqual(sec.verifyTotp(secret, '287082', 59_000), 1);
  assert.strictEqual(sec.verifyTotp(secret, '000000', 59_000), null);
});

test('server validation blocks ssh option injection', () => {
  assert.throws(() => validateServer({ name: 'x', host: '-oProxyCommand=touch /tmp/pwn' }));
  assert.throws(() => validateServer({ name: 'x', host: 'a', user: '-oFoo' }));
  assert.throws(() => validateServer({ name: 'x', host: 'a b' }));
  assert.throws(() => validateServer({ name: 'x', host: 'a', tags: 'ok, no spaces allowed' }));
  assert.deepStrictEqual(validateServer({ name: 'x', host: 'web1.example.com', tags: 'Prod, web' }),
    { name: 'x', host: 'web1.example.com', user: 'root', port: 22, key_id: null, tags: 'prod,web', notes: '', paused: 0 });
});

test('shell quoting survives quotes and $', () => {
  const v = "it's $HOME `id`";
  assert.strictEqual(execFileSync('sh', ['-c', `printf %s ${shq(v)}`]).toString(), v);
});

test('repository validation', () => {
  const ok = (b) => repos.validateRepo({ name: 'infra', ...b });
  assert.strictEqual(ok({ url: 'https://github.com/org/infra.git' }).branch, 'main');
  assert.ok(ok({ url: 'git@github.com:org/infra.git', branch: 'release/1.2' }));
  assert.ok(ok({ url: 'ssh://git@git.example.com:2222/org/infra.git' }));
  for (const url of ['--upload-pack=touch /tmp/x', 'ext::sh -c id', 'file:///etc', '/srv/repo', 'http://plain.example/repo.git', 'https://x/ y'])
    assert.throws(() => ok({ url }), undefined, url);
  assert.throws(() => ok({ url: 'https://github.com/o/r.git', branch: '--orphan' }));
  assert.throws(() => ok({ url: 'https://github.com/o/r.git', branch: 'a/../b' }));
  assert.throws(() => ok({ url: 'git@github.com:o/r.git', auth: 'token', token: 'x' })); // token needs https
  const t = ok({ url: 'https://github.com/o/r.git', auth: 'token', username: 'x-access-token', token: 'secret' });
  assert.ok(t.token_enc.startsWith('v1:') && !t.token_enc.includes('secret'));
});

test('playbook paths stay inside the repo', () => {
  for (const p of ['site.yml', 'playbooks/web.yaml', 'a-b_c/d.yml']) assert.ok(repos.validPlaybook(p), p);
  for (const p of ['../etc/x.yml', 'a/../../x.yml', '/etc/x.yml', '-x.yml', 'site.sh', 'a b.yml']) assert.ok(!repos.validPlaybook(p), p);
});

test('ansible output is split per host with recap', () => {
  const sp = repos.hostSplitter(['web1', 'db_1']);
  sp.write('PLAY [all] ***\n\nTASK [Gathering Facts] ***\nok: [web1]\nfatal: [db_1]: UNREACHABLE! => {"msg": "x"}\n');
  sp.write('\nTASK [ping] ***\nchanged: [web1] => (item=a)\nok: [web1] => {\n    "msg": "hello"\n}\n\nPLAY RECAP ***\n');
  sp.write('web1                       : ok=2    changed=1    unreachable=0    failed=0    skipped=0\ndb_1   : ok=0    changed=0    unreachable=1    failed=0\n');
  const { perHost, recap } = sp.end();
  assert.deepStrictEqual(recap.get('web1'), { ok: 2, changed: 1, unreachable: 0, failed: 0 });
  assert.strictEqual(recap.get('db_1').unreachable, 1);
  assert.match(perHost.get('web1'), /TASK \[ping\] \*\*\*\nchanged: \[web1\]/);
  assert.doesNotMatch(perHost.get('web1'), /db_1/);
  assert.match(perHost.get('web1'), /"msg": "hello"\n}/);
});

test('git host extraction for known_hosts', () => {
  assert.strictEqual(repos.sshHost('git@github.com:org/r.git'), 'github.com');
  assert.strictEqual(repos.sshHost('ssh://git@git.example.com:2222/org/r.git'), '[git.example.com]:2222');
  assert.strictEqual(repos.sshHost('ssh://git@git.example.com/org/r.git'), 'git.example.com');
  assert.strictEqual(repos.sshHost('https://github.com/org/r.git'), null);
});

test('tag-scoped alert rules follow the server tags', () => {
  const D = require('./lib/db');
  const alerts = require('./lib/alerts');
  D.run("INSERT INTO servers (id, name, host, tags, created_at) VALUES ('s1', 'web1', 'h', 'prod,web', 0), ('s2', 'db1', 'h', 'db', 0)");
  D.run("INSERT INTO alert_rules (id, metric, threshold, tag, created_at) VALUES ('r1', 'updates', 0, 'web', 0)");
  const srv = (id) => D.get('SELECT * FROM servers WHERE id = ?', id);
  const firing = () => D.all('SELECT server_id FROM alert_state').map((r) => r.server_id);
  alerts.evaluate(srv('s1'), { updates: 3 }, null);
  alerts.evaluate(srv('s2'), { updates: 3 }, null);
  assert.deepStrictEqual(firing(), ['s1']);
  D.run("UPDATE servers SET tags = 'prod' WHERE id = 's1'"); // tag removed -> alert dropped
  alerts.evaluate(srv('s1'), { updates: 3 }, null);
  assert.deepStrictEqual(firing(), []);
  assert.throws(() => alerts.validateRule({ metric: 'cpu', tag: 'web', server_id: 's1' }));
});
