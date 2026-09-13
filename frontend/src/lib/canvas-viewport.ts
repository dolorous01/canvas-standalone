import type { CanvasNodeData, ViewportTransform } from "@/types/canvas";

export const CANVAS_FIT_MIN_SCALE = 0.15;
export const CANVAS_FIT_MAX_SCALE = 1.2;

type CanvasViewportSize = {
    width: number;
    height: number;
};

type CanvasViewportNode = Pick<CanvasNodeData, "position" | "width" | "height">;

type FitCanvasViewportOptions = {
    padding?: number;
    minScale?: number;
    maxScale?: number;
};

export function fitCanvasNodes(nodes: CanvasViewportNode[], viewportSize: CanvasViewportSize, options: FitCanvasViewportOptions = {}): ViewportTransform {
    const width = finiteNonNegative(viewportSize.width);
    const height = finiteNonNegative(viewportSize.height);
    const emptyViewport = { x: width / 2, y: height / 2, k: 1 };
    const bounds = canvasNodeBounds(nodes);
    if (!bounds || width === 0 || height === 0) return emptyViewport;

    const padding = Math.max(0, finiteNumber(options.padding, 80));
    const minScale = Math.max(0.01, finiteNumber(options.minScale, CANVAS_FIT_MIN_SCALE));
    const maxScale = Math.max(minScale, finiteNumber(options.maxScale, CANVAS_FIT_MAX_SCALE));
    const availableWidth = Math.max(1, width - Math.min(padding * 2, Math.max(0, width - 1)));
    const availableHeight = Math.max(1, height - Math.min(padding * 2, Math.max(0, height - 1)));
    const contentWidth = Math.max(1, bounds.maxX - bounds.minX);
    const contentHeight = Math.max(1, bounds.maxY - bounds.minY);
    const scale = clamp(Math.min(availableWidth / contentWidth, availableHeight / contentHeight), minScale, maxScale);
    const centerX = (bounds.minX + bounds.maxX) / 2;
    const centerY = (bounds.minY + bounds.maxY) / 2;

    return {
        x: width / 2 - centerX * scale,
        y: height / 2 - centerY * scale,
        k: scale,
    };
}

export function shouldFitRestoredViewport(viewport: ViewportTransform): boolean {
    return !Number.isFinite(viewport.x) || !Number.isFinite(viewport.y) || !Number.isFinite(viewport.k) || viewport.k < CANVAS_FIT_MIN_SCALE;
}

function canvasNodeBounds(nodes: CanvasViewportNode[]) {
    let minX = Number.POSITIVE_INFINITY;
    let minY = Number.POSITIVE_INFINITY;
    let maxX = Number.NEGATIVE_INFINITY;
    let maxY = Number.NEGATIVE_INFINITY;

    for (const node of nodes) {
        const { x, y } = node.position;
        const { width, height } = node;
        if (![x, y, width, height].every(Number.isFinite)) continue;
        minX = Math.min(minX, x, x + width);
        minY = Math.min(minY, y, y + height);
        maxX = Math.max(maxX, x, x + width);
        maxY = Math.max(maxY, y, y + height);
    }

    return Number.isFinite(minX) ? { minX, minY, maxX, maxY } : null;
}

function finiteNonNegative(value: number): number {
    return Number.isFinite(value) ? Math.max(0, value) : 0;
}

function finiteNumber(value: number | undefined, fallback: number): number {
    return value !== undefined && Number.isFinite(value) ? value : fallback;
}

function clamp(value: number, min: number, max: number): number {
    return Math.min(Math.max(value, min), max);
}
