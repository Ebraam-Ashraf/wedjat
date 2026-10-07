import { findSpec, type GpuSpec } from '../gpuSpecs';

export interface SmEstimateProps {
  name: string;
  vramBytes: number | null;
  util: number | null | undefined;
  compact?: boolean;
}

export interface ArchitectureIllustrationProps {
  className?: string;
}

export function SmEstimate({ name, vramBytes, util, compact = false }: SmEstimateProps) {
  const spec: GpuSpec | null = findSpec(name, vramBytes);
  if (!spec || !Number.isFinite(util ?? NaN)) {
    return (
      <p className="text-text-dim text-sm">
        SM count unavailable for this GPU · utilization bar only
      </p>
    );
  }

  const active = Math.max(0, Math.min(spec.sms, Math.round(((util ?? 0) / 100) * spec.sms)));

  return (
    <div className={compact ? 'text-xs' : ''}>
      {/* Header */}
      <div className="flex items-center justify-between mb-3">
        <strong className="font-mono text-xs uppercase tracking-wider">
          Estimated SM activity
        </strong>
        <span className="text-text-dim text-xs font-mono">
          {active} / {spec.sms} estimated active
        </span>
      </div>

      {/* SM grid */}
      <div
        className="sm-grid"
        aria-label={`${active} of ${spec.sms} SMs estimated active`}
      >
        {Array.from({ length: spec.sms }, (_, index) => (
          <span
            key={index}
            className={index < active ? 'sm-active' : 'sm-idle'}
            title={`SM ${index + 1}: ${index < active ? 'estimated active' : 'estimated idle'}`}
          />
        ))}
      </div>

      {/* Legend */}
      <div className="flex flex-wrap gap-4 mt-3">
        <span className="flex items-center gap-2 text-xs font-mono text-text-dim">
          <i className="sm-active inline-block w-3 h-3 rounded-xs" />
          Active estimate
        </span>
        <span className="flex items-center gap-2 text-xs font-mono text-text-dim">
          <i className="sm-idle inline-block w-3 h-3 rounded-xs" />
          Idle estimate
        </span>
        <small className="w-full text-text-dim text-xs mt-1">
          Estimated from whole-GPU utilization; no per-SM activity data is available.
        </small>
      </div>
    </div>
  );
}

export function ArchitectureIllustration({ className = '' }: ArchitectureIllustrationProps) {
  return (
    <details className={`glass-panel rounded-xl p-4 ${className}`}>
      <summary className="flex items-center justify-between cursor-pointer text-text-dim text-xs font-mono uppercase tracking-wider">
        SM architecture illustration
        <span className="ml-auto pl-4 text-text-dim text-xs font-mono normal-case tracking-normal">
          Static explainer · not live telemetry
        </span>
      </summary>
      <div className="grid grid-cols-4 gap-2 mt-4 sm:grid-cols-7">
        {['INT32', 'FP32 (1)', 'FP32 (2)', 'FP64', 'Tensor Core', 'SFU', 'LD / ST'].map(
          (unit, i) => (
            <div
              key={i}
              className="p-2 text-center text-xs font-mono border border-border rounded-md text-text-dim"
            >
              {unit}
            </div>
          ),
        )}
      </div>
      <p className="mt-3 text-text-dim text-xs leading-relaxed">
        This generic diagram explains common GPU execution units. It does not describe the
        selected GPU's exact layout or indicate which units are working.
      </p>
    </details>
  );
}

export default SmEstimate;
