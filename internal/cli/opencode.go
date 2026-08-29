package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/pax-beehive/paxm/internal/config"
)

func openCodeConfigDir() string {
	if path := strings.TrimSpace(os.Getenv("PAXM_OPENCODE_CONFIG_DIR")); path != "" {
		return config.ExpandPath(path)
	}
	if path := strings.TrimSpace(os.Getenv("OPENCODE_CONFIG_DIR")); path != "" {
		return config.ExpandPath(path)
	}
	if path := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); path != "" {
		return filepath.Join(config.ExpandPath(path), "opencode")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".config", "opencode")
	}
	return filepath.Join(home, ".config", "opencode")
}

func openCodePluginPath() string {
	return filepath.Join(openCodeConfigDir(), "plugins", "paxm.ts")
}

func installOpenCodeGlobalHook(path string, scriptPaths map[string]string) error {
	sessionStartScriptPath := strings.TrimSpace(scriptPaths["session_start"])
	userInputScriptPath := strings.TrimSpace(scriptPaths["user_input"])
	turnEndScriptPath := strings.TrimSpace(scriptPaths["turn_end"])
	if sessionStartScriptPath == "" && userInputScriptPath == "" && turnEndScriptPath == "" {
		return errors.New("OpenCode plugin requires at least one hook shim")
	}
	path = config.ExpandPath(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(openCodePluginSource(sessionStartScriptPath, userInputScriptPath, turnEndScriptPath)), 0o600)
}

