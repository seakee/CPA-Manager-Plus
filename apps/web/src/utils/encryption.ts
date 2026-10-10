/**
 * 本地存储混淆工具函数（可逆）
 * 从原项目 src/utils/secure-storage.js 迁移
 *
 * IMPORTANT: 这不是安全边界。浏览器端长期持久化的密钥仍应视为可被读取。
 */

const ENC_PREFIX_V1 = 'enc::v1::';
const ENC_PREFIX_V2 = 'enc::v2::';
const SECRET_SALT = 'cli-proxy-api-webui::secure-storage';

export { ENC_PREFIX_V1, ENC_PREFIX_V2 };

let cachedV1KeyBytes: Uint8Array | null = null;
let cachedV2KeyBytes: Uint8Array | null = null;

function encodeText(text: string): Uint8Array {
  const encoder = new TextEncoder();
  return encoder.encode(text);
}

function decodeText(bytes: Uint8Array): string {
  const decoder = new TextDecoder();
  return decoder.decode(bytes);
}

function getV1KeyBytes(): Uint8Array {
  if (cachedV1KeyBytes) return cachedV1KeyBytes;

  try {
    const host = window.location.host;
    const ua = navigator.userAgent;
    cachedV1KeyBytes = encodeText(`${SECRET_SALT}|${host}|${ua}`);
  } catch (error) {
    console.warn('Obfuscation fallback to simple key:', error);
    cachedV1KeyBytes = encodeText(SECRET_SALT);
  }

  return cachedV1KeyBytes;
}

function getV2KeyBytes(): Uint8Array {
  if (cachedV2KeyBytes) return cachedV2KeyBytes;

  try {
    const host = window.location.host;
    cachedV2KeyBytes = encodeText(`${SECRET_SALT}|v2|${host}`);
  } catch (error) {
    console.warn('V2 obfuscation fallback to simple key:', error);
    cachedV2KeyBytes = encodeText(`${SECRET_SALT}|v2`);
  }

  return cachedV2KeyBytes;
}

function xorBytes(data: Uint8Array, keyBytes: Uint8Array): Uint8Array {
  const result = new Uint8Array(data.length);
  for (let i = 0; i < data.length; i++) {
    result[i] = data[i] ^ keyBytes[i % keyBytes.length];
  }
  return result;
}

function toBase64(bytes: Uint8Array): string {
  let binary = '';
  for (let i = 0; i < bytes.length; i++) {
    binary += String.fromCharCode(bytes[i]);
  }
  return btoa(binary);
}

function fromBase64(base64: string): Uint8Array {
  const binary = atob(base64);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

/**
 * 加密数据（统一输出 enc::v2::）
 */
export function obfuscateData(value: string): string {
  if (!value) return value;

  try {
    const keyBytes = getV2KeyBytes();
    const encrypted = xorBytes(encodeText(value), keyBytes);
    return `${ENC_PREFIX_V2}${toBase64(encrypted)}`;
  } catch (error) {
    console.warn('Obfuscation failed, fallback to plaintext:', error);
    return value;
  }
}

/**
 * 解密数据（按 prefix 分流支持 v1 与 v2）
 */
export function deobfuscateData(payload: string): string {
  if (!payload || typeof payload !== 'string') {
    return payload;
  }

  if (payload.startsWith(ENC_PREFIX_V2)) {
    try {
      const encodedBody = payload.slice(ENC_PREFIX_V2.length);
      const encrypted = fromBase64(encodedBody);
      const decrypted = xorBytes(encrypted, getV2KeyBytes());
      return decodeText(decrypted);
    } catch (error) {
      console.warn('V2 deobfuscation failed, return as-is:', error);
      return payload;
    }
  }

  if (payload.startsWith(ENC_PREFIX_V1)) {
    try {
      const encodedBody = payload.slice(ENC_PREFIX_V1.length);
      const encrypted = fromBase64(encodedBody);
      const decrypted = xorBytes(encrypted, getV1KeyBytes());
      return decodeText(decrypted);
    } catch (error) {
      console.warn('V1 deobfuscation failed, return as-is:', error);
      return payload;
    }
  }

  return payload;
}

export type ObfuscationVersion = 'v1' | 'v2' | null;

/**
 * 识别混淆数据版本
 */
export function getObfuscationVersion(value: string): ObfuscationVersion {
  if (!value || typeof value !== 'string') return null;
  if (value.startsWith(ENC_PREFIX_V2)) return 'v2';
  if (value.startsWith(ENC_PREFIX_V1)) return 'v1';
  return null;
}

/**
 * 检查是否已混淆（同时识别 v1 / v2）
 */
export function isObfuscated(value: string): boolean {
  return getObfuscationVersion(value) !== null;
}

// Backward-compatible aliases (this module was historically named "encryption").
export const encryptData = obfuscateData;
export const decryptData = deobfuscateData;
export const isEncrypted = isObfuscated;
