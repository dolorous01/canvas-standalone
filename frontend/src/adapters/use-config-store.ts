import { useMemo } from "react";
import { create } from "zustand";
import { nanoid } from "nanoid";

import i18n from "@/i18n";
import { createCanvasAPI, type CanvasAPIKey, type CanvasCapability, type CanvasConfig } from "@sub2api/api/canvas-api";
import { getCanvasRuntimeHost } from "@sub2api/runtime/host-runtime";
import { useCanvasSessionStore } from "@sub2api/stores/canvas-session-store";

export type ApiCallFormat = "openai" | "gemini";
export type ModelCapability = "image" | "video" | "text" | "audio";
export type ReasoningEffort = "auto" | "low" | "medium" | "high" | "xhigh";

export type ChannelModel = {
    name: string;
    capability: ModelCapability;
    script?: string;
};

export type ModelChannel = {
    id: string;
    name: string;
    baseUrl: string;
    apiKey: string;
    apiFormat: ApiCallFormat;
    models: ChannelModel[];
};

export type AiConfig = {
    channelMode: "remote" | "local";
    baseUrl: string;
    apiKey: string;
    apiFormat: ApiCallFormat;
    channels: ModelChannel[];
    model: string;
    imageModel: string;
    videoModel: string;
    textModel: string;
    audioModel: string;
    audioVoice: string;
    audioFormat: string;
    audioSpeed: string;
    audioInstructions: string;
    videoSeconds: string;
    vquality: string;
    videoGenerateAudio: string;
    videoWatermark: string;
    systemPrompt: string;
    reasoningEffort: ReasoningEffort;
    models: string[];
    quality: string;
    size: string;
    background: string;
    count: string;
    canvasImageCount: string;
};

export type WebdavSyncConfig = {
    url: string;
    username: string;
    password: string;
    directory: string;
    lastSyncedAt: string;
};
export type ConfigTabKey = "channels" | "preferences" | "prompt-sources" | "webdav" | "local-storage";

export const CONFIG_STORE_KEY = "infinite-canvas:ai_config_store";
const CHANNEL_MODEL_SEPARATOR = "::";
const SAFE_PREFERENCES_KEY = "sub2api:canvas-preferences";

export const defaultConfig: AiConfig = {
    channelMode: "local",
    baseUrl: "",
    apiKey: "",
    apiFormat: "openai",
    channels: [],
    model: "",
    imageModel: "",
    videoModel: "",
    textModel: "",
    audioModel: "",
    audioVoice: "alloy",
    audioFormat: "mp3",
    audioSpeed: "1",
    audioInstructions: "",
    videoSeconds: "6",
    vquality: "720",
    videoGenerateAudio: "true",
    videoWatermark: "false",
    systemPrompt: "",
    reasoningEffort: "auto",
    models: [],
    quality: "auto",
    size: "1:1",
    background: "",
    count: "1",
    canvasImageCount: "3",
};

export const defaultWebdavSyncConfig: WebdavSyncConfig = {
    url: "",
    username: "",
    password: "",
    directory: "infinite-canvas",
    lastSyncedAt: "",
};

type ConfigStore = {
    config: AiConfig;
    canvasConfig?: CanvasConfig;
    canvasConfigLoading: boolean;
    canvasConfigError: string;
    webdav: WebdavSyncConfig;
    isConfigOpen: boolean;
    configTab: ConfigTabKey;
    shouldPromptContinue: boolean;
    updateConfig: <K extends keyof AiConfig>(key: K, value: AiConfig[K]) => void;
    updateWebdavConfig: <K extends keyof WebdavSyncConfig>(key: K, value: WebdavSyncConfig[K]) => void;
    isAiConfigReady: (config: AiConfig, model: string) => boolean;
    openConfigDialog: (shouldPromptContinue?: boolean, tab?: ConfigTabKey) => void;
    setConfigDialogOpen: (isOpen: boolean) => void;
    clearPromptContinue: () => void;
};

const VIDEO_KEYWORDS = ["video", "sora", "veo", "kling", "wan", "hailuo"];

