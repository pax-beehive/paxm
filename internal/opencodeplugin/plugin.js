import { spawnSync } from "node:child_process";
import { writeFileSync } from "node:fs";

const paxmBinary = process.env.PAXM_BINARY || "paxm";
const paxmConfig = process.env.PAXM_CONFIG || "";
const recallEnabled = process.env.PAXM_OPENCODE_RECALL !== "0";
const writeEnabled = process.env.PAXM_OPENCODE_WRITE === "1";
const recallMarker = process.env.PAXM_OPENCODE_RECALL_MARKER || "";
const pending = new Map();
const lastFlushedMessage = new Map();

function textOf(parts) {
  return (parts ?? []).filter((part) => part?.type === "text" && !part.synthetic && !part.ignored)
    .map((part) => typeof part.text === "string" ? part.text.trim() : "").filter(Boolean).join("\n\n").trim();
}

function runHook(event, payload) {
  const args = paxmConfig ? ["--config", paxmConfig] : [];
  args.push("__hook", "--target", "opencode", "--event", event, "--json");
  const result = spawnSync(paxmBinary, args, {input:JSON.stringify(payload)+"\n",encoding:"utf8",timeout:5000,maxBuffer:1024*1024});
  return {ok: !result.error && result.status === 0, stdout: result.stdout ?? ""};
}

function formatRecall(raw) {
  try {
    const value = JSON.parse(raw || "{}");
    const lines = (value?.recall?.hits ?? []).map((hit) => String(hit?.text ?? "").trim()).filter(Boolean);
    return lines.length ? '<paxm-recall version="1" mode="passive">\nRelevant memory recalled by paxm:\n' + lines.join("\n\n---\n\n") + '\n</paxm-recall>' : "";
  } catch { return ""; }
}

export const PaxmPlugin = async ({client, directory, worktree}) => ({
  "chat.message": async (input, output) => {
    if (!recallEnabled) return;
    const prompt = textOf(output.parts);
    if (!prompt) return;
    const marker = "Question:";
    const markerIndex = prompt.lastIndexOf(marker);
    const query = markerIndex >= 0 ? prompt.slice(markerIndex + marker.length).trim() : prompt;
    const result = runHook("user_input", {schema_version:"paxm.opencode.user_input.v1",target:"opencode",event:"user_input",agent:"opencode",session_id:input.sessionID,workspace:worktree||directory,prompt:query,source:"opencode"});
    if (!result.ok) return;
    const value = formatRecall(result.stdout);
    if (value) {
      pending.set(input.sessionID, value);
      if (recallMarker) try { writeFileSync(recallMarker, "1"); } catch {}
    }
  },
  "experimental.chat.messages.transform": async (_input, output) => {
    for (let index = output.messages.length - 1; index >= 0; index--) {
      const message = output.messages[index];
      if (message?.info?.role !== "user") continue;
      const value = pending.get(String(message.info.sessionID ?? ""));
      if (!value) return;
      pending.delete(String(message.info.sessionID ?? ""));
      const part = message.parts.find((candidate) => candidate?.type === "text" && !candidate.synthetic && !candidate.ignored);
      if (part && typeof part.text === "string") part.text += "\n\n" + value;
      return;
    }
  },
  event: async ({event}) => {
    if (!writeEnabled || event.type !== "session.idle") return;
    const sessionID = String(event.properties?.sessionID ?? "");
    if (!sessionID) return;
    try {
      const response = await client.session.messages({path:{id:sessionID},query:{directory,limit:30}});
      const history = Array.isArray(response.data) ? response.data : [];
      let userIndex = -1;
      for (let index = history.length - 1; index >= 0; index--) if (history[index]?.info?.role === "user") { userIndex=index; break; }
      if (userIndex < 0) return;
      const turn = history.slice(userIndex);
      const assistant = [...turn].reverse().find((message) => message?.info?.role === "assistant");
      const flushID = String(assistant?.info?.id ?? turn[turn.length-1]?.info?.id ?? "");
      if (!flushID || lastFlushedMessage.get(sessionID) === flushID) return;
      const messages = turn.map((message) => ({role:String(message?.info?.role ?? "unknown"),text:textOf(message?.parts ?? []),source:"session.idle"}))
        .filter((message) => (message.role === "user" || message.role === "assistant") && message.text);
      if (!messages.length) return;
      const result = runHook("turn_end", {schema_version:"paxm.opencode.turn_end.v1",target:"opencode",event:"turn_end",agent:"opencode",session_id:sessionID,workspace:worktree||directory,prompt:messages.find((message)=>message.role==="user")?.text??"",source:"opencode",trigger_event:"session.idle",messages,metadata:{message_count:String(messages.length)}});
      if (result.ok) lastFlushedMessage.set(sessionID, flushID);
    } catch {}
  }
});
