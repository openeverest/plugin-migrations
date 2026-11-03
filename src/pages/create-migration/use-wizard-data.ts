import { useCallback, useEffect, useState } from 'react';
import { fetchInstances } from '../../api/instances';
import type { InstanceSummary } from '../../api/instances.types';
import { fetchStatus } from '../../api/status';
import { ENGINES } from '../../engines';
import type { Engine } from '../../engines/engine.types';
import { availableEngines } from '../../engines/engines.utils';
import { errorMessage } from '../../utils/error.utils';

export interface WizardData {
  engines: Engine[];
  instances: InstanceSummary[];
}

interface WizardDataState {
  data?: WizardData;
  error?: string;
}

export function useWizardData(k8sCluster: string) {
  const [state, setState] = useState<WizardDataState>({});
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    setState({});
    Promise.all([fetchStatus(controller.signal), fetchInstances(k8sCluster, controller.signal)])
      .then(([status, instances]) => {
        if (controller.signal.aborted) return;
        setState({ data: { engines: availableEngines(ENGINES, status.engines), instances } });
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted) return;
        setState({ error: errorMessage(error) });
      });
    return () => controller.abort();
  }, [k8sCluster, attempt]);

  const retry = useCallback(() => setAttempt((value) => value + 1), []);

  return { ...state, retry };
}