export function boolConfig(value: string, fallback: boolean) {
    return value ? value === "true" : fallback;
}
const AUDIO_KEYWORDS = ["audio", "tts", "speech", "voice", "music", "sound"];
const IMAGE_KEYWORDS = ["seedream", "gpt-image", "image", "dall-e", "dalle", "imagen", "flux", "sdxl", "stable-diffusion", "midjourney"];

/** Best-effort default capability for a freshly fetched model name; user can override in the channel editor. */
export function guessCapability(name: string): ModelCapability {
    const value = name.toLowerCase();
    if (VIDEO_KEYWORDS.some((keyword) => value.includes(keyword))) return "video";
    if (AUDIO_KEYWORDS.some((keyword) => value.includes(keyword))) return "audio";
    if (IMAGE_KEYWORDS.some((keyword) => value.includes(keyword))) return "image";
    return "text";
}

function findChannelModel(config: AiConfig, value: string): { channel: ModelChannel; model: ChannelModel } | null {
    const decoded = decodeChannelModel(value);
    const name = decoded?.model || value;
    const channel = decoded ? config.channels.find((item) => item.id === decoded.channelId) : config.channels.find((item) => item.models.some((model) => model.name === name));
    const model = channel?.models.find((item) => item.name === name);
    return channel && model ? { channel, model } : null;
}

export function modelCapabilityOf(config: AiConfig, value: string): ModelCapability | undefined {
    return findChannelModel(config, value)?.model.capability;
}

export function modelMatchesCapability(config: AiConfig, value: string, capability?: ModelCapability) {
    if (!capability) return true;
    return modelCapabilityOf(config, value) === capability;
}

export function resolveModelForCapability(config: AiConfig, currentModel: string | undefined, capability: ModelCapability) {
    const defaultModel = capability === "image" ? config.imageModel : capability === "video" ? config.videoModel : capability === "audio" ? config.audioModel : config.textModel;
    const fallbackModel = capability === "image" ? defaultConfig.imageModel : capability === "video" ? defaultConfig.videoModel : capability === "audio" ? defaultConfig.audioModel : defaultConfig.textModel;
    if (currentModel && modelMatchesCapability(config, currentModel, capability)) return currentModel;
    if (defaultModel && modelMatchesCapability(config, defaultModel, capability)) return defaultModel;
    return fallbackModel;
}

export function selectableModelsByCapability(config: AiConfig, capability?: ModelCapability) {
    if (!capability) return config.models;
    return config.channels.flatMap((channel) => channel.models.filter((model) => model.capability === capability).map((model) => encodeChannelModel(channel.id, model.name)));
}

/** The user script (if any) attached to a model; empty string means use the system default call. */
export function resolveModelScript(config: AiConfig, value: string) {
    void config;
    void value;
    return "";
}

function isAiConfigReady(config: AiConfig, model: string) {
    return Boolean(selectedAPIKeyID && model.trim() && modelCapabilityOf(config, model));
}

const blockedConfigKeys = new Set<keyof AiConfig>(["apiKey", "baseUrl", "channels", "channelMode", "apiFormat", "models"]);
let selectedAPIKeyID: number | undefined;
let configInitialization: Promise<void> | undefined;
let configInitializationVersion = 0;
const capabilitiesByModel = new Map<string, CanvasCapability>();

export const useConfigStore = create<ConfigStore>()((set) => ({
    config: { ...defaultConfig, ...readSafePreferences() },
    canvasConfig: undefined,
    canvasConfigLoading: false,
    canvasConfigError: "",
    webdav: defaultWebdavSyncConfig,
    isConfigOpen: false,
    configTab: "channels",
    shouldPromptContinue: false,
    updateConfig: (key, value) => {
        if (blockedConfigKeys.has(key)) return;
        set((state) => {
            const config = { ...state.config, [key]: value };
            writeSafePreferences(config);
            if (key === "model") useCanvasSessionStore.getState().setSelection(selectedAPIKeyID, modelOptionName(String(value || "")));
            return { config };
        });
    },
    updateWebdavConfig: () => undefined,
    isAiConfigReady: (config, model) => isAiConfigReady(config, model),
    openConfigDialog: (shouldPromptContinue = false, configTab = "preferences") => set({ isConfigOpen: true, shouldPromptContinue, configTab }),
    setConfigDialogOpen: (isConfigOpen) => set({ isConfigOpen }),
    clearPromptContinue: () => set({ shouldPromptContinue: false }),
}));

