#!/usr/bin/env node
import {
  createPrivateKey,
  createPublicKey,
  generateKeyPairSync,
  sign as cryptoSign,
  verify as cryptoVerify,
} from 'node:crypto';
import {
  chmodSync,
  existsSync,
  mkdirSync,
  readFileSync,
  realpathSync,
  statSync,
  unlinkSync,
  writeFileSync,
} from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

export const SLOT_MANIFEST_SIGNING_DOMAIN = 'cpamp-slot-manifest-v1\n';
export const DEFAULT_SLOT_KEY_ID = 'cpamp-root-2026-01';
export const DEFAULT_TRUSTED_SLOT_KEYS = Object.freeze({
  'cpamp-root-2026-01': 'e6eb42c6a92a4c78d1c2e6b050234824b17936788799036b4201f98e6291d41f',
});
export const TRUSTED_SLOT_PUBLIC_KEYS = DEFAULT_TRUSTED_SLOT_KEYS;

// RFC 8410 Ed25519 ASN.1 DER prefixes
const ED25519_PKCS8_PREFIX = Buffer.from('302e020100300506032b657004220420', 'hex');
const ED25519_SPKI_PREFIX = Buffer.from('302a300506032b6570032100', 'hex');

const KEY_ID_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const HEX64_PATTERN = /^[0-9a-fA-F]{64}$/;
const HEX128_PATTERN = /^[0-9a-fA-F]{128}$/;

export function compareCodeUnits(a, b) {
  if (a < b) return -1;
  if (a > b) return 1;
  return 0;
}

export function sortKeysDeep(value) {
  if (Array.isArray(value)) {
    return value.map((item) => sortKeysDeep(item));
  }
  if (value && typeof value === 'object') {
    const out = {};
    for (const key of Object.keys(value).sort(compareCodeUnits)) {
      out[key] = sortKeysDeep(value[key]);
    }
    return out;
  }
  return value;
}

export function formatCanonicalJson(value) {
  return JSON.stringify(sortKeysDeep(value), null, 2) + '\n';
}

export function assertValidKeyId(keyId) {
  if (typeof keyId !== 'string' || !KEY_ID_PATTERN.test(keyId)) {
    throw new Error(`Invalid slot signing key_id: ${String(keyId)}`);
  }
  return keyId;
}

export function assertValidPublicKeyHex(publicKeyHex, label = 'public_key_hex') {
  if (typeof publicKeyHex !== 'string' || !HEX64_PATTERN.test(publicKeyHex)) {
    throw new Error(`Invalid Ed25519 ${label}: expected 64 hex characters`);
  }
  return publicKeyHex.toLowerCase();
}
export function resolveSlotPublicKeyHex(spec, options = {}) {
  if (typeof spec !== 'string' || !spec.trim()) {
    throw new Error('Missing slot public key');
  }
  const trimmed = spec.trim();
  if (HEX64_PATTERN.test(trimmed)) {
    return assertValidPublicKeyHex(trimmed, 'slot-public-key');
  }
  if (!existsSync(trimmed) || !statSync(trimmed).isFile()) {
    throw new Error(`Slot public key file not found or invalid hex format: ${spec}`);
  }
  const raw = readFileSync(trimmed, 'utf8').trim();
  if (raw.startsWith('{')) {
    let parsed;
    try {
      parsed = JSON.parse(raw);
    } catch (error) {
      throw new Error(
        `Invalid slot public key JSON in ${spec}: ${error instanceof Error ? error.message : String(error)}`
      );
    }
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      throw new Error(`Invalid slot public key JSON object in ${spec}`);
    }
    if (parsed.algorithm !== undefined && parsed.algorithm !== 'ed25519') {
      throw new Error(
        `Unsupported slot public key algorithm in ${spec}: ${String(parsed.algorithm)}`
      );
    }
    if (parsed.key_id !== undefined) {
      assertValidKeyId(parsed.key_id);
      if (options.keyId !== undefined && parsed.key_id !== options.keyId) {
        throw new Error(
          `Public key file key_id mismatch: expected ${options.keyId}, got ${parsed.key_id}`
        );
      }
    }
    return assertValidPublicKeyHex(parsed.public_key_hex, 'public_key_hex');
  }
  return assertValidPublicKeyHex(raw, 'slot-public-key');
}

