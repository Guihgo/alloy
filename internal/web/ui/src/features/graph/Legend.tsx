import { FeedDataTypeColorMap } from './feedDataType';

import styles from './Legend.module.css';

export const Legend: React.FC = () => {
  return (
    <div className={styles.legend}>
      {Object.entries(FeedDataTypeColorMap)
        .filter(([key, color]) => key !== 'undefined')
        .map(([key, color]) => (
          <div key={key} className={styles.legendItem}>
            <div className={styles.colorBox} style={{ backgroundColor: color }}></div>
            {key}
          </div>
        ))}
    </div>
  );
};
