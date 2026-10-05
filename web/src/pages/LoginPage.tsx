import { useEffect, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { ArrowRightRegular } from "@fluentui/react-icons";
import { api } from "../api";
import { useI18n } from "../lib/i18n";
import { BrandLogo } from "../components/shell/BrandLogo";

export default function LoginPage() {
  const { t } = useI18n();
  const [searchParams] = useSearchParams();
  const [enabled, setEnabled] = useState(false);
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    let active = true;
    api<{ enabled: boolean }>("/auth/config")
      .then((config) => { if (active) setEnabled(config.enabled); })
      .catch(() => { if (active) setEnabled(false); })
      .finally(() => { if (active) setLoading(false); });
    return () => { active = false; };
  }, []);

  return (
    <div className="relative flex h-full w-full items-center justify-center overflow-hidden">
      <div className="animate-pulse-slow absolute -left-32 -top-32 h-[520px] w-[520px] rounded-full bg-indigo-500/15 blur-[120px] dark:bg-indigo-500/20" />
      <div className="animate-pulse-slow absolute -bottom-32 -right-32 h-[520px] w-[520px] rounded-full bg-indigo-500/12 blur-[120px] [animation-delay:2s] dark:bg-indigo-500/16" />
      <div className="relative w-full max-w-md p-1">
        <div className="group relative overflow-hidden rounded-2xl border border-gray-100 bg-white/70 p-8 shadow-2xl backdrop-blur-xl dark:border-white/10 dark:bg-[#141418]/70">
          <div className="pointer-events-none absolute inset-0 bg-gradient-to-br from-indigo-500/8 to-transparent opacity-0 transition-opacity duration-500 group-hover:opacity-100" />
          <div className="relative z-10 mb-10 text-center">
            <div className="mx-auto mb-6 flex h-20 w-20 items-center justify-center rounded-2xl bg-white shadow-lg shadow-indigo-500/20 ring-1 ring-black/5 transition-transform duration-300 group-hover:scale-105 dark:bg-white/10 dark:ring-white/10">
              <BrandLogo className="h-14 w-14" />
            </div>
            <h2 className="bg-gradient-to-r from-gray-900 to-gray-600 bg-clip-text text-3xl font-bold text-transparent dark:from-white dark:to-gray-400">
              vocat
            </h2>
            <p className="mt-3 text-sm tracking-wide text-gray-500 dark:text-gray-400">{t("高通模块专业测试工具")}</p>
          </div>
          <div className="relative z-10 space-y-6">
            <p className="text-center text-sm text-gray-500 dark:text-gray-400">
              {t("使用 Pocket ID 安全登录")}
            </p>
            {searchParams.has("error") && (
              <p role="alert" className="text-center text-sm text-red-600 dark:text-red-400">
                {t("Pocket ID 登录未完成，请重试。")}
              </p>
            )}
            {!loading && !enabled && (
              <p role="alert" className="text-center text-sm text-amber-600 dark:text-amber-400">
                {t("Pocket ID 暂不可用，请联系管理员。")}
              </p>
            )}
            <button
              type="button"
              disabled={loading || !enabled}
              onClick={() => {
                const query = new URLSearchParams({ redirect: searchParams.get("redirect") || "/" });
                window.location.assign(`/api/auth/oidc/start?${query.toString()}`);
              }}
              className="flex w-full items-center justify-center gap-2 rounded-lg bg-[#0ea5e9] px-4 py-3 font-bold text-white shadow-sm transition-all duration-200 hover:bg-[#0284c7] active:scale-95 disabled:cursor-not-allowed disabled:opacity-70"
            >
              {loading ? t("加载中") : t("使用 Pocket ID 登录")}
              <ArrowRightRegular className="h-5 w-5" />
            </button>
          </div>
        </div>
        <div className="mt-6 text-center">
          <p className="text-xs text-gray-500">vocat © 2026</p>
        </div>
      </div>
    </div>
  );
}
