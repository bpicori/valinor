const state = {
  sessions: [],
  activeSessionId: "",
  selectedSessionId: "",
  isSelectedSessionActive: false,
  messages: [],
  providers: [],
  models: [],
  currentProvider: "",
  currentModel: "",
};

const sessionsEl = document.getElementById("sessions");
const messagesEl = document.getElementById("messages");
const statusEl = document.getElementById("status");
const composerEl = document.getElementById("composer");
const inputEl = document.getElementById("input");
const newConversationEl = document.getElementById("new-conversation");
const providerSelectEl = document.getElementById("provider-select");
const modelSelectEl = document.getElementById("model-select");

async function requestJSON(url, options = {}) {
  const res = await fetch(url, options);
  const body = await res.json().catch(() => ({}));
  if (!res.ok) {
    throw new Error(body.error || `request failed (${res.status})`);
  }
  return body;
}

function renderSessions() {
  sessionsEl.innerHTML = "";
  for (const session of state.sessions) {
    const item = document.createElement("div");
    item.className = "session-item";
    if (session.id === state.selectedSessionId) {
      item.classList.add("active");
    }
    item.innerHTML = `
      <div class="session-title">${session.title || session.id.slice(0, 8)}</div>
      <div class="session-time">${new Date(session.updated_at).toLocaleString()}</div>
    `;
    item.addEventListener("click", () => {
      selectSession(session.id);
    });
    sessionsEl.appendChild(item);
  }
}

function renderMessages() {
  messagesEl.innerHTML = "";
  for (const msg of state.messages) {
    const row = document.createElement("div");
    row.className = `msg ${msg.role === "user" ? "user" : "assistant"}`;

    if (msg.role === "assistant" && Array.isArray(msg.tool_calls)) {
      for (const tc of msg.tool_calls) {
        const tcEl = document.createElement("div");
        tcEl.className = "tool-call";
        tcEl.textContent = `-> ${tc.name}${tc.args ? ` ${tc.args}` : ""}${tc.done ? " (done)" : ""}`;
        row.appendChild(tcEl);
      }
    }

    const body = document.createElement("div");
    body.textContent = msg.error ? `Error: ${msg.error}` : msg.content || "";
    row.appendChild(body);
    messagesEl.appendChild(row);
  }
  messagesEl.scrollTop = messagesEl.scrollHeight;
}

function ensureAssistantMessage() {
  const last = state.messages[state.messages.length - 1];
  if (!last || last.role !== "assistant") {
    state.messages.push({ role: "assistant", content: "", tool_calls: [], streaming: true });
  }
  return state.messages[state.messages.length - 1];
}

function applyRealtimeEvent(event) {
  switch (event.type) {
    case "user_message":
      state.messages.push({ role: "user", content: event.payload.content });
      state.messages.push({ role: "assistant", content: "", tool_calls: [], streaming: true });
      break;
    case "new_conversation":
      state.messages = [];
      break;
    case "agent_content_delta":
    case "agent_refusal": {
      const assistant = ensureAssistantMessage();
      assistant.content = (assistant.content || "") + (event.payload.content || "");
      break;
    }
    case "agent_tool_call_start": {
      const assistant = ensureAssistantMessage();
      assistant.tool_calls = assistant.tool_calls || [];
      assistant.tool_calls.push({
        id: event.payload.tool_call_id,
        name: event.payload.tool_name,
        args: event.payload.tool_args,
        done: false,
      });
      break;
    }
    case "agent_tool_call_done": {
      const assistant = ensureAssistantMessage();
      assistant.tool_calls = assistant.tool_calls || [];
      for (const call of assistant.tool_calls) {
        if (call.id === event.payload.tool_call_id && !call.done) {
          call.result = event.payload.tool_result;
          call.done = true;
          break;
        }
      }
      break;
    }
    case "agent_done": {
      const assistant = ensureAssistantMessage();
      assistant.streaming = false;
      break;
    }
    case "agent_error": {
      const assistant = ensureAssistantMessage();
      assistant.streaming = false;
      assistant.error = event.payload.error || "unknown error";
      break;
    }
    case "change_provider_model": {
      state.currentProvider = event.payload?.provider || "";
      state.currentModel = event.payload?.model || "";
      renderModelSelector();
      break;
    }
    default:
      break;
  }
  renderMessages();
}

async function loadSessions() {
  const [sessionsData, activeData, providersData] = await Promise.all([
    requestJSON("/api/sessions"),
    requestJSON("/api/active-session"),
    requestJSON("/api/providers"),
  ]);
  state.sessions = sessionsData.sessions || [];
  state.activeSessionId = activeData.id;
  state.providers = providersData.providers || [];
  if (!state.selectedSessionId) {
    state.selectedSessionId = state.activeSessionId || state.sessions[0]?.id || "";
  }
  renderSessions();
  renderModelSelector();
}

