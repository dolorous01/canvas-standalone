import { useMemo, useState } from "react";
import { Button, Dropdown, type MenuProps, Tooltip } from "antd";
import { Check, KeyRound, LoaderCircle, Settings, TriangleAlert } from "lucide-react";
import { useTranslation } from "react-i18next";

import { useCanvasHost } from "@sub2api/host-context";
import { selectCanvasAPIKey, useConfigStore } from "@sub2api/adapters/use-config-store";
import { useCanvasSessionStore } from "@sub2api/stores/canvas-session-store";
import type { CanvasAPIKey } from "@sub2api/api/canvas-api";
import { canvasAPIKeyManagementPath } from "./canvas-api-key-navigation";

export function CanvasAPIKeyMenu({ showLabel = true }: { showLabel?: boolean }) {
    const { t } = useTranslation();
    const host = useCanvasHost();
    const config = useConfigStore((state) => state.canvasConfig);
    const loading = useConfigStore((state) => state.canvasConfigLoading);
    const error = useConfigStore((state) => state.canvasConfigError);
    const selectedID = useCanvasSessionStore((state) => state.apiKeyID);
    const [switching, setSwitching] = useState(false);
    const selected = config?.api_keys.find((item) => item.id === selectedID);

    const items = useMemo<MenuProps["items"]>(() => {
        const result: NonNullable<MenuProps["items"]> = [];
        if (config && !config.enabled) {
            result.push({
                key: "policy-disabled",
                disabled: true,
                icon: <TriangleAlert className="size-4 text-amber-500" />,
                label: <span className="text-amber-700 dark:text-amber-300">{t("canvas.apiKeys.policyDisabled")}</span>,
            });
            result.push({ type: "divider" });
        }
        for (const key of config?.api_keys || []) {
            const reason = key.available === false ? unavailableReason(t, key) : "";
            result.push({
                key: `key:${key.id}`,
                disabled: key.available === false,
                icon: key.id === selectedID ? <Check className="size-4 text-emerald-600" /> : <KeyRound className="size-4 opacity-45" />,
                label: (
                    <div className="min-w-56 py-0.5">
                        <div className="truncate font-medium">{key.name}</div>
                        <div className={`mt-0.5 truncate text-xs ${reason ? "text-amber-600 dark:text-amber-300" : "opacity-55"}`}>
                            {reason || key.group_name || t("canvas.apiKeys.noGroup")}
                        </div>
                    </div>
                ),
            });
        }
        if (!config?.api_keys.length) {
            result.push({ key: "empty", disabled: true, label: t("canvas.apiKeys.empty") });
        }
        result.push({ type: "divider" });
        result.push({ key: "manage", icon: <Settings className="size-4" />, label: t("canvas.apiKeys.manage") });
        return result;
    }, [config, selectedID, t]);

    const onClick: MenuProps["onClick"] = async ({ key }) => {
        if (key === "manage") {
            host.navigate(canvasAPIKeyManagementPath());
            return;
        }
        if (!key.startsWith("key:")) return;
        const id = Number(key.slice(4));
        if (!Number.isSafeInteger(id) || id <= 0 || id === selectedID) return;
        setSwitching(true);
        try {
            await selectCanvasAPIKey(id);
        } finally {
            setSwitching(false);
        }
    };

    const busy = loading || switching;
    const title = error || (!config?.enabled ? t("canvas.apiKeys.policyDisabled") : selected ? `${selected.name} · ${selected.group_name}` : t("canvas.apiKeys.select"));
    return (
        <Tooltip title={title}>
            <Dropdown trigger={["click"]} menu={{ items, onClick }} placement="bottomRight">
                <Button
                    type="text"
                    aria-label={t("canvas.apiKeys.select")}
                    className="canvas-api-key-trigger !h-9 !max-w-[220px] !rounded-lg !px-2.5"
                    icon={busy ? <LoaderCircle className="size-4 animate-spin" /> : !config?.enabled ? <TriangleAlert className="size-4 text-amber-500" /> : <KeyRound className="size-4" />}
                >
                    {showLabel ? <span className="canvas-api-key-label max-w-36 truncate">{selected?.name || t("canvas.apiKeys.select")}</span> : null}
                </Button>
            </Dropdown>
        </Tooltip>
    );
}

function unavailableReason(t: (key: string) => string, key: CanvasAPIKey): string {
    return key.unavailable_reason ? t(`canvas.apiKeys.reasons.${key.unavailable_reason}`) : t("canvas.apiKeys.reasons.unknown");
}
