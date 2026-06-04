import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StreamableHTTPClientTransport } from "@modelcontextprotocol/sdk/client/streamableHttp.js";
import {
  LoggingMessageNotificationSchema,
  ToolListChangedNotificationSchema,
  type CallToolResult,
  type Tool,
} from "@modelcontextprotocol/sdk/types.js";

export type ConnStatus = "connecting" | "ready" | "error";

export interface LogEntry {
  id: number;
  ts: number;
  level: string;
  logger: string;
  data: unknown;
}

interface McpContextValue {
  status: ConnStatus;
  error: string | null;
  tools: Tool[];
  serverInfo: { name?: string; version?: string } | null;
  logs: LogEntry[];
  clearLogs: () => void;
  hasTool: (name: string) => boolean;
  getTool: (name: string) => Tool | undefined;
  callTool: (name: string, args?: Record<string, unknown>) => Promise<CallToolResult>;
  refreshTools: () => Promise<void>;
  reconnect: () => void;
}

const McpContext = createContext<McpContextValue | null>(null);

const MAX_LOGS = 1000;

// The daemon serves the SPA at /app/ and the MCP endpoint at / on the same
// origin, so the transport target is just the origin root — no CORS, no config.
function mcpEndpoint(): URL {
  return new URL("/", window.location.href);
}

export function McpProvider({ children }: { children: ReactNode }) {
  const clientRef = useRef<Client | null>(null);
  const transportRef = useRef<StreamableHTTPClientTransport | null>(null);
  const logSeq = useRef(0);

  const [status, setStatus] = useState<ConnStatus>("connecting");
  const [error, setError] = useState<string | null>(null);
  const [tools, setTools] = useState<Tool[]>([]);
  const [serverInfo, setServerInfo] = useState<{ name?: string; version?: string } | null>(null);
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const [generation, setGeneration] = useState(0);

  const pushLog = useCallback((level: string, logger: string, data: unknown) => {
    setLogs((prev) => {
      const next = [
        ...prev,
        { id: logSeq.current++, ts: Date.now(), level, logger, data },
      ];
      return next.length > MAX_LOGS ? next.slice(next.length - MAX_LOGS) : next;
    });
  }, []);

  const refreshTools = useCallback(async () => {
    const client = clientRef.current;
    if (!client) return;
    const res = await client.listTools();
    setTools(res.tools);
  }, []);

  useEffect(() => {
    let cancelled = false;
    const client = new Client(
      { name: "signalwave", version: "0.0.1" },
      { capabilities: {} },
    );
    const transport = new StreamableHTTPClientTransport(mcpEndpoint());
    clientRef.current = client;
    transportRef.current = transport;

    client.setNotificationHandler(ToolListChangedNotificationSchema, () => {
      void refreshTools();
    });
    client.setNotificationHandler(LoggingMessageNotificationSchema, (n) => {
      const p = n.params;
      pushLog(p.level ?? "info", p.logger ?? "server", p.data);
    });

    setStatus("connecting");
    setError(null);

    (async () => {
      try {
        await client.connect(transport);
        if (cancelled) return;
        setServerInfo(client.getServerVersion() ?? null);
        await refreshTools();
        // Subscribe to log notifications so the Activity feed receives inbound
        // MIDI, AUv3 probe, and AUM session events broadcast by the daemon.
        try {
          await client.setLoggingLevel("info");
        } catch {
          // Older daemons may not advertise logging; the rest still works.
        }
        if (cancelled) return;
        setStatus("ready");
      } catch (e) {
        if (cancelled) return;
        setError(e instanceof Error ? e.message : String(e));
        setStatus("error");
      }
    })();

    return () => {
      cancelled = true;
      void client.close().catch(() => undefined);
      clientRef.current = null;
      transportRef.current = null;
    };
  }, [generation, refreshTools, pushLog]);

  const callTool = useCallback(
    async (name: string, args: Record<string, unknown> = {}) => {
      const client = clientRef.current;
      if (!client) throw new Error("not connected");
      return (await client.callTool({ name, arguments: args })) as CallToolResult;
    },
    [],
  );

  const value = useMemo<McpContextValue>(
    () => ({
      status,
      error,
      tools,
      serverInfo,
      logs,
      clearLogs: () => setLogs([]),
      hasTool: (name: string) => tools.some((t) => t.name === name),
      getTool: (name: string) => tools.find((t) => t.name === name),
      callTool,
      refreshTools,
      reconnect: () => setGeneration((g) => g + 1),
    }),
    [status, error, tools, serverInfo, logs, callTool, refreshTools],
  );

  return <McpContext.Provider value={value}>{children}</McpContext.Provider>;
}

export function useMcp(): McpContextValue {
  const ctx = useContext(McpContext);
  if (!ctx) throw new Error("useMcp must be used within McpProvider");
  return ctx;
}
