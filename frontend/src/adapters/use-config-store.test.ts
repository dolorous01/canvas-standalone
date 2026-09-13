import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
    getConfig: vi.fn(),
    notify: vi.fn(),
}));

vi.mock("@sub2api/api/canvas-api", async (importOriginal) => {
    const actual = await importOriginal<typeof import("@sub2api/api/canvas-api")>();
    return { ...actual, createCanvasAPI: () => ({ getConfig: mocks.getConfig }) };
});

vi.mock("@sub2api/runtime/host-runtime", () => ({
    getCanvasRuntimeHost: () => ({ notify: mocks.notify }),
}));

import type { CanvasConfig } from "@sub2api/api/canvas-api";
import { useCanvasSessionStore } from "@sub2api/stores/canvas-session-store";
import {
    getSelectedCanvasAPIKeyID,
    initializeCanvasConfigStore,
    resetCanvasConfigStore,
    resolvePreferredCanvasAPIKeyID,
    selectCanvasAPIKey,
    useConfigStore,
} from "./use-config-store";

const summary: CanvasConfig = {
    enabled: true,
    api_keys: [
        { id: 2, name: "image-a", group_id: 3, group_name: "images", available: true },
        { id: 3, name: "image-b", group_id: 3, group_name: "images", available: true },
    ],
    policy_version: 4,
    models: [],
};

describe("resolvePreferredCanvasAPIKeyID", () => {
    const keys = [
        { id: 1, name: "blocked", group_id: 2, group_name: "text", available: false as const, unavailable_reason: "image_generation_disabled" as const },
        { id: 2, name: "image-a", group_id: 3, group_name: "images", available: true as const },
        { id: 3, name: "image-b", group_id: 3, group_name: "images", available: true as const },
    ];

    it("keeps an available saved selection", () => {
        expect(resolvePreferredCanvasAPIKeyID(keys, 3)).toBe(3);
    });

    it("does not silently replace an unavailable saved selection", () => {
        expect(resolvePreferredCanvasAPIKeyID(keys, 1)).toBeUndefined();
    });

    it("requires an explicit selection when no preference exists", () => {
        expect(resolvePreferredCanvasAPIKeyID([{ id: 4, name: "legacy", group_id: 1, group_name: "default" }])).toBeUndefined();
    });
});

describe("initializeCanvasConfigStore", () => {
    beforeEach(() => {
        vi.clearAllMocks();
        localStorage.clear();
        useCanvasSessionStore.getState().clear();
        resetCanvasConfigStore();
    });

    it("retries after a failed initialization", async () => {
        useCanvasSessionStore.getState().setSelection(2, "gpt-image-2");
        mocks.getConfig
            .mockRejectedValueOnce(new Error("temporary outage"))
            .mockResolvedValueOnce(summary)
            .mockResolvedValueOnce({ ...summary, selected_api_key_id: 2 });

        await initializeCanvasConfigStore();
        expect(useConfigStore.getState().canvasConfigError).toBe("temporary outage");
        expect(mocks.notify).toHaveBeenCalledWith("error", "temporary outage");

        await initializeCanvasConfigStore();
        expect(mocks.getConfig).toHaveBeenCalledTimes(3);
        expect(getSelectedCanvasAPIKeyID()).toBe(2);
        expect(useConfigStore.getState().canvasConfigError).toBe("");
    });

    it("keeps the active key when a switch fails", async () => {
        useCanvasSessionStore.getState().setSelection(2, "gpt-image-2");
        mocks.getConfig
            .mockResolvedValueOnce(summary)
            .mockResolvedValueOnce({ ...summary, selected_api_key_id: 2 });
        await initializeCanvasConfigStore();

        mocks.getConfig
            .mockResolvedValueOnce(summary)
            .mockRejectedValueOnce(new Error("key configuration failed"));
        await selectCanvasAPIKey(3);

        expect(getSelectedCanvasAPIKeyID()).toBe(2);
        expect(useCanvasSessionStore.getState().apiKeyID).toBe(2);
        expect(useConfigStore.getState().canvasConfigError).toBe("key configuration failed");
    });

    it("loads the key list without selecting a key on first entry", async () => {
        mocks.getConfig.mockResolvedValueOnce(summary);

        await initializeCanvasConfigStore();

        expect(mocks.getConfig).toHaveBeenCalledTimes(1);
        expect(getSelectedCanvasAPIKeyID()).toBeUndefined();
        expect(useCanvasSessionStore.getState().apiKeyID).toBeUndefined();
        expect(useConfigStore.getState().canvasConfig).toEqual(summary);
        expect(useConfigStore.getState().config.models).toEqual([]);
    });
});
