import { Component, type ReactNode, type ErrorInfo } from 'react';

interface Props {
  children: ReactNode;
  fallback?: ReactNode;
  onReset?: () => void;
}

interface State {
  hasError: boolean;
  error: Error | null;
}

export default class ErrorBoundary extends Component<Props, State> {
  constructor(props: Props) {
    super(props);
    this.state = { hasError: false, error: null };
  }

  static getDerivedStateFromError(error: Error): State {
    return { hasError: true, error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('ErrorBoundary caught:', error, info);
  }

  handleReset = () => {
    this.setState({ hasError: false, error: null });
    this.props.onReset?.();
  };

  render() {
    if (!this.state.hasError) return this.props.children;

    if (this.props.fallback) return <>{this.props.fallback}</>;

    return (
      <div
        className="glass-panel"
        style={{ padding: 24, color: 'var(--critical)', borderRadius: 16 }}
      >
        <h3 style={{ margin: '0 0 12px' }}>Something went wrong</h3>
        <p style={{ margin: 0, fontFamily: 'monospace', fontSize: '0.88rem' }}>
          {this.state.error?.message}
        </p>
        <button
          style={{
            marginTop: 12,
            padding: '6px 14px',
            border: '1px solid var(--critical)',
            borderRadius: 999,
            background: 'transparent',
            color: 'var(--critical)',
            fontFamily: 'monospace',
            fontSize: '0.82rem',
            cursor: 'pointer',
          }}
          onClick={this.handleReset}
        >
          Reset
        </button>
      </div>
    );
  }
}
