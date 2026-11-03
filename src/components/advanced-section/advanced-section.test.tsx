import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import { AdvancedSection } from './advanced-section';

const summary = () => screen.getByRole('button', { name: /advanced/i });

afterEach(cleanup);

describe('AdvancedSection', () => {
  it('starts collapsed with a visible expand indicator and expands on click', () => {
    render(
      <AdvancedSection title="Advanced" helperText="Tuning">
        content
      </AdvancedSection>,
    );

    expect(summary().getAttribute('aria-expanded')).toBe('false');
    expect(screen.getByTestId('ExpandMoreIcon')).toBeTruthy();

    fireEvent.click(summary());
    expect(summary().getAttribute('aria-expanded')).toBe('true');
  });
});
