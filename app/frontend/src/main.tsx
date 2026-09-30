// @wailsio/runtime must load for file drops to reach Go: a drop round-trips
// through window._wails, and without the runtime nothing fires and nothing
// errors. (Measured on GNOME Wayland; see CLAUDE.md.)
import "@wailsio/runtime";
import React from "react";
import ReactDOM from "react-dom/client";
import "../../../web/src/gloam.css";
import "./app.css";
import App from "./App";

ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
