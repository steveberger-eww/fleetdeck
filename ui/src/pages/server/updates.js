// How far a container's image is behind its registry (internal/registry steps), smallest change first.
// The four rungs of the scale in the container's details; "build" and "patch" share the "Small" rung.
export const SCALE = [['current', 'Up to date'], ['small', 'Small'], ['minor', 'Minor'], ['major', 'Major']];

export const STEPS = {
  current: { rung: 'current', kind: 'ok', label: 'Up to date', text: 'Nothing newer of this kind is published.' },
  build: { rung: 'small', kind: 'info', label: 'New build', text: 'Same version, rebuilt: usually security fixes in the base image. Safe to apply any time.' },
  patch: { rung: 'small', kind: 'info', label: 'Patch', text: 'Bug and security fixes for the same version. Safe to apply soon.' },
  minor: { rung: 'minor', kind: 'warn', label: 'Minor update', text: 'New features that should stay compatible. Skim the release notes, then update.' },
  major: { rung: 'major', kind: 'bad', label: 'Major update', text: 'Can break things or need a data migration. Read the upgrade notes and back up its volumes first.' },
};

// Worth a chip in the table: anything newer than what runs.
export const behind = (u) => !!u && !!STEPS[u.step] && u.step !== 'current';

export const label = (u) => (u.step === 'major' && u.behind > 1 ? `${u.behind} majors behind` : STEPS[u.step].label);

// The reference with another tag: ghcr.io/org/app:1.2@sha… is never bumped (pinned), so only name[:tag].
export const withTag = (ref, tag) => `${ref.replace(/:[^:/]+$/, '')}:${tag}`;

// A new version tag is applied by editing the Compose file; a channel (latest) or a new build by pulling again.
export const needsEdit = (u) => !!u.newest && !u.channel && !u.pinned;
