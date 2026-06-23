import React from 'react';
import { createRoot } from 'react-dom/client';
import './styles.css';

function App() {
  return (
    <main className="shell">
      <section className="panel">
        <p className="eyebrow">Phlox-GW</p>
        <h1>Gateway control plane</h1>
        <p>
          The embedded production dashboard is served from <code>frontend/dist</code>.
          This React/Vite scaffold is ready for the richer admin UI.
        </p>
      </section>
    </main>
  );
}

createRoot(document.getElementById('root')).render(<App />);

