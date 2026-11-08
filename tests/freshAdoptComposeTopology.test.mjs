import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { parse as parseYaml } from 'yaml';
import { describe, expect, it } from 'vitest';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const composePath = path.join(repoRoot, 'docker-compose.yml');

// A Fresh claim session is the whole trust anchor of an adoption, so the
// operator holding the `initialize` handoff alone decides to adopt. That anchor
// is only meaningful while the Fresh record it authorizes is unreadable to every
// other container: the Manager data volume holds the Fresh operation, its
// admin credential and the protected adoption snapshot.
const MANAGER_DATA_VOLUME = 'cpamp-data';
const MANAGER_DATA_TARGET = '/data';
const MANAGER_SERVICE = 'cpa-manager-plus';

// A short-syntax source is a host path when it looks like one; anything else is
// a named volume or a `${VAR}` interpolation, neither of which this file can
// resolve, so both stay classified as volumes rather than binds.
const isHostPath = (source) =>
  typeof source === 'string' && (source.startsWith('/') || source.startsWith('./') || source.startsWith('../') || source.startsWith('~'));

// The Manager store is the target path, not just the volume: a host bind onto the
// same path would put the Fresh CAS on the host disk, and a bind onto a
// subdirectory would split the store across two mounts.
const isManagerDataPath = (target) =>
  typeof target === 'string' && (target === MANAGER_DATA_TARGET || target.startsWith(`${MANAGER_DATA_TARGET}/`));

// Compose accepts both the short `[source:]target[:mode]` string and the long
// object form. Normalizing both keeps this assertion independent of formatting.
const normalizeVolume = (volume) => {
  if (typeof volume === 'string') {
    const [first, second, third] = volume.split(':');
    // A single segment is an anonymous volume, so it has no source at all.
    const [source, target, mode] = second === undefined ? [undefined, first, undefined] : [first, second, third];
    return {
      source,
      target,
      type: undefined,
      readOnly: /^(?:ro|rw)$/.test(mode ?? '') ? mode === 'ro' : false,
    };
  }
  return {
    source: volume?.source,
    target: volume?.target,
    type: volume?.type,
    readOnly: volume?.read_only === true,
  };
};

const serviceVolumes = (document) =>
  Object.entries(document?.services ?? {}).flatMap(([service, config]) =>
    (config?.volumes ?? []).map((volume) => ({ service, ...normalizeVolume(volume) })),
  );

const isBind = (volume) => volume.type === 'bind' || (volume.type === undefined && isHostPath(volume.source));

// `volumes_from` accepts `service`, `service:name`, `service:name:ro`,
// `container:name` and a bare service/container name, so the referenced service
// is the segment after an explicit prefix and the first segment otherwise.
const volumesFromTarget = (reference) => {
  const parts = String(reference).split(':');
  return parts[0] === 'service' || parts[0] === 'container' ? parts[1] : parts[0];
};

const serviceVolumesFrom = (document) =>
  Object.entries(document?.services ?? {}).flatMap(([service, config]) =>
    (config?.volumes_from ?? []).map((reference) => ({ service, reference: String(reference), target: volumesFromTarget(reference) })),
  );

// Every way the Manager store can reach a second container, as one audit so the
// checked-in file and the negative fixtures below run through identical rules.
const auditComposeTopology = (document) => {
  const volumes = serviceVolumes(document);
  return {
    managerDataMounts: volumes.filter((volume) => volume.source === MANAGER_DATA_VOLUME),
    unexpectedManagerDataMounts: volumes.filter(
      (volume) => volume.source === MANAGER_DATA_VOLUME && (volume.service !== MANAGER_SERVICE || volume.target !== MANAGER_DATA_TARGET),
    ),
    foreignTargetMounts: volumes.filter((volume) => volume.service !== MANAGER_SERVICE && isManagerDataPath(volume.target)),
    hostBindsOfManagerData: volumes.filter((volume) => isBind(volume) && isManagerDataPath(volume.target)),
    inheritedManagerData: serviceVolumesFrom(document).filter((entry) => entry.target === MANAGER_SERVICE),
  };
};

