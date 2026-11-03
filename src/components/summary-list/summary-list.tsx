import { Box, Typography } from '@mui/material';
import type { OptionSummaryItem } from '../../engines/engine.types';

interface SummaryListProps {
  items: OptionSummaryItem[];
}

export const SummaryList = ({ items }: SummaryListProps) => (
  <Box
    component="dl"
    sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', sm: 'minmax(160px, max-content) 1fr' }, columnGap: 3, rowGap: 1, m: 0 }}
  >
    {items.map(({ label, value }) => (
      <Box key={label} sx={{ display: 'contents' }}>
        <Typography component="dt" variant="body2" color="text.secondary">
          {label}
        </Typography>
        <Typography component="dd" variant="body2" sx={{ m: 0, wordBreak: 'break-word' }}>
          {value}
        </Typography>
      </Box>
    ))}
  </Box>
);
