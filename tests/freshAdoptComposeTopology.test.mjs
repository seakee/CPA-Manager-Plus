import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { parse as parseYaml } from 'yaml';
import { describe, expect, it } from 'vitest';
import { parseGoDurationSeconds } from '../bin/ci/validate-runtime12-compose.mjs';
import { validateRuntime12ComposeDocument } from '../bin/release/render-embedded-compose.mjs';
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

  it('validates stop_grace_period >= CPAMP_DRAIN_TIMEOUT + 60s and CPAMP_RUNTIME_ADDR loopback in validate-runtime12-compose.mjs', () => {
    const validatorPath = path.join(repoRoot, 'bin', 'ci', 'validate-runtime12-compose.mjs');
    const validDoc = {
      services: {
        'cpa-manager-plus': {
          image: 'seakee/cpa-manager-plus:latest',
          stop_grace_period: '2m30s',
          ports: [{ mode: 'ingress', published: '18317', target: 18317, protocol: 'tcp' }],
          environment: {
            CPAMP_PUBLIC_ORIGIN: '',
            CPAMP_DRAIN_TIMEOUT: '90s',
          },
          volumes: [{ type: 'volume', source: 'cpamp-data', target: '/data' }],
        },
      },
      volumes: { 'cpamp-data': {} },
    };
    const runValidator = (doc) =>
      execFileSync(process.execPath, [validatorPath], {
        cwd: repoRoot,
        input: JSON.stringify(doc),
        encoding: 'utf8',
        stdio: ['pipe', 'pipe', 'pipe'],
      });

    const validDurationTable = [
      ['1.5m', 90],
      ['90000ms', 90],
      ['1m30s', 90],
      ['2h', 7200],
      ['.5m', 30],
      ['1.m', 60],
      ['500us', 0.0005],
      ['500µs', 0.0005],
      ['500μs', 0.0005],
      ['1000ns', 1e-6],
      ['0', 0],
      ['+150s', 150],
    ];
    const pinnedDocForRender = {
      ...validDoc,
      services: {
        'cpa-manager-plus': {
          ...validDoc.services['cpa-manager-plus'],
          image: `seakee/cpa-manager-plus:v2.0.0-beta.1@sha256:${'a'.repeat(64)}`,
        },
      },
    };
    for (const [input, expected] of validDurationTable) {
      expect(parseGoDurationSeconds(input)).toBeCloseTo(expected, 9);
      if (expected > 0) {
        const graceStr = `${Math.ceil(Math.max(150, expected + 60))}s`;
        const docA = structuredClone(validDoc);
        docA.services['cpa-manager-plus'].environment.CPAMP_DRAIN_TIMEOUT = input;
        docA.services['cpa-manager-plus'].stop_grace_period = graceStr;
        expect(() => runValidator(docA)).not.toThrow();

        const docB = structuredClone(pinnedDocForRender);
        docB.services['cpa-manager-plus'].environment.CPAMP_DRAIN_TIMEOUT = input;
        docB.services['cpa-manager-plus'].stop_grace_period = graceStr;
        expect(() => validateRuntime12ComposeDocument(docB, { validateComposeScript: false })).not.toThrow();
      }
    }
    for (const invalid of ['90', 'abc', '-1s', '', '.s', '1d']) {
      expect(Number.isNaN(parseGoDurationSeconds(invalid))).toBe(true);
      if (invalid !== '') {
        const docA = structuredClone(validDoc);
        docA.services['cpa-manager-plus'].environment.CPAMP_DRAIN_TIMEOUT = invalid;
        expect(() => runValidator(docA)).toThrow(/stop_grace_period >= CPAMP_DRAIN_TIMEOUT \+ 60s/);

        const docB = structuredClone(pinnedDocForRender);
        docB.services['cpa-manager-plus'].environment.CPAMP_DRAIN_TIMEOUT = invalid;
        expect(() => validateRuntime12ComposeDocument(docB, { validateComposeScript: false })).toThrow(
          /stop_grace_period >= CPAMP_DRAIN_TIMEOUT \+ 60s/
        );
      }
    }
    expect(() => runValidator(validDoc)).not.toThrow();

    for (const validDrain of ['1.5m', '90000ms', '1m30s']) {
      const fractionalDrainDoc = structuredClone(validDoc);
      fractionalDrainDoc.services['cpa-manager-plus'].environment.CPAMP_DRAIN_TIMEOUT = validDrain;
      expect(() => runValidator(fractionalDrainDoc)).not.toThrow();
    }

    const fractionalOverBudgetDoc = structuredClone(validDoc);
    fractionalOverBudgetDoc.services['cpa-manager-plus'].environment.CPAMP_DRAIN_TIMEOUT = '1.75m';
    fractionalOverBudgetDoc.services['cpa-manager-plus'].stop_grace_period = '150s';
    expect(() => runValidator(fractionalOverBudgetDoc)).toThrow(/stop_grace_period >= CPAMP_DRAIN_TIMEOUT \+ 60s/);

    const fractionalWithinCustomGraceDoc = structuredClone(validDoc);
    fractionalWithinCustomGraceDoc.services['cpa-manager-plus'].environment.CPAMP_DRAIN_TIMEOUT = '1.75m';
    fractionalWithinCustomGraceDoc.services['cpa-manager-plus'].stop_grace_period = '2m45s';
    expect(() => runValidator(fractionalWithinCustomGraceDoc)).not.toThrow();

    for (const invalidDrain of ['90', 'abc', '-1s']) {
      const badDrainDoc = structuredClone(validDoc);
      badDrainDoc.services['cpa-manager-plus'].environment.CPAMP_DRAIN_TIMEOUT = invalidDrain;
      expect(() => runValidator(badDrainDoc)).toThrow(/stop_grace_period >= CPAMP_DRAIN_TIMEOUT \+ 60s/);
    }

    const shortGraceDoc = structuredClone(validDoc);
    shortGraceDoc.services['cpa-manager-plus'].stop_grace_period = '2m0s';
    expect(() => runValidator(shortGraceDoc)).toThrow(/stop_grace_period >= CPAMP_DRAIN_TIMEOUT \+ 60s/);

    const longDrainShortGraceDoc = structuredClone(validDoc);
    longDrainShortGraceDoc.services['cpa-manager-plus'].environment.CPAMP_DRAIN_TIMEOUT = '120s';
    longDrainShortGraceDoc.services['cpa-manager-plus'].stop_grace_period = '150s';
    expect(() => runValidator(longDrainShortGraceDoc)).toThrow(/stop_grace_period >= CPAMP_DRAIN_TIMEOUT \+ 60s/);
    for (const badRuntimeAddr of ['0.0.0.0:9081', '127.0.0.1:9000', '[::1]:9081', '127.0.0.2:9081', 'localhost:9081']) {
      const badRuntimeAddrDoc = structuredClone(validDoc);
      badRuntimeAddrDoc.services['cpa-manager-plus'].environment.CPAMP_RUNTIME_ADDR = badRuntimeAddr;
      expect(() => runValidator(badRuntimeAddrDoc)).toThrow(
        /CPAMP_RUNTIME_ADDR override must stay on in-container loopback 127\.0\.0\.1:9081/
      );
      const badRuntimeAddrRenderDoc = structuredClone(pinnedDocForRender);
      badRuntimeAddrRenderDoc.services['cpa-manager-plus'].environment.CPAMP_RUNTIME_ADDR = badRuntimeAddr;
      expect(() => validateRuntime12ComposeDocument(badRuntimeAddrRenderDoc, { validateComposeScript: false })).toThrow(
        /CPAMP_RUNTIME_ADDR override must stay on in-container loopback 127\.0\.0\.1:9081/
      );
    }

    for (const badCpaAddr of ['0.0.0.0:8317', '127.0.0.1:9000', '[::1]:8317', '127.0.0.2:8317', 'localhost:8317']) {
      const badCpaAddrDoc = structuredClone(validDoc);
      badCpaAddrDoc.services['cpa-manager-plus'].environment.CPAMP_RUNTIME_CPA_ADDR = badCpaAddr;
      expect(() => runValidator(badCpaAddrDoc)).toThrow(
        /CPAMP_RUNTIME_CPA_ADDR override must stay on in-container loopback 127\.0\.0\.1:8317/
      );
      const badCpaAddrRenderDoc = structuredClone(pinnedDocForRender);
      badCpaAddrRenderDoc.services['cpa-manager-plus'].environment.CPAMP_RUNTIME_CPA_ADDR = badCpaAddr;
      expect(() => validateRuntime12ComposeDocument(badCpaAddrRenderDoc, { validateComposeScript: false })).toThrow(
        /CPAMP_RUNTIME_CPA_ADDR override must stay on in-container loopback 127\.0\.0\.1:8317/
      );
    }
  });
});