function extractRawPublicKeyHex(publicKeyObj) {
  if (publicKeyObj.asymmetricKeyType !== 'ed25519') {
    throw new Error(`Unsupported key type: ${publicKeyObj.asymmetricKeyType}`);
  }
  const spki = publicKeyObj.export({ type: 'spki', format: 'der' });
  if (
    spki.length !== ED25519_SPKI_PREFIX.length + 32 ||
    !spki.subarray(0, ED25519_SPKI_PREFIX.length).equals(ED25519_SPKI_PREFIX)
  ) {
    throw new Error('Unexpected Ed25519 SPKI encoding');
  }
  return spki.subarray(ED25519_SPKI_PREFIX.length).toString('hex');
}

function parsePrivateKeyObject(privateKeyInput) {
  if (privateKeyInput && typeof privateKeyInput === 'object' && privateKeyInput.type === 'private') {
    if (privateKeyInput.asymmetricKeyType !== 'ed25519') {
      throw new Error(`Unsupported private key type: ${privateKeyInput.asymmetricKeyType}`);
    }
    return privateKeyInput;
  }
  if (typeof privateKeyInput !== 'string') {
    throw new Error('Private key must be a PEM string, 64/128-char hex string, or KeyObject');
  }
  const trimmed = privateKeyInput.trim();
  if (!trimmed) {
    throw new Error('Private key is empty');
  }
  if (trimmed.startsWith('-----BEGIN')) {
    const keyObj = createPrivateKey({ key: trimmed, format: 'pem' });
    if (keyObj.asymmetricKeyType !== 'ed25519') {
      throw new Error(`Unsupported PEM private key type: ${keyObj.asymmetricKeyType}`);
    }
    return keyObj;
  }
  if (HEX64_PATTERN.test(trimmed)) {
    const seed = Buffer.from(trimmed, 'hex');
    const pkcs8 = Buffer.concat([ED25519_PKCS8_PREFIX, seed]);
    return createPrivateKey({ key: pkcs8, format: 'der', type: 'pkcs8' });
  }
  if (HEX128_PATTERN.test(trimmed)) {
    const raw = Buffer.from(trimmed, 'hex');
    const seed = raw.subarray(0, 32);
    const expectedPubHex = raw.subarray(32, 64).toString('hex').toLowerCase();
    const pkcs8 = Buffer.concat([ED25519_PKCS8_PREFIX, seed]);
    const keyObj = createPrivateKey({ key: pkcs8, format: 'der', type: 'pkcs8' });
    const derivedPubHex = extractRawPublicKeyHex(createPublicKey(keyObj));
    if (derivedPubHex !== expectedPubHex) {
      throw new Error('128-hex Ed25519 private key public suffix does not match seed');
    }
    return keyObj;
  }
  throw new Error('Unsupported Ed25519 private key format (expected PKCS8 PEM or 64/128-char hex)');
}

function resolvePrivateKeyValue(spec) {
  if (typeof spec !== 'string' || !spec.trim()) {
    throw new Error('Missing private key');
  }
  const trimmed = spec.trim();
  if (trimmed.startsWith('-----BEGIN') || HEX64_PATTERN.test(trimmed) || HEX128_PATTERN.test(trimmed)) {
    return trimmed;
  }
  if (trimmed.startsWith('env:')) {
    const envName = trimmed.slice(4).trim();
    if (!envName || !process.env[envName]) {
      throw new Error(`Environment variable ${envName || '<empty>'} for private key is not set`);
    }
    return process.env[envName];
  }
  if (/^[A-Z][A-Z0-9_]*$/.test(trimmed) && process.env[trimmed]) {
    return process.env[trimmed];
  }
  if (existsSync(spec) && statSync(spec).isFile()) {
    return readFileSync(spec, 'utf8');
  }
  throw new Error(`Private key file not found or invalid key format: ${spec}`);
}

export function generateSlotKeypair(keyId) {
  const validKeyId = assertValidKeyId(keyId);
  const { publicKey, privateKey } = generateKeyPairSync('ed25519');
  const privateKeyPem = privateKey.export({ type: 'pkcs8', format: 'pem' });
  const publicKeyHex = extractRawPublicKeyHex(publicKey);
  return {
    key_id: validKeyId,
    algorithm: 'ed25519',
    privateKeyPem,
    publicKeyHex,
    publicRecord: {
      key_id: validKeyId,
      algorithm: 'ed25519',
      public_key_hex: publicKeyHex,
    },
  };
}

