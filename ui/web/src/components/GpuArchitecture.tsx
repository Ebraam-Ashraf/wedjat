// The architecture view is intentionally static. Retained as a named component
// for callers that used the previous dashboard component.
import { ArchitectureIllustration } from './SmEstimate';

export interface GpuArchitectureProps {
  className?: string;
}

export default function GpuArchitecture({ className }: GpuArchitectureProps) {
  return <ArchitectureIllustration className={className} />;
}
