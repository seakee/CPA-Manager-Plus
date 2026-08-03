export interface DeviceFlowDetails {
  userCode: string;
  verificationUrl: string;
}

const DEVICE_CODE_PATTERN = /^[A-Z0-9]{4}-[A-Z0-9]{4}$/i;

export function extractDeviceFlowDetails(value: string): DeviceFlowDetails | null {
  try {
    const url = new URL(value);
    const userCode = url.searchParams.get('user_code')?.trim() || '';
    if (!DEVICE_CODE_PATTERN.test(userCode)) return null;

    url.searchParams.delete('user_code');
    return {
      userCode: userCode.toUpperCase(),
      verificationUrl: url.toString(),
    };
  } catch {
    return null;
  }
}