export function initializeCanvasConfigStore(preferredAPIKeyID?: number): Promise<void> {
    if (configInitialization && preferredAPIKeyID === undefined) return configInitialization;
    const initializationVersion = ++configInitializationVersion;
    useConfigStore.setState({ canvasConfigLoading: true, canvasConfigError: "" });
    configInitialization = (async () => {
        const canvasAPI = createCanvasAPI(getCanvasRuntimeHost());
        const summary = await canvasAPI.getConfig();
        if (initializationVersion !== configInitializationVersion) return;
        useConfigStore.setState({ canvasConfig: summary });
        const saved = preferredAPIKeyID ?? useCanvasSessionStore.getState().apiKeyID;
        const apiKeyID = resolvePreferredCanvasAPIKeyID(summary.api_keys, saved);
        if (!apiKeyID) {
            selectedAPIKeyID = undefined;
            capabilitiesByModel.clear();
            useCanvasSessionStore.getState().clear();
            useConfigStore.setState((state) => ({
                canvasConfig: summary,
                canvasConfigLoading: false,
                config: { ...state.config, channels: [], models: [], model: "", imageModel: "", videoModel: "", textModel: "", audioModel: "" },
            }));
            return;
        }
        const serverConfig = await canvasAPI.getConfig(apiKeyID);
        if (initializationVersion !== configInitializationVersion) return;
        applyServerConfig(serverConfig, apiKeyID);
    })().catch((error) => {
        if (initializationVersion !== configInitializationVersion) return;
        configInitialization = undefined;
        const message = error instanceof Error ? error.message : "Canvas configuration could not be loaded";
        useConfigStore.setState({ canvasConfigLoading: false, canvasConfigError: message });
        getCanvasRuntimeHost().notify("error", message);
    });
    return configInitialization;
}

export function resetCanvasConfigStore(): void {
    configInitializationVersion += 1;
    selectedAPIKeyID = undefined;
    configInitialization = undefined;
    capabilitiesByModel.clear();
    useConfigStore.setState((state) => ({
        config: { ...defaultConfig, ...readSafePreferences() },
        canvasConfig: undefined,
        canvasConfigLoading: false,
        canvasConfigError: "",
        webdav: defaultWebdavSyncConfig,
        isConfigOpen: false,
        shouldPromptContinue: false,
        configTab: state.configTab,
    }));
}

export function getSelectedCanvasAPIKeyID(): number | undefined {
    return selectedAPIKeyID;
}

export function getCanvasModelCapability(model: string): CanvasCapability | undefined {
    return capabilitiesByModel.get(modelOptionName(model));
}

export function selectCanvasAPIKey(apiKeyID: number): Promise<void> {
    return initializeCanvasConfigStore(apiKeyID);
}

export function resolvePreferredCanvasAPIKeyID(apiKeys: CanvasAPIKey[], preferred?: number): number | undefined {
    const availableKeys = apiKeys.filter((item) => item.available !== false);
    return availableKeys.some((item) => item.id === preferred) ? preferred : undefined;
}

export function useEffectiveConfig() {
    const config = useConfigStore((state) => state.config);
    return useMemo(() => ({ ...config, channelMode: "local" as const }), [config]);
}

/** Normalize a mixed list of raw model names or model objects into deduped ChannelModel entries. */
export function normalizeChannelModels(models: Array<string | ChannelModel> | undefined): ChannelModel[] {
    const seen = new Set<string>();
    const result: ChannelModel[] = [];
    for (const item of models || []) {
        const name = (typeof item === "string" ? item : item?.name || "").trim();
        if (!name || seen.has(name)) continue;
        seen.add(name);
        const capability = typeof item === "string" ? guessCapability(name) : item.capability || guessCapability(name);
        result.push({ name, capability });
    }
    return result;
}