describe('Fresh adoption compose topology', () => {
  const document = parseYaml(readFileSync(composePath, 'utf8'));
  const audit = auditComposeTopology(document);

  it('mounts the Manager data volume only in the Manager service', () => {
    expect(audit.managerDataMounts.map(({ service, target }) => ({ service, target }))).toEqual([
      { service: MANAGER_SERVICE, target: MANAGER_DATA_TARGET },
    ]);
    expect(audit.unexpectedManagerDataMounts.map(({ service, target }) => `${service}:${target}`)).toEqual([]);
  });

  it('never exposes the Manager data path to another service', () => {
    // A different volume at the same path would still let a sibling container
    // outlive this record, so the target path is reserved as well as the source.
    expect(audit.foreignTargetMounts.map((volume) => `${volume.service}:${volume.target}`)).toEqual([]);
  });

  it('never bind-mounts a host path onto the Manager data path', () => {
    // A host bind replaces the named volume, so the Fresh CAS, its admin
    // credential and the protected snapshot would live on the host filesystem
    // where any sibling container can mount the same directory.
    expect(audit.hostBindsOfManagerData.map((volume) => `${volume.service}:${volume.source}:${volume.target}`)).toEqual([]);
  });

  it('never inherits the Manager volumes through volumes_from', () => {
    // `volumes_from` copies every mount of the named service, which reaches the
    // store without declaring the volume anywhere this file would see it.
    expect(audit.inheritedManagerData.map((entry) => `${entry.service}:${entry.reference}`)).toEqual([]);
  });

  it('declares the Manager data volume as a named volume, not a host bind', () => {
    expect(Object.keys(document?.volumes ?? {})).toContain(MANAGER_DATA_VOLUME);
    expect(audit.managerDataMounts.every((volume) => !isBind(volume))).toBe(true);
  });
});

// Negative fixtures pin the two rules the checked-in file cannot exercise. Each
// one must be reported as an offender, so the assertions above fail if a rule is
// removed and the fixture is not the only thing that noticed.
describe('Fresh adoption compose topology negative fixtures', () => {
  const fixture = (services) =>
    parseYaml(['services:', ...services, 'volumes:', `  ${MANAGER_DATA_VOLUME}:`].join('\n'));

  const checkedInManager = [`  ${MANAGER_SERVICE}:`, '    volumes:', `      - ${MANAGER_DATA_VOLUME}:${MANAGER_DATA_TARGET}`];

  it('rejects a volumes_from reference to the Manager service', () => {
    const audit = auditComposeTopology(fixture([...checkedInManager, '  cpamp-runtime:', '    volumes_from:', `      - ${MANAGER_SERVICE}`]));

    expect(audit.inheritedManagerData.map((entry) => `${entry.service}:${entry.reference}`)).toEqual([`cpamp-runtime:${MANAGER_SERVICE}`]);
  });

  it('rejects a prefixed read-only volumes_from reference to the Manager service', () => {
    const audit = auditComposeTopology(
      fixture([...checkedInManager, '  cpamp-runtime:', '    volumes_from:', `      - 'service:${MANAGER_SERVICE}:ro'`]),
    );

    expect(audit.inheritedManagerData.map((entry) => `${entry.service}:${entry.reference}`)).toEqual([
      `cpamp-runtime:service:${MANAGER_SERVICE}:ro`,
    ]);
  });

  it('rejects a short-syntax host bind of the Manager data path', () => {
    const audit = auditComposeTopology(
      fixture([...checkedInManager, '  cpamp-runtime:', '    volumes:', `      - ./manager-data:${MANAGER_DATA_TARGET}`]),
    );

    expect(audit.hostBindsOfManagerData.map((volume) => `${volume.service}:${volume.source}:${volume.target}`)).toEqual([
      `cpamp-runtime:./manager-data:${MANAGER_DATA_TARGET}`,
    ]);
    expect(audit.foreignTargetMounts.map((volume) => `${volume.service}:${volume.target}`)).toEqual([
      `cpamp-runtime:${MANAGER_DATA_TARGET}`,
    ]);
  });

  it('rejects a long-syntax host bind of the Manager data path', () => {
    const audit = auditComposeTopology(
      fixture([
        ...checkedInManager,
        '  cpamp-runtime:',
        '    volumes:',
        '      - type: bind',
        '        source: /var/lib/cpamp/manager-data',
        `        target: ${MANAGER_DATA_TARGET}`,
      ]),
    );

    expect(audit.hostBindsOfManagerData.map((volume) => `${volume.service}:${volume.source}:${volume.target}`)).toEqual([
      `cpamp-runtime:/var/lib/cpamp/manager-data:${MANAGER_DATA_TARGET}`,
    ]);
  });

  it('rejects a host bind of a subdirectory of the Manager data path', () => {
    const audit = auditComposeTopology(
      fixture([...checkedInManager, '  cpamp-runtime:', '    volumes:', `      - ./fresh:${MANAGER_DATA_TARGET}/fresh`]),
    );

    expect(audit.hostBindsOfManagerData.map((volume) => `${volume.service}:${volume.source}:${volume.target}`)).toEqual([
      `cpamp-runtime:./fresh:${MANAGER_DATA_TARGET}/fresh`,
    ]);
  });

  it('rejects the Manager service binding a host path onto its own data path', () => {
    const audit = auditComposeTopology(
      fixture([`  ${MANAGER_SERVICE}:`, '    volumes:', `      - ./manager-data:${MANAGER_DATA_TARGET}`, '  cpamp-runtime:', '    image: unused']),
    );

    expect(audit.hostBindsOfManagerData.map((volume) => `${volume.service}:${volume.source}:${volume.target}`)).toEqual([
      `${MANAGER_SERVICE}:./manager-data:${MANAGER_DATA_TARGET}`,
    ]);
    expect(audit.managerDataMounts).toEqual([]);
  });
});