export function buildSlotSigningPayload(manifestBytes) {
  const bytes = Buffer.isBuffer(manifestBytes)
    ? manifestBytes
    : manifestBytes instanceof Uint8Array
      ? Buffer.from(manifestBytes)
      : typeof manifestBytes === 'string'
        ? Buffer.from(manifestBytes, 'utf8')
        : null;
  if (!bytes || bytes.length === 0) {
    throw new Error('Manifest bytes must be non-empty');
  }
  return Buffer.concat([Buffer.from(SLOT_MANIFEST_SIGNING_DOMAIN, 'utf8'), bytes]);
}

export function signSlotManifestBytes(manifestBytes, privateKeyInput, keyId) {
  const validKeyId = assertValidKeyId(keyId);
  const privateKeyObj = parsePrivateKeyObject(privateKeyInput);
  const payload = buildSlotSigningPayload(manifestBytes);
  const sigBytes = cryptoSign(null, payload, privateKeyObj);
  if (sigBytes.length !== 64) {
    throw new Error(`Unexpected Ed25519 signature byte length: ${sigBytes.length}`);
  }
  return {
    schema_version: 1,
    algorithm: 'ed25519',
    key_id: validKeyId,
    signature: sigBytes.toString('base64'),
  };
}

export function parseAndValidateSlotSignatureRecord(signatureInput, options = {}) {
  let sigObj = signatureInput;
  if (typeof signatureInput === 'string' || Buffer.isBuffer(signatureInput)) {
    sigObj = JSON.parse(signatureInput.toString('utf8'));
  }
  if (!sigObj || typeof sigObj !== 'object' || Array.isArray(sigObj)) {
    throw new Error('Slot signature record must be a JSON object');
  }
  const allowedKeys = new Set(['schema_version', 'algorithm', 'key_id', 'signature']);
  for (const key of Object.keys(sigObj)) {
    if (!allowedKeys.has(key)) {
      throw new Error(`Unexpected field in slot signature record: ${key}`);
    }
  }
  if (sigObj.schema_version !== 1) {
    throw new Error(`Unsupported slot signature schema_version: ${String(sigObj.schema_version)}`);
  }
  if (sigObj.algorithm !== 'ed25519') {
    throw new Error(`Unsupported slot signature algorithm: ${String(sigObj.algorithm)}`);
  }
  const sigKeyId = assertValidKeyId(sigObj.key_id);
  if (options.keyId !== undefined) {
    const expectedKeyId = assertValidKeyId(options.keyId);
    if (sigKeyId !== expectedKeyId) {
      throw new Error(`Slot signature key_id mismatch: expected ${expectedKeyId}, got ${sigKeyId}`);
    }
  }
  if (typeof sigObj.signature !== 'string' || !sigObj.signature.trim()) {
    throw new Error('Slot signature field is missing or empty');
  }
  const sigBytes = Buffer.from(sigObj.signature, 'base64');
  if (sigBytes.length !== 64 || sigBytes.toString('base64') !== sigObj.signature) {
    throw new Error('Slot signature must be canonical base64 encoding of 64 bytes');
  }
  return { sigObj, sigKeyId, sigBytes };
}

export function verifySlotManifestBytes(manifestBytes, signatureInput, options = {}) {
  const { sigKeyId, sigBytes } = parseAndValidateSlotSignatureRecord(signatureInput, options);
  const trustedPubHex = Object.hasOwn(TRUSTED_SLOT_PUBLIC_KEYS, sigKeyId)
    ? TRUSTED_SLOT_PUBLIC_KEYS[sigKeyId]
    : undefined;

  let resolvedPubHex = options.publicKeyHex;
  if (resolvedPubHex !== undefined && resolvedPubHex !== null) {
    const normalizedProvided = assertValidPublicKeyHex(resolvedPubHex);
    if (trustedPubHex && normalizedProvided !== trustedPubHex.toLowerCase()) {
      throw new Error(
        `Provided public_key_hex for key_id ${sigKeyId} does not match trusted slot keyring`
      );
    }
    resolvedPubHex = normalizedProvided;
  } else {
    resolvedPubHex = trustedPubHex;
    if (!resolvedPubHex) {
      throw new Error(`Unknown untrusted slot key_id: ${sigKeyId}`);
    }
  }
  const validPubHex = assertValidPublicKeyHex(resolvedPubHex);
  const spki = Buffer.concat([ED25519_SPKI_PREFIX, Buffer.from(validPubHex, 'hex')]);
  const publicKeyObj = createPublicKey({ key: spki, format: 'der', type: 'spki' });
  const payload = buildSlotSigningPayload(manifestBytes);
  if (!cryptoVerify(null, payload, publicKeyObj, sigBytes)) {
    throw new Error('Ed25519 slot manifest signature verification failed');
  }
  return true;
}