export function createModelChannel(channel?: Partial<ModelChannel>): ModelChannel {
    const apiFormat = normalizeApiFormat(channel?.apiFormat);
    return {
        id: channel?.id?.trim() || nanoid(),
        name: channel?.name?.trim() || i18n.t("config.channels.newName"),
        baseUrl: "",
        apiKey: "",
        apiFormat,
        models: normalizeChannelModels(channel?.models),
    };
}

export function encodeChannelModel(channelId: string, model: string) {
    return `${channelId}${CHANNEL_MODEL_SEPARATOR}${model.trim()}`;
}

export function isChannelModelValue(value: string) {
    return value.includes(CHANNEL_MODEL_SEPARATOR);
}

export function decodeChannelModel(value: string) {
    const index = value.indexOf(CHANNEL_MODEL_SEPARATOR);
    if (index < 0) return null;
    return { channelId: value.slice(0, index), model: value.slice(index + CHANNEL_MODEL_SEPARATOR.length) };
}

export function modelOptionName(value: string) {
    return decodeChannelModel(value)?.model || value;
}

export function modelOptionLabel(config: AiConfig, value: string) {
    const decoded = decodeChannelModel(value);
    if (!decoded) return value;
    const channel = config.channels.find((item) => item.id === decoded.channelId);
    return channel ? `${decoded.model}（${channel.name}）` : decoded.model;
}

export function modelOptionsFromChannels(channels: ModelChannel[]) {
    return uniqueModelOptions(channels.flatMap((channel) => channel.models.map((model) => encodeChannelModel(channel.id, model.name))));
}

export function normalizeModelOptionValue(value: string | undefined, channels: ModelChannel[]) {
    const model = (value || "").trim();
    if (!model) return "";
    const decoded = decodeChannelModel(model);
    if (decoded) {
        const channel = channels.find((item) => item.id === decoded.channelId);
        return channel && channel.models.some((item) => item.name === decoded.model) ? model : "";
    }
    const channel = channels.find((item) => item.models.some((entry) => entry.name === model)) || channels[0];
    return channel && channel.models.some((item) => item.name === model) ? encodeChannelModel(channel.id, model) : model;
}

export function resolveModelChannel(config: AiConfig, value: string) {
    const decoded = decodeChannelModel(value);
    const model = decoded?.model || value;
    const matched = decoded ? config.channels.find((channel) => channel.id === decoded.channelId) : config.channels.find((channel) => channel.models.some((item) => item.name === model));
    return matched || config.channels[0] || createModelChannel({ id: "default", name: i18n.t("config.channels.defaultName"), baseUrl: config.baseUrl, apiKey: config.apiKey, apiFormat: config.apiFormat, models: config.models.map(modelOptionName).map((name) => ({ name, capability: guessCapability(name) })) });
}

export function resolveModelRequestConfig(config: AiConfig, value: string) {
    return {
        ...config,
        model: modelOptionName(value || config.model),
        baseUrl: "",
        apiKey: "",
        apiFormat: "openai" as const,
    };
}

function normalizeChannels(config: AiConfig) {
    const persistedChannels = Array.isArray(config.channels) ? config.channels : [];
    const channels = persistedChannels.map((channel, index) =>
        createModelChannel({
            ...channel,
            id: channel.id || (index === 0 ? "default" : `channel-${index + 1}`),
            name: channel.name || (index === 0 ? i18n.t("config.channels.defaultName") : i18n.t("config.channels.indexedName", { index: index + 1 })),
            models: normalizeChannelModels(channel.models),
        }),
    );
    if (!channels.length) {
        channels.push(
            createModelChannel({
                id: "default",
                name: i18n.t("config.channels.defaultName"),
                baseUrl: config.baseUrl || defaultConfig.baseUrl,
                apiKey: config.apiKey || "",
                apiFormat: config.apiFormat || defaultConfig.apiFormat,
                models: normalizeChannelModels([config.model, config.imageModel, config.videoModel, config.textModel, config.audioModel].map(modelOptionName)),
            }),
        );
    }
    return channels;
}

