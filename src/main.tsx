import type { PluginApi, PluginRegisterFn } from '@openeverest/plugin-sdk';
import { MigrationsRoute } from './components/migrations-route/migrations-route';
import { initPluginApi } from './plugin-api';

const LABEL = 'Migrations';

// The host imports this bundle and calls register(api) once at startup.
const register: PluginRegisterFn = (api: PluginApi) => {
  initPluginApi(api);

  api.registerExtension({
    type: 'sidebarItem',
    label: LABEL,
  });

  api.registerExtension({
    type: 'route',
    label: LABEL,
    component: MigrationsRoute,
  });
};

export default register;
