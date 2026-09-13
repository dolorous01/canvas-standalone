import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import upstreamI18n from "@/i18n";
import { useConfigStore } from "@sub2api/adapters/use-config-store";
import type { CanvasHostContext } from "@sub2api/host-context";
import { CanvasHostProvider, createCanvasHostStore } from "@sub2api/host-context";
import { useCanvasSessionStore } from "@sub2api/stores/canvas-session-store";
import { CanvasAPIKeyMenu } from "./canvas-api-key-menu";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

describe("CanvasAPIKeyMenu", () => {
    beforeEach(async () => {
        vi.stubGlobal("ResizeObserver", class {
            observe() {}
            unobserve() {}
            disconnect() {}
        });
        vi.stubGlobal("matchMedia", vi.fn(() => ({
            matches: false,
            media: "",
            onchange: null,
            addListener: vi.fn(),
            removeListener: vi.fn(),
            addEventListener: vi.fn(),
            removeEventListener: vi.fn(),
            dispatchEvent: vi.fn(),
        })));
        await upstreamI18n.changeLanguage("en-US");
        useCanvasSessionStore.getState().setSelection(2, "gpt-image-2");
        useConfigStore.setState({
            canvasConfig: {
                enabled: false,
                policy_version: 7,
                models: [],
                api_keys: [
                    { id: 1, name: "Blocked key", group_id: 4, group_name: "text", available: false, unavailable_reason: "image_generation_disabled" },
                    { id: 2, name: "Creative key", group_id: 3, group_name: "images", available: true },
                ],
            },
            canvasConfigLoading: false,
            canvasConfigError: "",
        });
    });

    afterEach(() => {
        vi.unstubAllGlobals();
        useCanvasSessionStore.getState().clear();
        document.body.replaceChildren();
    });

    it("shows the selected key, disabled reasons, policy state, and management command", async () => {
        const navigate = vi.fn();
        const host: CanvasHostContext = {
            apiBaseURL: "/api/v1",
            locale: "en-US",
            theme: "light",
            routeMode: "user",
            request: vi.fn(),
            stream: async () => new ReadableStream<Uint8Array>(),
            navigate,
            notify: vi.fn(),
        };
        const container = document.createElement("div");
        document.body.append(container);
        const root = createRoot(container);

        await act(async () => {
            root.render(
                <CanvasHostProvider store={createCanvasHostStore(host)}>
                    <CanvasAPIKeyMenu />
                </CanvasHostProvider>,
            );
        });
        const trigger = container.querySelector("button");
        expect(trigger?.textContent).toContain("Creative key");

        await act(async () => {
            trigger?.click();
            await Promise.resolve();
        });

        expect(document.body.textContent).toContain("Image jobs are not globally enabled");
        expect(document.body.textContent).toContain("Image generation is disabled for this group");
        const blockedItem = Array.from(document.querySelectorAll<HTMLElement>("[role=menuitem]")).find((item) => item.textContent?.includes("Blocked key"));
        expect(blockedItem?.getAttribute("aria-disabled")).toBe("true");
        const manageItem = Array.from(document.querySelectorAll<HTMLElement>("[role=menuitem]")).find((item) => item.textContent?.includes("Manage API keys"));

        await act(async () => manageItem?.click());
        expect(navigate).toHaveBeenCalledWith("/studio/credentials");

        await act(async () => root.unmount());
    });
});
