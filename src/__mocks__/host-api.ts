import * as React from 'react';
import { vi } from 'vitest';
import type { PluginApi } from '@openeverest/plugin-sdk';
import { initPluginApi } from '../plugin-api';

export const TEST_BASE_PATH = '/v1/clusters/main/plugins/plugin-migrations';

export function initHostApi(fetch: PluginApi['fetch']): void {
  initPluginApi({
    React,
    registerExtension: vi.fn(),
    fetch,
    basePath: TEST_BASE_PATH,
    cssNonce: 'test-nonce',
    hostVersion: 'test',
    uiContractVersion: 'test',
  });
}

export function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}
