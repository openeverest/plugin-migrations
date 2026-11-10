import { describe, expect, it } from 'vitest';
import type { InstanceSummary } from '../../api/instances.types';
import { mssqlLogShippingEngine } from '../../engines/mssql-log-shipping';
import { defaultMssqlLogShippingOptions } from '../../engines/mssql-log-shipping/mssql-log-shipping.utils';
import { pcsmEngine } from '../../engines/pcsm';
import { WIZARD_STEPS } from './create-migration.constants';
import type { CreateMigrationDraft } from './create-migration.types';
import {
  buildCreateRequest,
  buildReviewSummary,
  buildWizardContext,
  createInitialDraft,
  filterSourceInstances,
  filterTargetInstances,
  generateDefaultName,
  isStepValid,
  selectEngine,
  validateName,
  withSourceInstance,
  withTargetInstance,
} from './create-migration.utils';

const MONGO = 'percona-server-mongodb';
const orders: InstanceSummary = { k8sCluster: 'main', namespace: 'shop', name: 'orders', provider: MONGO };
const archive: InstanceSummary = { k8sCluster: 'main', namespace: 'shop', name: 'archive', provider: MONGO };
const postgres: InstanceSummary = { k8sCluster: 'main', namespace: 'billing', name: 'ledger', provider: 'percona-postgresql' };
const INSTANCES = [orders, archive, postgres];
const ENGINES = [pcsmEngine];

function draft(overrides: Partial<CreateMigrationDraft> = {}): CreateMigrationDraft {
  return { ...createInitialDraft(ENGINES), name: 'orders-into-everest', ...overrides };
}

describe('generateDefaultName', () => {
  it('pre-fills the draft with a valid, random migration name', () => {
    const name = createInitialDraft(ENGINES).name;
    expect(name).toMatch(/^migration-[a-z0-9]{5}$/);
    expect(validateName(name)).toBeUndefined();
  });

  it('maps the full random range onto valid suffix characters', () => {
    expect(generateDefaultName(() => 0)).toBe('migration-aaaaa');
    expect(generateDefaultName(() => 0.9999999)).toBe('migration-99999');
  });
});

describe('validateName', () => {
  it('accepts Kubernetes-style names up to 40 characters', () => {
    expect(validateName('orders-into-everest')).toBeUndefined();
    expect(validateName('a'.repeat(40))).toBeUndefined();
  });

  it('rejects empty, too long and malformed names', () => {
    expect(validateName('')).toBe('Enter a name.');
    expect(validateName('a'.repeat(41))).toBe('Use at most 40 characters.');
    for (const name of ['Orders', 'orders_db', '-orders', 'orders-', 'orders.db']) {
      expect(validateName(name)).toMatch(/lowercase letters/);
    }
  });
});

describe('instance filtering', () => {
  it('keeps only instances whose provider the engine supports', () => {
    expect(filterSourceInstances(INSTANCES, pcsmEngine)).toEqual([orders, archive]);
    expect(filterSourceInstances(INSTANCES, undefined)).toEqual([]);
  });

  it('excludes the chosen source instance from targets', () => {
    expect(filterTargetInstances(INSTANCES, pcsmEngine, 'main/shop/orders')).toEqual([archive]);
    expect(filterTargetInstances(INSTANCES, pcsmEngine)).toEqual([orders, archive]);
  });

  it('applies the exclusion only when the source is an instance', () => {
    const instanceSource = buildWizardContext(
      draft({ sourceKind: 'instance', sourceInstanceKey: 'main/shop/orders' }),
      ENGINES,
      INSTANCES,
    );
    expect(instanceSource.targetInstances).toEqual([archive]);
    const externalSource = buildWizardContext(
      draft({ sourceKind: 'external', sourceInstanceKey: 'main/shop/orders' }),
      ENGINES,
      INSTANCES,
    );
    expect(externalSource.targetInstances).toEqual([orders, archive]);
  });

  it('clears the target when it becomes the source', () => {
    expect(withSourceInstance(draft({ targetInstanceKey: 'main/shop/orders' }), 'main/shop/orders').targetInstanceKey).toBe('');
    expect(withSourceInstance(draft({ targetInstanceKey: 'main/shop/archive' }), 'main/shop/orders').targetInstanceKey).toBe(
      'main/shop/archive',
    );
  });
});

