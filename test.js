// node --test: parsers, cron, crypto, TOTP, input validation.
const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { execFileSync } = require('node:child_process');

process.env.DATA_DIR = fs.mkdtempSync(path.join(os.tmpdir(), 'fleetdeck-test-'));
const { parse } = require('./lib/probe');
const { parseCron, nextRuns, bootedAfter } = require('./lib/jobs');
const sec = require('./lib/sec');
const { shq, explain } = require('./lib/ssh');
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
  for (const junk of ['ééé', '12 345', '', undefined, '2870821']) assert.strictEqual(sec.verifyTotp(secret, junk, 59_000), null); // never throws
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

test('reboot detection and readable ssh errors', () => {
  const now = 1_000_000_000;
  assert.ok(bootedAfter('42.10 80.00\n', now - 300_000, now)); // up 42s, run started 5 min ago
  assert.ok(!bootedAfter('9000.5 1.0', now - 300_000, now));
  assert.ok(!bootedAfter('', now - 300_000, now));
  const srv = { host: 'web1', port: 22, user: 'deploy' };
  const e = explain(Object.assign(new Error('connect ECONNRESET'), { code: 'ECONNRESET' }), srv);
  assert.match(e.message, /dropped the connection.*ECONNRESET/); // exec's retry still matches
  assert.match(explain(new Error('All configured authentication methods failed'), srv).message, /~deploy\/\.ssh\/authorized_keys/);
  assert.strictEqual(explain(new Error('weird'), srv).message, 'weird');
});

test('admin parsers: services to restart, timers', () => {
  const admin = require('./lib/admin');
  assert.deepStrictEqual(admin.parseStale('nginx.service\ndbus.service\n\nbad name;\n'),
    [{ unit: 'nginx.service', careful: false }, { unit: 'dbus.service', careful: true }]);
  const t = admin.parseTimers([
    'Id=apt-daily.timer', 'Description=Daily apt', 'Unit=apt-daily.service', 'ActiveState=active', 'UnitFileState=enabled',
    'NextElapseUSecRealtime=@1764000000', 'LastTriggerUSec=n/a', '',
    'Id=fstrim.timer', 'Unit=fstrim.service', 'ActiveState=active', 'UnitFileState=enabled', 'NextElapseUSecRealtime=', 'LastTriggerUSec=Mon 2025-11-24 00:00:00 UTC',
    '#svc', 'Id=apt-daily.service', 'Result=success', 'ActiveState=inactive', '', 'Id=fstrim.service', 'Result=exit-code', 'ActiveState=activating',
  ].join('\n'));
  assert.deepStrictEqual(t.map((x) => [x.unit, x.next, x.last, x.result, x.running]), [
    ['apt-daily.timer', 1764000000000, null, 'success', false],
    ['fstrim.timer', null, Date.UTC(2025, 10, 24), 'exit-code', true], // pre-247 systemd: text dates
  ]);
});

test('probe: automatic updates and S.M.A.R.T.', () => {
  const sata = { smart_status: { passed: true }, model_name: 'WDC WD40', temperature: { current: 34 }, power_on_time: { hours: 20000 },
    ata_smart_attributes: { table: [{ id: 5, raw: { value: 8 } }, { id: 197, raw: { value: 0 } }, { id: 198, raw: { value: 0 } }] } };
  const nvme = { smart_status: { passed: true }, model_name: 'Samsung 980', nvme_smart_health_information_log: { percentage_used: 3, media_errors: 0, critical_warning: 0 } };
  const pending = { ...sata, ata_smart_attributes: { table: [{ id: 197, raw: { value: 2 } }] } };
  const s = parse(['auto_updates=on', 'smart=ok', `smartdev=/dev/sda ${JSON.stringify(sata)}`, `smartdev=/dev/nvme0 ${JSON.stringify(nvme)}`,
    `smartdev=/dev/sdb ${JSON.stringify(pending)}`, 'smartdev=/dev/sdc {"smartctl":{"exit_status":2}}', 'smartdev=/dev/sdd {broken'].join('\n'));
  assert.strictEqual(s.autoUpdates, true);
  assert.strictEqual(parse('auto_updates=off').autoUpdates, false);
  assert.strictEqual(parse('x=1').autoUpdates, null);
  assert.deepStrictEqual(s.smart.disks.map((d) => [d.dev, d.problem]), [['/dev/sda', false], ['/dev/nvme0', false], ['/dev/sdb', true]]); // standby + junk skipped
  assert.strictEqual(s.smart.disks[0].realloc, 8);
  assert.strictEqual(s.smart.disks[1].wear, 3);
  assert.strictEqual(parse('x=1').smart, null); // not checked this round
  assert.deepStrictEqual(parse('smart=none').smart, { status: 'none', disks: [] });
  const D = require('./lib/db');
  const alerts = require('./lib/alerts');
  D.run("INSERT INTO servers (id, name, host, created_at) VALUES ('s9', 'nas', 'h', 0)");
  D.run("INSERT INTO alert_rules (id, metric, threshold, server_id, created_at) VALUES ('r9', 'smart', 0, 's9', 0)");
  alerts.evaluate(D.get("SELECT * FROM servers WHERE id = 's9'"), s, null);
  assert.match(D.get("SELECT message FROM alert_state WHERE rule_id = 'r9'").message, /S\.M\.A\.R\.T\. problems is 1/);
});