func openCodePluginSource(sessionStartScriptPath, userInputScriptPath, turnEndScriptPath string) string {
	sessionStartScriptLiteral := jsonStringLiteral(config.ExpandPath(sessionStartScriptPath))
	userInputScriptLiteral := jsonStringLiteral(config.ExpandPath(userInputScriptPath))
	turnEndScriptLiteral := jsonStringLiteral(config.ExpandPath(turnEndScriptPath))
	return `import type { Plugin } from "@opencode-ai/plugin";
import { spawnSync } from "node:child_process";

const paxmSessionStartHookCommand = ` + sessionStartScriptLiteral + `;
const paxmUserInputHookCommand = ` + userInputScriptLiteral + `;
const paxmTurnEndHookCommand = ` + turnEndScriptLiteral + `;
const pendingRecall = new Map<string, string>();
const lastFlushedMessage = new Map<string, string>();
const startedSessions = new Set<string>();

type OpenCodePart = {
  id?: string;
  sessionID?: string;
  messageID?: string;
  type?: string;
  text?: string;
  synthetic?: boolean;
  ignored?: boolean;
};

function partText(parts: OpenCodePart[]): string {
  return parts
    .filter((part) => part?.type === "text" && !part.synthetic && !part.ignored)
    .map((part) => typeof part.text === "string" ? part.text.trim() : "")
    .filter(Boolean)
    .join("\n\n")
    .trim();
}

function runHook(command: string, payload: unknown, workspace: string): { ok: boolean; stdout: string } {
  if (command === "") return { ok: true, stdout: "" };
  try {
	    const result = spawnSync(command, [], {
	      input: JSON.stringify(payload) + "\n",
	      encoding: "utf8",
	      maxBuffer: 1024 * 1024,
	      cwd: workspace,
      timeout: 2_000,
    });
	    const stdout = result.stdout ?? "";
	    if (result.error) {
	      console.warn("paxm OpenCode hook failed:", result.error);
	      return { ok: false, stdout };
	    }
	    if (result.status !== 0) {
	      const stderr = (result.stderr ?? "").trim();
	      console.warn("paxm OpenCode hook failed:", stderr || stdout.trim() || ` + "`exit ${result.status}`" + `);
      return { ok: false, stdout };
    }
    return { ok: true, stdout };
  } catch (error) {
    console.warn("paxm OpenCode hook failed:", error);
    return { ok: false, stdout: "" };
  }
}

function escapeRecallText(text: string): string {
  return text
    .split("</paxm-recall>").join("&lt;/paxm-recall&gt;")
    .split("<paxm-recall").join("&lt;paxm-recall");
}

function formatRecall(raw: string): string {
  if (raw.trim() === "") return "";
  try {
    const result = JSON.parse(raw);
	const contexts: string[] = [];
	if (typeof result?.additional_context === "string" && result.additional_context.trim() !== "") {
	  contexts.push(result.additional_context.trim());
	}
	if (result?.skipped || !Array.isArray(result?.recall?.hits) || result.recall.hits.length === 0) return contexts.join("\n\n");
    const lines = ["Relevant memory recalled by paxm:"];
    for (const hit of result.recall.hits) {
      const text = escapeRecallText(String(hit?.text ?? "").trim());
      if (text === "") continue;
      const provider = String(hit?.provider ?? "unknown");
      const score = typeof hit?.score === "number" ? hit.score.toFixed(4) : "n/a";
      lines.push(` + "`- [${provider} score=${score}] ${text}`" + `);
    }
	if (lines.length > 1) contexts.push('<paxm-recall version="1" mode="passive">\n' + lines.join("\n") + '\n</paxm-recall>');
	return contexts.join("\n\n");
  } catch {
    const text = escapeRecallText(raw.trim());
    if (text === "" || text.includes("<paxm-recall") || text.includes("<paxm-local-time") || text.includes("<paxm-session-identity")) return text;
    return '<paxm-recall version="1" mode="passive">\n' + text + '\n</paxm-recall>';
  }
}

function injectRecall(messages: Array<{ info: any; parts: OpenCodePart[] }>): void {
  for (let index = messages.length - 1; index >= 0; index--) {
    const message = messages[index];
    if (message?.info?.role !== "user") continue;
    const sessionID = String(message.info.sessionID ?? "");
    const recall = pendingRecall.get(sessionID);
    if (!recall) return;
    pendingRecall.delete(sessionID);
    const textPart = message.parts.find((part) => part?.type === "text" && !part.synthetic && !part.ignored);
    if (!textPart || typeof textPart.text !== "string") return;
    textPart.text += "\n\n" + recall;
    return;
  }
}

export const PaxmPlugin: Plugin = async ({ client, directory, worktree }) => ({
  "chat.message": async (input, output) => {
    const prompt = partText(output.parts as OpenCodePart[]);
    if (prompt === "") return;
    const workspace = worktree || directory;
    const contexts: string[] = [];
    if (!startedSessions.has(input.sessionID)) {
      startedSessions.add(input.sessionID);
      const started = runHook(paxmSessionStartHookCommand, {
        schema_version: "paxm.opencode.session_start.v1",
        target: "opencode",
        event: "session_start",
        agent: "opencode",
        session_id: input.sessionID,
        cwd: directory,
        workspace,
        source: "opencode",
      }, workspace);
      if (started.ok && started.stdout.trim() !== "") contexts.push(started.stdout.trim());
    }
    if (paxmUserInputHookCommand === "") {
      if (contexts.length > 0) pendingRecall.set(input.sessionID, contexts.join("\n\n"));
      return;
    }
    const payload = {
      schema_version: "paxm.opencode.user_input.v1",
      target: "opencode",
      event: "user_input",
      agent: "opencode",
      session_id: input.sessionID,
      cwd: directory,
      workspace,
      prompt,
      source: "opencode",
    };
    const result = runHook(paxmUserInputHookCommand, payload, workspace);
    if (result.ok) {
      const recall = formatRecall(result.stdout);
      if (recall !== "") contexts.push(recall);
    }
    if (contexts.length > 0) pendingRecall.set(input.sessionID, contexts.join("\n\n"));
  },

  "experimental.chat.messages.transform": async (_input, output) => {
    injectRecall(output.messages as Array<{ info: any; parts: OpenCodePart[] }>);
  },

  event: async ({ event }) => {
    if (event.type !== "session.idle" || paxmTurnEndHookCommand === "") return;
    const sessionID = String(event.properties?.sessionID ?? "");
    if (sessionID === "") return;
    try {
      const response = await client.session.messages({
        path: { id: sessionID },
        query: { directory, limit: 30 },
      });
      const history = Array.isArray(response.data) ? response.data : [];
      let userIndex = -1;
      for (let index = history.length - 1; index >= 0; index--) {
        if (history[index]?.info?.role === "user") { userIndex = index; break; }
      }
      if (userIndex < 0) return;
      const turn = history.slice(userIndex);
      const assistant = [...turn].reverse().find((message) => message?.info?.role === "assistant");
      const flushID = String(assistant?.info?.id ?? turn[turn.length - 1]?.info?.id ?? "");
      if (flushID === "" || lastFlushedMessage.get(sessionID) === flushID) return;
      const messages = turn
        .map((message) => ({
          role: String(message?.info?.role ?? "unknown"),
          text: partText((message?.parts ?? []) as OpenCodePart[]),
          source: "session.idle",
        }))
        .filter((message) => (message.role === "user" || message.role === "assistant") && message.text !== "");
      if (messages.length === 0) return;
      const prompt = messages.find((message) => message.role === "user")?.text ?? "";
      const workspace = worktree || directory;
      const result = runHook(paxmTurnEndHookCommand, {
        schema_version: "paxm.opencode.turn_end.v1",
        target: "opencode",
        event: "turn_end",
        agent: "opencode",
        session_id: sessionID,
        cwd: directory,
        workspace,
        prompt,
        source: "opencode",
        trigger_event: "session.idle",
        messages,
        metadata: { message_count: String(messages.length) },
      }, workspace);
      if (result.ok) lastFlushedMessage.set(sessionID, flushID);
    } catch (error) {
      console.warn("paxm OpenCode session flush failed:", error);
    }
  },
});
`
}
