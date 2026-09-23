import { clampUpdatePercent } from "../../../shared/updater-types";

export function UpdateProgressBar({
  percent,
  label,
}: {
  percent: number;
  label: string;
}) {
  const shown = Math.round(clampUpdatePercent(percent));
  return (
    <div className="mt-2">
      <div
        role="progressbar"
        aria-valuenow={shown}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-label={label}
        className="h-1.5 w-full overflow-hidden rounded-full bg-muted"
      >
        <div className="h-full bg-primary" style={{ width: `${shown}%` }} />
      </div>
      <p className="mt-1 text-caption tabular-nums text-muted-foreground">{shown}%</p>
    </div>
  );
}
