import type { ReactNode } from 'react';
import ExpandMoreIcon from '@mui/icons-material/ExpandMore';
import { Accordion, AccordionDetails, AccordionSummary, Typography } from '@mui/material';

interface AdvancedSectionProps {
  title: string;
  helperText: string;
  /** Expanded initially, e.g. when a field inside has a validation error. */
  defaultExpanded?: boolean;
  children: ReactNode;
}

/** Collapsed-by-default section for optional engine tuning. */
export const AdvancedSection = ({ title, helperText, defaultExpanded, children }: AdvancedSectionProps) => (
  <Accordion variant="outlined" disableGutters defaultExpanded={defaultExpanded}>
    <AccordionSummary expandIcon={<ExpandMoreIcon />}>
      <Typography variant="subtitle1">{title}</Typography>
    </AccordionSummary>
    <AccordionDetails>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        {helperText}
      </Typography>
      {children}
    </AccordionDetails>
  </Accordion>
);
