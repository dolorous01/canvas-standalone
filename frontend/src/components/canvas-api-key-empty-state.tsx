import { Button } from "antd";
import { KeyRound, RefreshCw, Settings, TriangleAlert } from "lucide-react";
import { useTranslation } from "react-i18next";

import { useCanvasHost } from "@sub2api/host-context";
import { canvasAPIKeyManagementPath } from "./canvas-api-key-navigation";

export function CanvasAPIKeyEmptyState({ unavailable = false }: { unavailable?: boolean }) {
    const { t } = useTranslation();
    const host = useCanvasHost();
    const Icon = unavailable ? TriangleAlert : KeyRound;

    return (
        <main className="flex h-full min-h-[480px] items-center justify-center bg-background px-6 text-stone-950 dark:text-stone-100">
            <section className="flex max-w-md flex-col items-center text-center">
                <span className={`grid size-14 place-items-center rounded-full ${unavailable ? "bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300" : "bg-stone-100 text-stone-700 dark:bg-stone-800 dark:text-stone-200"}`} aria-hidden="true">
                    <Icon className="size-6" />
                </span>
                <h1 className="mt-5 text-xl font-semibold">{t(unavailable ? "canvas.apiKeys.unavailableTitle" : "canvas.apiKeys.emptyTitle")}</h1>
                <p className="mt-2 text-sm leading-6 text-stone-500 dark:text-stone-400">{t(unavailable ? "canvas.apiKeys.unavailableDescription" : "canvas.apiKeys.emptyDescription")}</p>
                <Button type="primary" size="large" className="mt-6" icon={unavailable ? <Settings className="size-4" /> : <KeyRound className="size-4" />} onClick={() => host.navigate(canvasAPIKeyManagementPath(!unavailable))}>
                    {t(unavailable ? "canvas.apiKeys.manage" : "canvas.apiKeys.create")}
                </Button>
            </section>
        </main>
    );
}

export function CanvasConfigErrorState({ message, onRetry }: { message?: string; onRetry: () => void }) {
    const { t } = useTranslation();
    const host = useCanvasHost();

    return (
        <main className="flex h-full min-h-[480px] items-center justify-center bg-background px-6 text-stone-950 dark:text-stone-100">
            <section className="flex max-w-lg flex-col items-center text-center">
                <span className="grid size-14 place-items-center rounded-full bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-300" aria-hidden="true">
                    <TriangleAlert className="size-6" />
                </span>
                <h1 className="mt-5 text-xl font-semibold">{t("canvas.apiKeys.configErrorTitle")}</h1>
                <p className="mt-2 text-sm leading-6 text-stone-500 dark:text-stone-400">{t("canvas.apiKeys.configErrorDescription")}</p>
                {message ? <p className="mt-3 max-w-full break-words text-xs text-red-600 dark:text-red-300">{message}</p> : null}
                <div className="mt-6 flex flex-wrap justify-center gap-3">
                    <Button type="primary" size="large" icon={<RefreshCw className="size-4" />} onClick={onRetry}>
                        {t("canvas.apiKeys.retry")}
                    </Button>
                    <Button size="large" icon={<Settings className="size-4" />} onClick={() => host.navigate(canvasAPIKeyManagementPath())}>
                        {t("canvas.apiKeys.manage")}
                    </Button>
                </div>
            </section>
        </main>
    );
}
