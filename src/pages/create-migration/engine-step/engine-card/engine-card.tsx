import { Card, CardActionArea, CardContent, Typography } from '@mui/material';
import type { Engine } from '../../../../engines/engine.types';

interface EngineCardProps {
  engine: Engine;
  selected: boolean;
  onSelect: (engine: Engine) => void;
}

export const EngineCard = ({ engine, selected, onSelect }: EngineCardProps) => (
  <Card variant="outlined" sx={{ borderColor: selected ? 'primary.main' : 'divider', borderWidth: selected ? 2 : 1 }}>
    <CardActionArea onClick={() => onSelect(engine)} aria-pressed={selected} sx={{ height: '100%' }}>
      <CardContent>
        <Typography variant="subtitle1" gutterBottom>
          {engine.label}
        </Typography>
        <Typography variant="body2" color="text.secondary">
          {engine.description}
        </Typography>
      </CardContent>
    </CardActionArea>
  </Card>
);
