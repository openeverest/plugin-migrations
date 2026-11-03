import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import { LiveIndicator } from './live-indicator';

const updatedAt = new Date(2026, 10, 3, 17, 13, 42);
const time = updatedAt.toLocaleTimeString();

afterEach(cleanup);

describe('LiveIndicator', () => {
  it('shows Live while refreshes succeed, without a changing timestamp', () => {
    render(<LiveIndicator lastUpdatedAt={updatedAt} stale={false} />);
    expect(screen.getByText('Live')).toBeTruthy();
    expect(screen.queryByText(new RegExp(time))).toBeNull();
  });

  it('shows the absolute time of the last successful refresh when stale', () => {
    render(<LiveIndicator lastUpdatedAt={updatedAt} stale />);
    expect(screen.getByText(`Last updated ${time}`)).toBeTruthy();
    expect(screen.queryByText('Live')).toBeNull();
  });

  it('renders nothing before the first successful refresh', () => {
    const { container } = render(<LiveIndicator stale />);
    expect(container.textContent).toBe('');
  });
});