test('probe: containers, interfaces, network alert', () => {
  const ps = (name, state, status) => `ctr=${JSON.stringify({ Names: name, Image: 'img', State: state, Status: status })}`;
  const s = parse(['docker=2 3', ps('web', 'running', 'Up 2 hours (healthy)'), ps('db', 'running', 'Up 1 hour (unhealthy)'), ps('job', 'restarting', 'Restarting (1) 3 seconds ago'),
    `cst=${JSON.stringify({ Name: 'web', CPUPerc: '150.25%', MemUsage: '12.5MiB / 1.9GiB' })}`, `cst=${JSON.stringify({ Name: 'db', CPUPerc: '0.00%', MemUsage: '1.5GB / 4GB' })}`, 'cst={broken',
    'if=eth0 5000 7000', 'if=docker0 300 400', 'net=5000 7000'].join('\n'));
  assert.deepStrictEqual(s.containers.map((c) => [c.name, c.health, c.cpu, c.mem]),
    [['web', 'healthy', 150.25, 13107200], ['db', 'unhealthy', 0, 1.5e9], ['job', null, null, null]]);
  assert.strictEqual(s.unhealthy, 2); // unhealthy + restarting
  assert.strictEqual(parse('x=1').containers, null);
  assert.deepStrictEqual(s.ifaces.map((i) => [i.name, i.rxTotal, i.txTotal]), [['eth0', 5000, 7000], ['docker0', 300, 400]]);
  const D = require('./lib/db');
  const alerts = require('./lib/alerts');
  D.run("INSERT INTO servers (id, name, host, created_at) VALUES ('n1', 'edge', 'h', 0)");
  D.run("INSERT INTO alert_rules (id, metric, threshold, server_id, created_at) VALUES ('rn', 'net', 50, 'n1', 0), ('ru', 'unhealthy', 0, 'n1', 0)");
  alerts.evaluate(D.get("SELECT * FROM servers WHERE id = 'n1'"), { ...s, rx: 40e6, tx: 20e6 }, null);
  assert.match(D.get("SELECT message FROM alert_state WHERE rule_id = 'rn'").message, /is 60 MB\/s/);
  assert.ok(D.get("SELECT 1 FROM alert_state WHERE rule_id = 'ru'"));
});

