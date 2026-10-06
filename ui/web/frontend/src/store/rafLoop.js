// One requestAnimationFrame loop shared by every chart on the page.
//
// This is the whole point of the refactor: the data arrives at 2Hz but the
// drawing happens at display rate, so the line glides instead of stepping.
// Multiple charts subscribe to a single loop rather than each starting their
// own, which keeps them frame-locked to each other.
//
// The loop parks itself while the tab is hidden. Because the renderer is
// stateless per frame (it reads the current store and draws the current time
// window) there is no queue of missed frames to replay, so resuming cannot
// produce a catch-up burst.

class RafLoop {
  constructor() {
    this.subs = new Set();
    this.rafId = 0;
    this.fps = 0;
    this.frames = 0;
    this._fpsWindowStart = performance.now();
    this._running = false;
    this._onVisibility = () => this._sync();
    document.addEventListener('visibilitychange', this._onVisibility);
  }

  /** Register a per-frame callback. Returns an unsubscribe function. */
  add(fn) {
    this.subs.add(fn);
    this._sync();
    return () => {
      this.subs.delete(fn);
      this._sync();
    };
  }

  _sync() {
    const shouldRun = !document.hidden && this.subs.size > 0;
    if (shouldRun && !this._running) {
      this._running = true;
      this._start();
    } else if (!shouldRun && this._running) {
      this._running = false;
      if (this.rafId) cancelAnimationFrame(this.rafId);
      this.rafId = 0;
    }
  }

  _start() {
    // Restart the fps window so a resume does not report a bogus average built
    // from the idle period.
    this.frames = 0;
    this._fpsWindowStart = performance.now();
    this._tick(performance.now());
  }

  _tick = (now) => {
    if (!this._running) return;
    this.rafId = requestAnimationFrame(this._tick);

    this.frames++;
    const span = now - this._fpsWindowStart;
    if (span >= 1000) {
      this.fps = (this.frames * 1000) / span;
      this.frames = 0;
      this._fpsWindowStart = now;
    }

    for (const fn of this.subs) {
      try {
        fn(now);
      } catch (err) {
        // One bad subscriber must not stop the loop for every other chart.
        console.error('rAF subscriber failed:', err);
      }
    }
  };
}

export const rafLoop = new RafLoop();