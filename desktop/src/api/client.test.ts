import { beforeEach, describe, expect, it } from "vitest";
import { apiError, createMockShell } from "@/test/mockShell";
import { ApiError, DaemonUnreachableError, api, describeError, parseError } from "./client";

describe("api client", () => {
  let shell: ReturnType<typeof createMockShell>;
  beforeEach(() => {
    shell = createMockShell({
      "GET /v1/status": { body: { nm_state: "connected-global", api_version: 1 } },
      "POST /v1/wifi/connect": apiError(403, "not authorised to control networking", "Add yourself to the netdev group, then log out and in.", "permission"),
      "POST /v1/wifi/scan": { status: 200 },
      "DELETE /v1/profiles/abc": { status: 204 },
    });
  });

  it("parses a 2xx body", async () => {
    const s = await api.get<{ nm_state: string }>("/v1/status");
    expect(s.nm_state).toBe("connected-global");
    expect(shell.requests[0]).toEqual({ method: "GET", path: "/v1/status", body: undefined });
  });

  it("turns a daemon error into an ApiError with code and hint", async () => {
    const err = await api.post("/v1/wifi/connect", { ssid: "x" }).catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(403);
    expect(err.code).toBe("permission");
    expect(err.message).toBe("not authorised to control networking");
    expect(err.hint).toBe("Add yourself to the netdev group, then log out and in.");
    expect(describeError(err)).toEqual({ title: "not authorised to control networking", detail: "Add yourself to the netdev group, then log out and in." });
  });

  it("tolerates a non-JSON error body and derives the code from the status", () => {
    const e = parseError(503, "backend down");
    expect(e.message).toBe("backend down");
    expect(e.code).toBe("unavailable");
  });

  it("returns undefined for an empty 204 body", async () => {
    await expect(api.del("/v1/profiles/abc")).resolves.toBeUndefined();
  });

  it("maps a transport failure to DaemonUnreachableError", async () => {
    shell.invoke = async () => {
      throw "daemon-unreachable: connect ENOENT /run/user/1000/bnm/bnmd.sock";
    };
    const err = await api.get("/v1/status").catch((e) => e);
    expect(err).toBeInstanceOf(DaemonUnreachableError);
    expect(describeError(err).detail).toContain("ENOENT");
  });
});