test('quiet hours hold back notifications, not alert state', () => {
  const D = require('./lib/db');
  const alerts = require('./lib/alerts');
  const night = alerts.validateMute({ kind: 'daily', start: '22:00', end: '06:30' });
  assert.deepStrictEqual([night.start_at, night.end_at], [1320, 390]);
  assert.ok(alerts.inWindow(night, new Date(2026, 0, 1, 23, 0)));
  assert.ok(alerts.inWindow(night, new Date(2026, 0, 1, 6, 29)));
  assert.ok(!alerts.inWindow(night, new Date(2026, 0, 1, 6, 30)));
  assert.ok(!alerts.inWindow(alerts.validateMute({ kind: 'daily', start: '09:00', end: '17:00' }), new Date(2026, 0, 1, 18, 0)));
  for (const b of [{ start: '25:00', end: '01:00' }, { start: '10:00', end: '10:00' }, { kind: 'once', start: 1, end: 2 }, { kind: 'once', start: Date.now() + 1e6, end: Date.now() }])
    assert.throws(() => alerts.validateMute(b), undefined, JSON.stringify(b));

  D.run("INSERT INTO servers (id, name, host, tags, created_at) VALUES ('q1', 'quiet', 'h', 'lab', 0)");
  D.run("INSERT INTO alert_rules (id, metric, threshold, server_id, created_at) VALUES ('rq', 'failed', 0, 'q1', 0)");
  D.run("INSERT INTO mute_windows (id, tag, kind, start_at, end_at, created_at) VALUES ('m1', 'lab', 'once', ?, ?, 0)", Date.now() - 1000, Date.now() + 3600e3);
  const srv = D.get("SELECT * FROM servers WHERE id = 'q1'");
  assert.strictEqual(alerts.quietFor(srv)?.id, 'm1');
  alerts.evaluate(srv, { failed: 2 }, null);
  assert.strictEqual(D.get("SELECT notified FROM alert_state WHERE rule_id = 'rq'").notified, 0); // fired, not sent
  D.run("DELETE FROM mute_windows WHERE id = 'm1'"); // window over, still firing -> sent now
  alerts.evaluate(srv, { failed: 2 }, null);
  assert.strictEqual(D.get("SELECT notified FROM alert_state WHERE rule_id = 'rq'").notified, 1);
});

test('authorized_keys parsing and revoke input checks', () => {
  const admin = require('./lib/admin');
  const blob = 'AAAAC3NzaC1lZDI1NTE5AAAAIGq0iQ5nQm8hJ3pHjW2Q0Yg3x8n6yWb1tH0r3u9k2Xc4';
  const users = admin.parseAccounts([
    'user=root:0:1:/bin/bash', `key=root from="10.0.0.0/8",command="echo a b" ssh-ed25519 ${blob} alice@laptop`,
    'user=git:998:0:/usr/sbin/nologin', `key=git ssh-ed25519 ${blob}`, 'key=git not a key', 'key=ghost ssh-ed25519 ' + blob,
  ].join('\n'));
  assert.deepStrictEqual(users.map((u) => [u.name, u.keys.length]), [['root', 1], ['git', 1]]);
  assert.deepStrictEqual(users[0].keys[0], { type: 'ssh-ed25519', blob, fp: users[1].keys[0].fp, comment: 'alice@laptop', options: 'from="10.0.0.0/8",command="echo a b"' });
  assert.match(users[0].keys[0].fp, /^SHA256:[A-Za-z0-9+/]{43}$/);
  assert.throws(() => admin.revokeKey({}, 'root; id', blob), /invalid user/);
  assert.throws(() => admin.revokeKey({}, 'root', `${blob}|d;e`), /invalid key/);
});

test('shared variables: validation, secrets kept on edit, masking', () => {
  const D = require('./lib/db');
  const jobs = require('./lib/jobs');
  assert.throws(() => jobs.validateVariable({ name: '1BAD', value: 'x' }));
  assert.throws(() => jobs.validateVariable({ name: 'TOKEN', value: '' }));
  const v = jobs.validateVariable({ name: 'TOKEN', value: 's3cr3t-value' });
  assert.strictEqual(v.secret, 1);
  assert.ok(!v.value_enc.includes('s3cr3t'));
  D.run('INSERT INTO variables (id, name, value_enc, secret, created_at) VALUES (?, ?, ?, ?, 0)', 'v1', v.name, v.value_enc, v.secret);
  assert.throws(() => jobs.validateVariable({ name: 'TOKEN', value: 'y' }), /already exists/);
  const kept = jobs.validateVariable({ value: '', name: 'RENAMED' }, D.get("SELECT * FROM variables WHERE id = 'v1'"));
  assert.strictEqual(kept.name, 'TOKEN'); // can't rename
  assert.strictEqual(sec.decrypt(kept.value_enc), 's3cr3t-value'); // blank = keep
  const script = { name: 's', body: 'echo', vars: ['TOKEN'] };
  assert.deepStrictEqual(jobs.validateScript(script).vars, ['TOKEN']);
  assert.throws(() => jobs.validateScript({ ...script, vars: ['NOPE'] }), /unknown variable/);
  assert.throws(() => jobs.validateScript({ ...script, params: [{ name: 'TOKEN' }] }), /both a parameter/);
  assert.strictEqual(jobs.masker(['s3cr3t-value'])('token=s3cr3t-value; again s3cr3t-value'), 'token=••••••; again ••••••');
});