export function verifySlotManifestWithPolicy(manifestBytes, signatureInput, options = {}) {
  const dryRun = options.dryRun === true;
  const allowEphemeralSlotKey = options.allowEphemeralSlotKey === true;
  if (allowEphemeralSlotKey && !dryRun) {
    throw new Error('--allow-ephemeral-slot-key requires --dry-run');
  }
  const rawPublicKeyHex =
    options.publicKeyHex !== undefined && options.publicKeyHex !== null
      ? options.publicKeyHex
      : options.slotPublicKeyHex;
  if (
    options.publicKeyHex !== undefined &&
    options.publicKeyHex !== null &&
    options.slotPublicKeyHex !== undefined &&
    options.slotPublicKeyHex !== null &&
    String(options.publicKeyHex).trim().toLowerCase() !==
      String(options.slotPublicKeyHex).trim().toLowerCase()
  ) {
    throw new Error('publicKeyHex and slotPublicKeyHex disagree');
  }
  const hasPublicKeyHex =
    rawPublicKeyHex !== undefined &&
    rawPublicKeyHex !== null &&
    (typeof rawPublicKeyHex !== 'string' || rawPublicKeyHex.trim() !== '');
  const { sigObj, sigKeyId } = parseAndValidateSlotSignatureRecord(signatureInput, {
    keyId: options.keyId,
  });
  if (Object.hasOwn(TRUSTED_SLOT_PUBLIC_KEYS, sigKeyId)) {
    return verifySlotManifestBytes(manifestBytes, sigObj, {
      keyId: sigKeyId,
      ...(hasPublicKeyHex ? { publicKeyHex: rawPublicKeyHex } : {}),
    });
  }
  if (sigKeyId.startsWith('cpamp-dry-run-')) {
    if (!(dryRun && allowEphemeralSlotKey)) {
      throw new Error(`Unknown untrusted slot key_id: ${sigKeyId}`);
    }
    if (!hasPublicKeyHex) {
      throw new Error('Ephemeral slot key verification requires --slot-public-key');
    }
    return verifySlotManifestBytes(manifestBytes, sigObj, {
      keyId: sigKeyId,
      publicKeyHex: rawPublicKeyHex,
    });
  }
  if (allowEphemeralSlotKey && !hasPublicKeyHex) {
    throw new Error('Ephemeral slot key verification requires --slot-public-key');
  }
  if (hasPublicKeyHex) {
    return verifySlotManifestBytes(manifestBytes, sigObj, {
      keyId: sigKeyId,
      publicKeyHex: rawPublicKeyHex,
    });
  }
  return verifySlotManifestBytes(manifestBytes, sigObj, { keyId: sigKeyId });
}

function writeFileExactMode(targetPath, content, mode) {
  mkdirSync(path.dirname(targetPath), { recursive: true });
  if (existsSync(targetPath)) {
    chmodSync(targetPath, 0o600);
    unlinkSync(targetPath);
  }
  writeFileSync(targetPath, content, { mode });
  chmodSync(targetPath, mode);
}

function parseCliFlags(args) {
  const parsed = {};
  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i];
    if (!arg.startsWith('--')) {
      throw new Error(`Unexpected positional argument: ${arg}`);
    }
    const key = arg.slice(2);
    const val = args[i + 1];
    if (val === undefined || val.startsWith('--')) {
      throw new Error(`Missing value for --${key}`);
    }
    parsed[key] = val;
    i += 1;
  }
  return parsed;
}