describe('step validation', () => {
  it('requires a valid external URI for an external source', () => {
    const context = buildWizardContext(draft(), ENGINES, INSTANCES);
    expect(isStepValid('source', draft({ externalUri: 'http://db1' }), context)).toBe(false);
    expect(isStepValid('source', draft({ externalUri: 'mongodb://db1:27017' }), context)).toBe(true);
  });

  it('requires a compatible instance for instance sources and targets', () => {
    const source = draft({ sourceKind: 'instance', sourceInstanceKey: 'billing/ledger' });
    expect(isStepValid('source', source, buildWizardContext(source, ENGINES, INSTANCES))).toBe(false);
    const target = draft({ targetInstanceKey: 'main/shop/orders' });
    expect(isStepValid('target', target, buildWizardContext(target, ENGINES, INSTANCES))).toBe(true);
  });
});

describe('buildCreateRequest', () => {
  it('builds the contract request from a complete draft', () => {
    const complete = draft({ externalUri: ' mongodb://u:p@db1:27017 ', targetInstanceKey: 'main/shop/orders' });
    const request = buildCreateRequest(complete, buildWizardContext(complete, ENGINES, INSTANCES), WIZARD_STEPS);
    expect(request).toEqual({
      name: 'orders-into-everest',
      engine: 'pcsm',
      source: { type: 'external', uri: 'mongodb://u:p@db1:27017', caCert: '' },
      target: { type: 'instance', k8sCluster: 'main', namespace: 'shop', name: 'orders' },
      options: { includeNamespaces: [], excludeNamespaces: [], pauseOnInitialSync: false },
    });
  });

  it('returns undefined while any step is invalid', () => {
    const incomplete = draft({ externalUri: 'mongodb://db1', targetInstanceKey: '' });
    expect(buildCreateRequest(incomplete, buildWizardContext(incomplete, ENGINES, INSTANCES), WIZARD_STEPS)).toBeUndefined();
  });
});

describe('buildReviewSummary', () => {
  it('masks the password of an external URI', () => {
    const complete = draft({ externalUri: 'mongodb://admin:hunter2@db1:27017', targetInstanceKey: 'main/shop/orders' });
    const summary = buildReviewSummary(complete, buildWizardContext(complete, ENGINES, INSTANCES));
    expect(summary).toContainEqual({ label: 'Source', value: 'mongodb://admin:****@db1:27017' });
    expect(JSON.stringify(summary)).not.toContain('hunter2');
  });
});

describe('backup storage', () => {
  const sqlOrders: InstanceSummary = { k8sCluster: 'main', namespace: 'shop', name: 'sql-orders', provider: 'mssql' };
  const sqlLegacy: InstanceSummary = { k8sCluster: 'main', namespace: 'legacy', name: 'sql-legacy', provider: 'mssql' };
  const engines = [pcsmEngine, mssqlLogShippingEngine];
  const instances = [sqlOrders, sqlLegacy];

  function mssqlDraft(overrides: Partial<CreateMigrationDraft> = {}): CreateMigrationDraft {
    return {
      ...selectEngine(draft(), mssqlLogShippingEngine),
      sourceKind: 'instance',
      sourceInstanceKey: 'main/legacy/sql-legacy',
      targetInstanceKey: 'main/shop/sql-orders',
      options: { ...defaultMssqlLogShippingOptions(), databases: ['shop'] },
      ...overrides,
    };
  }

  it('requires a storage for engines that stage backups', () => {
    const missing = mssqlDraft();
    const context = buildWizardContext(missing, engines, instances);
    expect(isStepValid('target', missing, context)).toBe(false);
    expect(buildCreateRequest(missing, context, WIZARD_STEPS)).toBeUndefined();

    const complete = mssqlDraft({ backupStorageName: 'minio' });
    const request = buildCreateRequest(complete, buildWizardContext(complete, engines, instances), WIZARD_STEPS);
    expect(request?.backupStorage).toEqual({ name: 'minio' });
    expect(buildReviewSummary(complete, buildWizardContext(complete, engines, instances))).toContainEqual({
      label: 'Backup storage',
      value: 'minio',
    });
  });

  it('does not send a storage for other engines', () => {
    const complete = draft({ externalUri: 'mongodb://db1:27017', targetInstanceKey: 'main/shop/orders', backupStorageName: 'x' });
    const request = buildCreateRequest(complete, buildWizardContext(complete, ENGINES, INSTANCES), WIZARD_STEPS);
    expect(request).toBeDefined();
    expect(request?.backupStorage).toBeUndefined();
  });

  it('clears the storage when the target changes', () => {
    const chosen = mssqlDraft({ backupStorageName: 'minio' });
    expect(withTargetInstance(chosen, 'main/shop/sql-orders').backupStorageName).toBe('minio');
    expect(withTargetInstance(chosen, 'main/legacy/sql-legacy').backupStorageName).toBe('');
    expect(withSourceInstance(chosen, 'main/shop/sql-orders').backupStorageName).toBe('');
  });
});
