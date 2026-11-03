import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { CutoverDialog } from './cutover-dialog';

const isDisabled = (element: HTMLElement) => element.hasAttribute('disabled');

describe('CutoverDialog', () => {
  it('enables Confirm only after every checklist item is checked', () => {
    const onConfirm = vi.fn();
    render(<CutoverDialog open busy={false} onCancel={vi.fn()} onConfirm={onConfirm} />);

    const confirm = screen.getByRole('button', { name: 'Cut over' });
    const boxes = screen.getAllByRole('checkbox');
    expect(boxes).toHaveLength(3);
    expect(isDisabled(confirm)).toBe(true);

    fireEvent.click(boxes[0]);
    fireEvent.click(boxes[1]);
    expect(isDisabled(confirm)).toBe(true);
    fireEvent.click(confirm);
    expect(onConfirm).not.toHaveBeenCalled();

    fireEvent.click(boxes[2]);
    expect(isDisabled(confirm)).toBe(false);

    fireEvent.click(boxes[1]);
    expect(isDisabled(confirm)).toBe(true);
    fireEvent.click(boxes[1]);

    fireEvent.click(confirm);
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it('keeps Confirm disabled while the cutover request is in flight', () => {
    render(<CutoverDialog open busy onCancel={vi.fn()} onConfirm={vi.fn()} />);
    screen.getAllByRole('checkbox').forEach((box) => fireEvent.click(box));
    expect(isDisabled(screen.getByRole('button', { name: 'Cut over' }))).toBe(true);
  });
});