function renderModelSelector() {
  providerSelectEl.innerHTML = "";
  modelSelectEl.innerHTML = "";
  for (const p of state.providers) {
    const opt = document.createElement("option");
    opt.value = p;
    opt.textContent = p;
    if (p === state.currentProvider) opt.selected = true;
    providerSelectEl.appendChild(opt);
  }
  for (const m of state.models) {
    const opt = document.createElement("option");
    opt.value = m.id;
    opt.textContent = m.display_name || m.id;
    if (m.id === state.currentModel) opt.selected = true;
    modelSelectEl.appendChild(opt);
  }
  providerSelectEl.disabled = !state.isSelectedSessionActive;
  modelSelectEl.disabled = !state.isSelectedSessionActive;
}

async function loadModelsForProvider(provider) {
  try {
    const data = await requestJSON(`/api/providers/${provider}/models`);
    state.models = data.models || [];
  } catch (_) {
    state.models = [];
  }
  renderModelSelector();
}

async function changeSessionModel(provider, model) {
  if (!state.selectedSessionId || !state.isSelectedSessionActive) return;
  if (provider !== state.currentProvider) {
    await createSession(provider, model);
    return;
  }
  try {
    await requestJSON(`/api/sessions/${state.selectedSessionId}/model`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ provider, model }),
    });
    state.currentProvider = provider;
    state.currentModel = model;
  } catch (err) {
    statusEl.textContent = err.message;
    renderModelSelector();
    return;
  }
  renderModelSelector();
}

async function createSession(provider, model) {
  try {
    const data = await requestJSON("/api/sessions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ provider, model }),
    });
    const newSessionId = data?.id;
    if (newSessionId) {
      await loadSessions();
      await selectSession(newSessionId);
    }
  } catch (err) {
    statusEl.textContent = err.message;
    renderModelSelector();
  }
}

async function selectSession(sessionId) {
  if (!sessionId) {
    return;
  }
  state.selectedSessionId = sessionId;
  const [messagesData, modelData] = await Promise.all([
    requestJSON(`/api/sessions/${sessionId}/messages`),
    requestJSON(`/api/sessions/${sessionId}/model`),
  ]);
  state.messages = messagesData.messages || [];
  state.isSelectedSessionActive = !!messagesData.active;
  state.currentProvider = modelData.provider || "";
  state.currentModel = modelData.model || "";
  inputEl.disabled = !state.isSelectedSessionActive;
  newConversationEl.disabled = !state.isSelectedSessionActive;
  if (state.currentProvider) {
    await loadModelsForProvider(state.currentProvider);
  } else {
    state.models = [];
    renderModelSelector();
  }
  renderSessions();
  renderMessages();
}

function connectWebSocket() {
  const protocol = location.protocol === "https:" ? "wss" : "ws";
  const ws = new WebSocket(`${protocol}://${location.host}/ws`);

  ws.onopen = () => {
    statusEl.textContent = "connected";
  };
  ws.onclose = () => {
    statusEl.textContent = "disconnected";
    setTimeout(connectWebSocket, 1000);
  };
  ws.onerror = () => {
    statusEl.textContent = "connection error";
  };
  ws.onmessage = (msg) => {
    if (!state.isSelectedSessionActive) {
      return;
    }
    try {
      const event = JSON.parse(msg.data);
      applyRealtimeEvent(event);
    } catch (_) {
      // Ignore malformed events.
    }
  };
}

composerEl.addEventListener("submit", async (e) => {
  e.preventDefault();
  const content = inputEl.value.trim();
  if (!content || !state.selectedSessionId || !state.isSelectedSessionActive) {
    return;
  }
  await requestJSON(`/api/sessions/${state.selectedSessionId}/messages`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ content }),
  });
  inputEl.value = "";
});

newConversationEl.addEventListener("click", async () => {
  if (!state.selectedSessionId || !state.isSelectedSessionActive) {
    return;
  }
  await requestJSON(`/api/sessions/${state.selectedSessionId}/new`, {
    method: "POST",
  });
});

providerSelectEl.addEventListener("change", async () => {
  const provider = providerSelectEl.value;
  if (!provider) return;
  await loadModelsForProvider(provider);
  const model = modelSelectEl.value || (state.models[0]?.id || "");
  if (model && state.isSelectedSessionActive) {
    await changeSessionModel(provider, model);
  }
});

modelSelectEl.addEventListener("change", async () => {
  const provider = providerSelectEl.value;
  const model = modelSelectEl.value;
  if (provider && model && state.isSelectedSessionActive) {
    await changeSessionModel(provider, model);
  }
});

async function boot() {
  await loadSessions();
  await selectSession(state.selectedSessionId);
  connectWebSocket();
}

boot().catch((err) => {
  statusEl.textContent = err.message;
});
