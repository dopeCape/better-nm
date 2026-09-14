import { useEffect } from "react";
import { QueryClient, QueryClientProvider, useQueryClient } from "@tanstack/react-query";
import { DaemonUnreachableError } from "@/api/client";
import { Banner } from "@/components/ui";
import { useUI } from "@/state/ui";
import { Devices } from "@/views/Devices";
import { Overview } from "@/views/Overview";
import { Quality } from "@/views/Quality";
import { Settings } from "@/views/Settings";
import { Speed } from "@/views/Speed";
import { Vpn } from "@/views/Vpn";
import { Wifi } from "@/views/Wifi";
import { startEventRouting } from "./events";
import { installHotkeys } from "./hotkeys";
import { Palette } from "./Palette";
import { Rail } from "./Rail";
import { SecretPrompt } from "./SecretPrompt";
import { Toasts } from "./Toasts";
import { TopStrip } from "./TopStrip";

export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 5_000,
        retry: (count, err) => !(err instanceof DaemonUnreachableError) && count < 1,
        refetchOnWindowFocus: true,
      },
    },
  });
}

const VIEWS = { overview: Overview, wifi: Wifi, vpn: Vpn, quality: Quality, speed: Speed, devices: Devices, settings: Settings } as const;

function Shell() {
  const qc = useQueryClient();
  const section = useUI((s) => s.section);
  const stream = useUI((s) => s.stream);
  const View = VIEWS[section];

  useEffect(() => {
    let off: (() => void) | undefined;
    let cancelled = false;
    void startEventRouting(qc).then((o) => {
      if (cancelled) o();
      else off = o;
    });
    const offKeys = installHotkeys();
    return () => {
      cancelled = true;
      off?.();
      offKeys();
    };
  }, [qc]);

  // Scroll the content pane to the top on section change.
  useEffect(() => {
    document.querySelector(".content")?.scrollTo({ top: 0 });
  }, [section]);

  return (
    <div className="app">
      <Rail />
      <div className="main">
        <TopStrip />
        <main className="content" id="main" tabIndex={-1}>
          <div className="content-inner">
            {!stream.connected && (
              <Banner tone="error" icon="warning-circle-fill" title="Daemon unreachable, retrying" className="mb-6">
                {stream.error ? `${stream.error}. ` : ""}
                {stream.attempt ? `Attempt ${stream.attempt}. ` : ""}
                Values shown may be stale; run <span className="mono">bnm daemon status</span> if this persists.
              </Banner>
            )}
            <View key={section} />
          </div>
        </main>
      </div>
      <Palette />
      <SecretPrompt />
      <Toasts />
    </div>
  );
}

export function App({ client }: { client?: QueryClient }) {
  const qc = client ?? defaultClient;
  return (
    <QueryClientProvider client={qc}>
      <Shell />
    </QueryClientProvider>
  );
}

const defaultClient = createQueryClient();
