import { controlIcons } from "./Icon";
// Render failures and unavailable deployment chunks share explicit reload recovery.
import { Component } from "react";
import type { ReactNode } from "react";

/** A missing chunk during a deployment must not leave an empty workspace. */
export class ErrorBoundary extends Component<
  { children: ReactNode },
  { failed: boolean }
> {
  state = { failed: false };
  // Retain only failure state; private exception details are not rendered.
  static getDerivedStateFromError() {
    return { failed: true };
  }
  // Recovery reloads the deployment entry point instead of reusing a failed chunk.
  render() {
    if (this.state.failed)
      return (
        <main>
          <h1>The client could not load this view</h1>
          <p>The application may have been updated. Reload to continue.</p>
          <button onClick={() => location.reload()}>
            {controlIcons["refresh"]} Reload Catalog
          </button>
        </main>
      );
    return this.props.children;
  }
}
