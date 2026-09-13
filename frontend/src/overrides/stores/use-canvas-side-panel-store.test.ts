import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { CANVAS_SIDE_PANEL_MOTION_MS, useCanvasSidePanelStore } from "./use-canvas-side-panel-store";

describe("responsive Canvas side panel", () => {
    beforeEach(() => {
        vi.useFakeTimers();
        localStorage.clear();
        useCanvasSidePanelStore.setState({
            width: 280,
            panelOpen: true,
            panelMounted: true,
            panelClosing: false,
            compact: false,
        });
    });

    afterEach(() => {
        vi.useRealTimers();
    });

    it("closes immediately in compact mode without overwriting the desktop preference", () => {
        localStorage.setItem("canvas-side-panel-open", "1");

        useCanvasSidePanelStore.getState().setResponsiveCompact(true);

        expect(useCanvasSidePanelStore.getState()).toMatchObject({ compact: true, panelOpen: false, panelMounted: false, panelClosing: false });
        expect(localStorage.getItem("canvas-side-panel-open")).toBe("1");

        useCanvasSidePanelStore.getState().setResponsiveCompact(false);
        expect(useCanvasSidePanelStore.getState()).toMatchObject({ compact: false, panelOpen: true, panelMounted: true });
    });

    it("lets a compact user open and close the overlay with the existing toggle", () => {
        useCanvasSidePanelStore.getState().setResponsiveCompact(true);

        useCanvasSidePanelStore.getState().togglePanel();
        expect(useCanvasSidePanelStore.getState()).toMatchObject({ compact: true, panelOpen: true, panelMounted: true });
        expect(localStorage.getItem("canvas-side-panel-open")).toBe("1");

        useCanvasSidePanelStore.getState().togglePanel();
        expect(useCanvasSidePanelStore.getState()).toMatchObject({ panelOpen: false, panelMounted: true, panelClosing: true });
        vi.advanceTimersByTime(CANVAS_SIDE_PANEL_MOTION_MS);
        expect(useCanvasSidePanelStore.getState()).toMatchObject({ panelOpen: false, panelMounted: false, panelClosing: false });
        expect(localStorage.getItem("canvas-side-panel-open")).toBe("0");
    });

    it("keeps the panel closed on desktop when that is the saved preference", () => {
        localStorage.setItem("canvas-side-panel-open", "0");
        useCanvasSidePanelStore.getState().setResponsiveCompact(true);
        useCanvasSidePanelStore.getState().setResponsiveCompact(false);

        expect(useCanvasSidePanelStore.getState()).toMatchObject({ compact: false, panelOpen: false, panelMounted: false });
    });
});