function runCli(argv) {
  const [subcommand, ...rest] = argv;
  if (!subcommand || subcommand === '--help' || subcommand === '-h') {
    process.stdout.write(
      [
        'Usage:',
        '  cpamp-slot-sign.mjs keygen --key-id <id> [--out-dir <dir>]',
        '  cpamp-slot-sign.mjs sign --manifest <path> --private-key <pem-or-hex> --key-id <id> [--out <sig>]',
        '  cpamp-slot-sign.mjs verify --manifest <path> --signature <path> --public-key-hex <64-hex> --key-id <id>',
      ].join('\n') + '\n'
    );
    return;
  }

  const flags = parseCliFlags(rest);
  if (subcommand === 'keygen') {
    if (!flags['key-id']) throw new Error('keygen requires --key-id <id>');
    const outDir = flags['out-dir'] || '.';
    const pair = generateSlotKeypair(flags['key-id']);
    const privPath = path.join(outDir, `${pair.key_id}.priv.pem`);
    const pubPath = path.join(outDir, `${pair.key_id}.pub.json`);
    writeFileExactMode(privPath, pair.privateKeyPem, 0o600);
    writeFileExactMode(
      pubPath,
      JSON.stringify(
        {
          key_id: pair.key_id,
          algorithm: 'ed25519',
          public_key_hex: pair.publicKeyHex,
        },
        null,
        2
      ) + '\n',
      0o644
    );
    process.stdout.write(
      JSON.stringify(
        {
          key_id: pair.key_id,
          algorithm: 'ed25519',
          private_key_path: privPath,
          public_key_path: pubPath,
          public_key_hex: pair.publicKeyHex,
        },
        null,
        2
      ) + '\n'
    );
    return;
  }

  if (subcommand === 'sign') {
    if (!flags.manifest) throw new Error('sign requires --manifest <path>');
    if (!flags['private-key']) throw new Error('sign requires --private-key <pem-or-hex>');
    if (!flags['key-id']) throw new Error('sign requires --key-id <id>');
    const manifestBytes = readFileSync(flags.manifest);
    const privRaw = resolvePrivateKeyValue(flags['private-key']);
    const signature = signSlotManifestBytes(manifestBytes, privRaw, flags['key-id']);
    const outPath = flags.out || `${flags.manifest}.sig`;
    const rendered =
      JSON.stringify(
        {
          schema_version: signature.schema_version,
          algorithm: signature.algorithm,
          key_id: signature.key_id,
          signature: signature.signature,
        },
        null,
        2
      ) + '\n';
    writeFileExactMode(outPath, rendered, 0o644);
    process.stdout.write(rendered);
    return;
  }

  if (subcommand === 'verify') {
    if (!flags.manifest) throw new Error('verify requires --manifest <path>');
    if (!flags.signature) throw new Error('verify requires --signature <path>');
    if (!flags['key-id']) throw new Error('verify requires --key-id <id>');
    const fromHexFlag = flags['public-key-hex']
      ? assertValidPublicKeyHex(flags['public-key-hex'], 'public-key-hex')
      : undefined;
    const fromKeyFlag = flags['public-key']
      ? resolveSlotPublicKeyHex(flags['public-key'], { keyId: flags['key-id'] })
      : undefined;
    if (fromHexFlag && fromKeyFlag && fromHexFlag !== fromKeyFlag) {
      throw new Error('--public-key and --public-key-hex disagree');
    }
    const publicKeyHex = fromHexFlag ?? fromKeyFlag;
    const manifestBytes = readFileSync(flags.manifest);
    const signatureRaw = readFileSync(flags.signature, 'utf8');
    verifySlotManifestBytes(manifestBytes, signatureRaw, {
      ...(publicKeyHex ? { publicKeyHex } : {}),
      keyId: flags['key-id'],
    });
    process.stdout.write(JSON.stringify({ ok: true, key_id: flags['key-id'] }) + '\n');
    return;
  }

  throw new Error(`Unknown subcommand: ${subcommand}`);
}

const isEntrypoint =
  process.argv[1] &&
  existsSync(process.argv[1]) &&
  realpathSync(fileURLToPath(import.meta.url)) === realpathSync(process.argv[1]);

if (isEntrypoint) {
  try {
    runCli(process.argv.slice(2));
  } catch (error) {
    console.error(error instanceof Error ? error.message : String(error));
    process.exit(1);
  }
}
