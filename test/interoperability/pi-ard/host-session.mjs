// Use Pi's public extension loader, including its upstream compatibility view.
import assert from "node:assert/strict";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fauxProvider } from "/pi/node_modules/@earendil-works/pi-ai/dist/index.js";
import {
  createAgentSession,
  DefaultResourceLoader,
  getAgentDir,
  ModelRuntime,
  SessionManager,
  SettingsManager,
} from "/pi/node_modules/@earendil-works/pi-coding-agent/dist/index.js";

export async function piHarness({ agentDir, answers = [], hasUI = true }) {
  const previous = process.env.PI_CODING_AGENT_DIR;
  process.env.PI_CODING_AGENT_DIR = agentDir;
  assert.equal(getAgentDir(), agentDir);
  const cwd = mkdtempSync(join(tmpdir(), "oac-pi-cwd-"));
  const confirms = [];
  const notifications = [];
  const remaining = [...answers];
  const faux = fauxProvider({
    models: [
      { id: "faux-1", contextWindow: 100000, maxTokens: 4096, input: ["text"] },
    ],
  });
  const modelRuntime = await ModelRuntime.create({
    authPath: join(agentDir, "auth.json"),
    modelsPath: null,
    allowModelNetwork: false,
    refreshOnCreate: false,
  });
  modelRuntime.registerNativeProvider(faux.provider);
  const model =
    modelRuntime.getModel(faux.provider.id ?? "faux", "faux-1") ??
    faux.getModel();
  const settingsManager = SettingsManager.inMemory({});
  const resourceLoader = new DefaultResourceLoader({
    cwd,
    agentDir,
    settingsManager,
    noExtensions: true,
    noSkills: true,
    noPromptTemplates: true,
    noThemes: true,
    noContextFiles: true,
    additionalExtensionPaths: ["/pi/src/extension.ts"],
  });
  await resourceLoader.reload();
  const loaded = resourceLoader.getExtensions();
  assert.deepEqual(loaded.errors, []);
  assert.deepEqual(
    loaded.extensions.map((extension) => extension.path),
    ["/pi/src/extension.ts"],
  );
  const { session } = await createAgentSession({
    cwd,
    agentDir,
    modelRuntime,
    model,
    resourceLoader,
    settingsManager,
    sessionManager: SessionManager.inMemory(cwd),
  });
  await session.bindExtensions({
    mode: hasUI ? "rpc" : "print",
    uiContext: hasUI
      ? {
          select: async () => undefined,
          confirm: async (title, body) => {
            confirms.push({ title, body });
            assert.ok(remaining.length, "Unexpected native confirmation");
            return remaining.shift();
          },
          input: async () => undefined,
          notify: (message) => notifications.push(message),
          onTerminalInput: () => () => {},
          setStatus: () => {},
          setWorkingMessage: () => {},
          setWorkingVisible: () => {},
          setWorkingIndicator: () => {},
          setHiddenThinkingLabel: () => {},
          setWidget: () => {},
          setFooter: () => {},
          setHeader: () => {},
          setTitle: () => {},
          custom: async () => undefined,
          pasteToEditor: () => {},
          setEditorText: () => {},
          getEditorText: () => "",
          editor: async () => undefined,
          setEditorComponent: () => {},
          getAllThemes: () => [],
          getTheme: () => undefined,
          setTheme: () => ({ success: false }),
          getToolsExpanded: () => false,
          setToolsExpanded: () => {},
        }
      : undefined,
  });
  return {
    session,
    confirms,
    notifications,
    setResponses: (responses) => faux.setResponses(responses),
    dispose: () => {
      session.dispose();
      if (previous === undefined) delete process.env.PI_CODING_AGENT_DIR;
      else process.env.PI_CODING_AGENT_DIR = previous;
    },
  };
}
