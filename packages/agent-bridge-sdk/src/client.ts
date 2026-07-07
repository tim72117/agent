import type {
  AckPayload,
  AssistantMessagePayload,
  Envelope,
  ErrorPayload,
  ToolCallPayload,
  ToolResultPayload,
} from "./protocol";

const SDK_VERSION = "0.1.0";

/** A tool handler the developer registers to fulfill a named tool call. */
export type ToolHandler = (args: any) => Promise<unknown> | unknown;

export interface AgentBridgeOptions {
  /** WebSocket endpoint, e.g. "wss://agent.example.com/ws". */
  url: string;
  /** The developer app ID whose tool set this session should load. */
  appId: string;
  /**
   * API key issued for this app (see backend's `genkey` command). Sent as a
   * `token` query parameter on the WebSocket handshake — browsers cannot
   * attach custom headers to a WebSocket upgrade request, so this is the
   * only place it can travel. The backend verifies it and resolves the
   * connection's appId from it server-side; when set, this always overrides
   * whatever `appId` is passed above for authorization purposes; unset
   * connects in the backend's dev/no-auth mode if it's configured to allow
   * that. Because the key rides in the URL, only ever connect over wss:// —
   * plain ws:// puts it on the wire (and often in server access logs) in
   * plaintext.
   */
  apiKey?: string;
  /**
   * Tool handlers keyed by tool name. Only names the backend already knows
   * about (from the app's tool definitions) will ever be invoked, but the
   * SDK also refuses to call anything not present in this map — the
   * front-end never executes arbitrary/unregistered actions.
   */
  tools: Record<string, ToolHandler>;
  /** Called for natural-language messages meant for display to the user. */
  onAssistantMessage?: (text: string) => void;
  /** Called on protocol/inference errors not tied to a specific call. */
  onError?: (err: ErrorPayload) => void;
  /** Reconnect backoff bounds, in ms. Defaults: 500ms .. 10s. */
  minBackoffMs?: number;
  maxBackoffMs?: number;
  /**
   * HTTP endpoint to fire a best-effort `sendBeacon` to when the page is
   * hidden/unloaded with unsent queued messages. The WebSocket connection
   * closes before a final in-flight send would complete, so this mirrors
   * the beacon fallback pattern analytics SDKs use for the same reason.
   * Omit to skip this fallback entirely.
   */
  beaconUrl?: string;
}

type QueuedSend = { type: string; requestId?: string; payload?: unknown };

/**
 * Browser-side bridge between a page and the agent-tool-platform backend.
 *
 * Modeled after gtag.js's stub-function-plus-queue pattern: calls made
 * before the socket is open (or during a reconnect) are buffered and
 * flushed once the connection is ready, so callers never have to check a
 * "ready" flag themselves.
 */
export class AgentBridge {
  private ws: WebSocket | null = null;
  private queue: QueuedSend[] = [];
  private ready = false;
  private closedByUser = false;
  private backoffMs: number;

  private readonly minBackoffMs: number;
  private readonly maxBackoffMs: number;

  constructor(private readonly opts: AgentBridgeOptions) {
    this.minBackoffMs = opts.minBackoffMs ?? 500;
    this.maxBackoffMs = opts.maxBackoffMs ?? 10_000;
    this.backoffMs = this.minBackoffMs;
    this.installUnloadFallback();
    this.connect();
  }

  /** Push a snapshot of front-end state/data as grounding for future prompts. */
  sendContext(data: unknown): void {
    this.enqueue({ type: "context", payload: { data } });
  }

  /** Ask the inference service to reason about a prompt, with optional context. */
  prompt(text: string, context?: unknown): void {
    const requestId = randomRequestId();
    this.enqueue({ type: "prompt", requestId, payload: { text, context } });
  }

  /** Tear down the connection. No further reconnect attempts will be made. */
  close(): void {
    this.closedByUser = true;
    this.ws?.close();
  }

  private wsUrl(): string {
    if (!this.opts.apiKey) return this.opts.url;
    const url = new URL(this.opts.url);
    url.searchParams.set("token", this.opts.apiKey);
    return url.toString();
  }

