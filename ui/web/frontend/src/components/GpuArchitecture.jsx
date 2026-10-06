// The architecture view is intentionally static. It is retained as a named
// component for callers that used the previous dashboard component.
import React from 'react';
import { ArchitectureIllustration } from './SmEstimate';

export default function GpuArchitecture() {
  return <ArchitectureIllustration />;
}
