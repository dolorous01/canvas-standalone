import { describe, expect, it } from "vitest";

import { CANVAS_FIT_MAX_SCALE, CANVAS_FIT_MIN_SCALE, fitCanvasNodes, shouldFitRestoredViewport } from "./canvas-viewport";

describe("fitCanvasNodes", () => {
    it("centers all node bounds with padding", () => {
        const viewport = fitCanvasNodes(
            [
                { position: { x: 100, y: 50 }, width: 200, height: 100 },
                { position: { x: 500, y: 250 }, width: 100, height: 150 },
            ],
            { width: 1000, height: 700 },
        );

        expect(viewport).toEqual({ x: 80, y: 80, k: 1.2 });
    });

    it("limits very large canvases to a readable minimum scale", () => {
        const viewport = fitCanvasNodes([{ position: { x: 0, y: 0 }, width: 10000, height: 8000 }], { width: 1200, height: 800 });

        expect(viewport.k).toBe(CANVAS_FIT_MIN_SCALE);
        expect(viewport.x).toBe(-150);
        expect(viewport.y).toBe(-200);
    });

    it("does not over-enlarge a small node", () => {
        const viewport = fitCanvasNodes([{ position: { x: 200, y: 100 }, width: 100, height: 100 }], { width: 1200, height: 800 });

        expect(viewport.k).toBe(CANVAS_FIT_MAX_SCALE);
        expect(viewport.x).toBe(300);
        expect(viewport.y).toBe(220);
    });

    it("returns a centered 100% viewport for an empty canvas", () => {
        expect(fitCanvasNodes([], { width: 900, height: 600 })).toEqual({ x: 450, y: 300, k: 1 });
    });
});

describe("shouldFitRestoredViewport", () => {
    it("repairs legacy tiny and invalid viewports", () => {
        expect(shouldFitRestoredViewport({ x: 10, y: 20, k: 0.05 })).toBe(true);
        expect(shouldFitRestoredViewport({ x: Number.NaN, y: 20, k: 1 })).toBe(true);
    });

    it("preserves a normal saved viewport", () => {
        expect(shouldFitRestoredViewport({ x: -300, y: 140, k: 0.5 })).toBe(false);
    });
});