  private connect(): void {
    const ws = new WebSocket(this.wsUrl());
    this.ws = ws;

    ws.addEventListener("open", () => {
      this.backoffMs = this.minBackoffMs;
      this.send("hello", undefined, {
        appId: this.opts.appId,
        sdkVersion: SDK_VERSION,
        pageUrl: typeof location !== "undefined" ? location.href : undefined,
      });
    });

    ws.addEventListener("message", (ev) => {
      this.handleMessage(String(ev.data));
    });

    ws.addEventListener("close", () => {
      this.ready = false;
      if (this.closedByUser) return;
      this.scheduleReconnect();
    });

    ws.addEventListener("error", () => {
      // The subsequent "close" event drives reconnect; nothing to do here
      // beyond letting it fire.
    });
  }

  private scheduleReconnect(): void {
    const delay = this.backoffMs;
    this.backoffMs = Math.min(this.backoffMs * 2, this.maxBackoffMs);
    setTimeout(() => {
      if (!this.closedByUser) this.connect();
    }, delay);
  }

  private handleMessage(raw: string): void {
    let env: Envelope;
    try {
      env = JSON.parse(raw);
    } catch {
      return;
    }

    switch (env.type) {
      case "ack": {
        const ack = env.payload as AckPayload;
        this.ready = true;
        this.flushQueue();
        this.validateHandlers(ack.toolNames);
        break;
      }
      case "tool_call":
        this.handleToolCall(env.requestId, env.payload as ToolCallPayload);
        break;
      case "assistant_message":
        this.opts.onAssistantMessage?.(
          (env.payload as AssistantMessagePayload).text
        );
        break;
      case "error":
        this.opts.onError?.(env.payload as ErrorPayload);
        break;
    }
  }

  private validateHandlers(toolNames: string[]): void {
    const missing = toolNames.filter((name) => !(name in this.opts.tools));
    if (missing.length > 0) {
      console.warn(
        `[agent-bridge] backend declares tools with no registered handler: ${missing.join(", ")}`
      );
    }
  }

  private async handleToolCall(
    requestId: string | undefined,
    payload: ToolCallPayload
  ): Promise<void> {
    const handler = this.opts.tools[payload.toolName];
    if (!handler) {
      // Never fall back to eval/dynamic dispatch: an unregistered tool
      // name is rejected, not guessed at.
      this.send("tool_result", requestId, {
        toolName: payload.toolName,
        ok: false,
        error: `no handler registered for tool "${payload.toolName}"`,
      } satisfies ToolResultPayload);
      return;
    }

    try {
      const result = await handler(payload.args);
      this.send("tool_result", requestId, {
        toolName: payload.toolName,
        ok: true,
        result: result ?? null,
      } satisfies ToolResultPayload);
    } catch (err) {
      this.send("tool_result", requestId, {
        toolName: payload.toolName,
        ok: false,
        error: err instanceof Error ? err.message : String(err),
      } satisfies ToolResultPayload);
    }
  }

  private enqueue(msg: QueuedSend): void {
    if (this.ready && this.ws?.readyState === WebSocket.OPEN) {
      this.write(msg);
    } else {
      this.queue.push(msg);
    }
  }

  private flushQueue(): void {
    const pending = this.queue;
    this.queue = [];
    for (const msg of pending) this.write(msg);
  }

  private send(type: string, requestId: string | undefined, payload: unknown): void {
    this.write({ type, requestId, payload });
  }

  private write(msg: QueuedSend): void {
    if (this.ws?.readyState !== WebSocket.OPEN) {
      this.queue.push(msg);
      return;
    }
    this.ws.send(JSON.stringify(msg));
  }

  /**
   * Best-effort delivery of the last known context on page unload, since
   * the WebSocket connection is torn down before an in-flight message can
   * be flushed. Mirrors the sendBeacon fallback pattern analytics SDKs use
   * for the same reason. Requires opts.beaconUrl to be set; a no-op
   * otherwise.
   */
  private installUnloadFallback(): void {
    if (typeof document === "undefined" || typeof navigator === "undefined") return;
    if (!navigator.sendBeacon) return;

    document.addEventListener("visibilitychange", () => {
      if (document.visibilityState !== "hidden") return;
      if (this.queue.length === 0) return;
      if (!this.opts.beaconUrl) return;
      navigator.sendBeacon(
        this.opts.beaconUrl,
        JSON.stringify({ appId: this.opts.appId, queued: this.queue })
      );
    });
  }
}

function randomRequestId(): string {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) {
    return crypto.randomUUID();
  }
  return `req_${Date.now()}_${Math.random().toString(36).slice(2)}`;
}
