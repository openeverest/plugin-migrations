import type { WizardStep } from './create-migration.types';

export const WIZARD_STEPS: readonly WizardStep[] = ['engine', 'source', 'target', 'options', 'review'];

export const NAME_MAX_LENGTH = 40;

export const NAME_PATTERN = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;

export const DEFAULT_NAME_PREFIX = 'migration';

export const DEFAULT_NAME_SUFFIX_LENGTH = 5;

export const DEFAULT_NAME_ALPHABET = 'abcdefghijklmnopqrstuvwxyz0123456789';