export function defaultBaseUrlForApiFormat(apiFormat: ApiCallFormat) {
    void apiFormat;
    return "";
}

function normalizeApiFormat(apiFormat: unknown): ApiCallFormat {
    return apiFormat === "gemini" ? apiFormat : "openai";
}

function uniqueModelOptions(models: string[]) {
    return Array.from(new Set((models || []).map((model) => model.trim()).filter(Boolean)));
}

export function buildApiUrl(baseUrl: string, path: string) {
    void baseUrl;
    void path;
    throw new Error("Direct provider requests are disabled in Sub2API Studio");
}

const safePreferenceKeys: Array<keyof AiConfig> = [
    "model",
    "imageModel",
    "videoModel",
    "textModel",
    "audioModel",
    "audioVoice",
    "audioFormat",
    "audioSpeed",
    "audioInstructions",
    "videoSeconds",
    "vquality",
    "videoGenerateAudio",
    "videoWatermark",
    "systemPrompt",
    "reasoningEffort",
    "quality",
    "size",
    "background",
    "count",
    "canvasImageCount",
];

function applyServerConfig(serverConfig: CanvasConfig, apiKeyID: number): void {
    const current = useConfigStore.getState().config;
    const apiKey = serverConfig.api_keys.find((item) => item.id === apiKeyID);
    selectedAPIKeyID = apiKeyID;
    capabilitiesByModel.clear();
    serverConfig.models.forEach((item) => capabilitiesByModel.set(item.model, item.capability));
    const channel: ModelChannel = {
        id: `sub2api-${apiKeyID}`,
        name: apiKey ? `${apiKey.name} - ${apiKey.group_name}` : `Sub2API ${apiKeyID}`,
        baseUrl: "",
        apiKey: "",
        apiFormat: "openai",
        models: serverConfig.models.flatMap((item) => {
            const capability = serverModelCapability(item.capability.media_kind);
            return capability ? [{ name: item.model, capability }] : [];
        }),
    };
    const channels = [channel];
    const models = modelOptionsFromChannels(channels);
    const preferred = useCanvasSessionStore.getState().model;
    const pick = (capability: ModelCapability, stored: string) => {
        const options = channel.models.filter((item) => item.capability === capability).map((item) => encodeChannelModel(channel.id, item.name));
        const normalizedStored = normalizeModelOptionValue(stored, channels);
        const normalizedPreferred = normalizeModelOptionValue(preferred, channels);
        return options.includes(normalizedStored) ? normalizedStored : options.includes(normalizedPreferred) ? normalizedPreferred : options[0] || "";
    };
    const imageModel = pick("image", current.imageModel || current.model);
    const videoModel = pick("video", current.videoModel);
    const textModel = pick("text", current.textModel);
    const audioModel = pick("audio", current.audioModel);
    const model = imageModel || videoModel || textModel || audioModel;
    useConfigStore.setState({
        canvasConfig: serverConfig,
        canvasConfigLoading: false,
        canvasConfigError: "",
        config: {
            ...current,
            channelMode: "local",
            baseUrl: "",
            apiKey: "",
            apiFormat: "openai",
            channels,
            models,
            model,
            imageModel,
            videoModel,
            textModel,
            audioModel,
        },
    });
    useCanvasSessionStore.getState().setSelection(apiKeyID, modelOptionName(model));
}

function serverModelCapability(value: unknown): ModelCapability | undefined {
    return value === "image" || value === "video" || value === "audio" || value === "text" ? value : undefined;
}

function readSafePreferences(): Partial<AiConfig> {
    try {
        const value = JSON.parse(localStorage.getItem(SAFE_PREFERENCES_KEY) || "{}") as Partial<AiConfig>;
        return Object.fromEntries(safePreferenceKeys.flatMap((key) => value[key] === undefined ? [] : [[key, value[key]]])) as Partial<AiConfig>;
    } catch {
        return {};
    }
}

function writeSafePreferences(config: AiConfig): void {
    const value = Object.fromEntries(safePreferenceKeys.map((key) => [key, config[key]]));
    localStorage.setItem(SAFE_PREFERENCES_KEY, JSON.stringify(value));
}
