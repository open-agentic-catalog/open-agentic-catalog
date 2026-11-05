// Bootstrap from the host's public runtime settings before mounting any authenticated workspace.
import { createRoot } from "react-dom/client";
import { Bootstrap } from "./shell/Bootstrap";
import "./styles.css";

const root = createRoot(document.getElementById("root")!);
// Built entry chunks live in assets/; this works on root and nested deployments
// without placing an internal upstream address in the JavaScript bundle.
root.render(
  <Bootstrap
    url={new URL(/* @vite-ignore */ "../runtime.json", import.meta.url)}
  />,
);
