import React from 'react';

// Without this, any render-time throw unmounts the whole tree and the browser
// shows a blank page with the real cause only in the console.
class ErrorBoundary extends React.Component {
  constructor(props) {
    super(props);
    this.state = { error: null };
  }

  static getDerivedStateFromError(error) {
    return { error };
  }

  componentDidCatch(error, info) {
    console.error('UI render error:', error, info.componentStack);
  }

  render() {
    if (this.state.error) {
      return (
        <div style={{ padding: '48px', maxWidth: '900px', margin: '0 auto' }}>
          <div className="glass-panel" style={{ padding: 24, borderColor: 'var(--accent-danger)' }}>
            <h2 style={{ margin: '0 0 12px', color: 'var(--accent-danger)' }}>Something broke while rendering</h2>
            <p style={{ fontFamily: 'monospace', color: 'var(--text-muted)', margin: 0 }}>
              {this.state.error.message}
            </p>
            <button className="btn" style={{ marginTop: 20 }} onClick={() => this.setState({ error: null })}>
              Retry
            </button>
          </div>
        </div>
      );
    }
    return this.props.children;
  }
}

export default ErrorBoundary;