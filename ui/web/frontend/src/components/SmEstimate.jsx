import React from 'react';
import { findSpec } from '../gpuSpecs';

export function SmEstimate({ name, vramBytes, util, compact = false }) {
  const spec = findSpec(name, vramBytes);
  if (!spec || !Number.isFinite(util)) return <div className="sm-unknown">SM count unavailable for this GPU · utilization bar only</div>;
  const active = Math.max(0, Math.min(spec.sms, Math.round((util / 100) * spec.sms)));
  return <div className={`sm-estimate ${compact ? 'sm-estimate-compact' : ''}`}>
    <div className="sm-estimate-head"><strong>Estimated SM activity</strong><span>{active} / {spec.sms} estimated active</span></div>
    <div className="sm-grid" aria-label={`${active} of ${spec.sms} SMs estimated active`}>
      {Array.from({ length: spec.sms }, (_, index) => <span key={index} className={index < active ? 'sm-active' : 'sm-idle'} title={`SM ${index + 1}: ${index < active ? 'estimated active' : 'estimated idle'}`} />)}
    </div>
    <div className="sm-legend"><span><i className="sm-active" />Active estimate</span><span><i className="sm-idle" />Idle estimate</span><small>Estimated from whole-GPU utilization; no per-SM activity data is available.</small></div>
  </div>;
}

export function ArchitectureIllustration() {
  return <details className="panel architecture-note">
    <summary>SM architecture illustration <span>Static explainer · not live telemetry</span></summary>
    <div className="architecture-grid" aria-label="Static illustration of common SM execution units">
      {['INT32', 'FP32 (1)', 'FP32 (2)', 'FP64', 'Tensor Core', 'SFU', 'LD / ST'].map((unit, i) => <div key={i}>{unit}</div>)}
    </div>
    <p>This generic diagram explains common GPU execution units. It does not describe the selected GPU’s exact layout or indicate which units are working.</p>
  </details>;
}
