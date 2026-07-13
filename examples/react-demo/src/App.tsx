import { useEffect, useRef, useState } from "react";
import { AgentBridge } from "@onagent/agent-bridge-sdk";
import "./App.css";

const WS_URL = import.meta.env.VITE_AGENT_WS_URL ?? "ws://localhost:8080/ws";

function App() {
  const [query, setQuery] = useState("");
  const [path, setPath] = useState("/home");
  const [log, setLog] = useState<string[]>([]);
  const [prompt, setPrompt] = useState("");
  const [checkoutStatus, setCheckoutStatus] = useState<"idle" | "done">("idle");
  const highlightRef = useRef<HTMLDivElement>(null);
  const bridgeRef = useRef<AgentBridge | null>(null);

  const appendLog = (line: string) => setLog((prev) => [...prev, line]);

  // This is the customer's own button logic — the SDK has no idea what it
  // does. It's just a normal React handler that happens to also be wired
  // up as a tool below.
  function handleCheckout() {
    setCheckoutStatus("done");
    appendLog("checkout: order placed");
    setTimeout(() => setCheckoutStatus("idle"), 2000);
  }

  useEffect(() => {
    const bridge = new AgentBridge({
      url: WS_URL,
      appId: "demo-app",
      onAssistantMessage: (text) => appendLog(`assistant: ${text}`),
      onError: (err) => appendLog(`error: ${err.message}`),
      tools: {
        fill_search_form: ({ query }: { query: string }) => {
          setQuery(query);
          appendLog(`tool fill_search_form -> query="${query}"`);
        },
        navigate_to_page: ({ path }: { path: string }) => {
          setPath(path);
          appendLog(`tool navigate_to_page -> path="${path}"`);
          return { currentPath: path };
        },
        highlight_element: ({ selector }: { selector: string; durationMs?: number }) => {
          appendLog(`tool highlight_element -> selector="${selector}"`);
          const el = document.querySelector(selector);
          if (el instanceof HTMLElement) {
            el.style.outline = "3px solid orange";
            setTimeout(() => (el.style.outline = ""), 1500);
          }
        },
        click_checkout_button: () => {
          appendLog("tool click_checkout_button -> calling handleCheckout()");
          handleCheckout();
        },
      },
    });
    bridgeRef.current = bridge;
    return () => bridge.close();
  }, []);

  useEffect(() => {
    bridgeRef.current?.sendContext({ query, path });
  }, [query, path]);

  return (
    <section id="demo-root">
      <h1>Agent Bridge SDK Demo</h1>
      <p>
        Connects to <code>{WS_URL}</code> as app <code>demo-app</code>. Type a
        prompt below and, once a real inference backend is wired up, it can
        drive the form/page below via tool calls.
      </p>

      <div ref={highlightRef} className="panel">
        <label>
          Search query
          <input value={query} onChange={(e) => setQuery(e.target.value)} />
        </label>
        <label>
          Current path
          <input value={path} onChange={(e) => setPath(e.target.value)} />
        </label>
        <button id="checkout-btn" type="button" onClick={handleCheckout}>
          {checkoutStatus === "done" ? "✅ Order placed!" : "🛒 Checkout"}
        </button>
      </div>

      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (!prompt.trim()) return;
          appendLog(`prompt: ${prompt}`);
          bridgeRef.current?.prompt(prompt, { query, path });
          setPrompt("");
        }}
      >
        <input
          placeholder='Try: "please fill_search_form for me"'
          value={prompt}
          onChange={(e) => setPrompt(e.target.value)}
        />
        <button type="submit">Send</button>
      </form>

      <pre className="log">{log.join("\n")}</pre>
    </section>
  );
}

export default App;
