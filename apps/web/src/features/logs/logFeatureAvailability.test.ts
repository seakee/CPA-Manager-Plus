import { describe, expect, it } from 'vitest';
import { isFileLogsAvailable } from './logFeatureAvailability';

describe('isFileLogsAvailable', () => {
  it('only enables log viewer when file logging is explicitly true', () => {
    expect(isFileLogsAvailable({ loggingToFile: true })).toBe(true);
    expect(isFileLogsAvailable({ loggingToFile: false })).toBe(false);
    expect(isFileLogsAvailable({})).toBe(false);
    expect(isFileLogsAvailable(null)).toBe(false);
  });
});
