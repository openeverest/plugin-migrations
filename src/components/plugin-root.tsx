import type { ReactNode } from 'react';
import { PluginThemeProvider } from '@openeverest/plugin-theme';
import { cssNonce } from '../plugin-api';

// Emotion cache key: lowercase letters and "-" only, unique across plugins.
const EMOTION_CACHE_KEY = 'plugin-migrations';

interface PluginRootProps {
  children: ReactNode;
}

// Wrap every registered component: it themes MUI from the host design tokens
// and keeps this plugin's styles isolated.
export const PluginRoot = ({ children }: PluginRootProps) => (
  <PluginThemeProvider cacheKey={EMOTION_CACHE_KEY} nonce={cssNonce}>
    {children}
  </PluginThemeProvider>
);
