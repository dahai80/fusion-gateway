import { BrowserRouter, Routes, Route, Navigate } from "react-router-dom";
import { ConfigProvider } from "antd";
import AppLayout from "./components/AppLayout";
import Login from "./pages/Login";
import Dashboard from "./pages/Dashboard";
import Keys from "./pages/Keys";
import Channels from "./pages/Channels";
import Logs from "./pages/Logs";
import Analytics from "./pages/Analytics";
import ServerConfig from "./pages/config/ServerConfig";
import AuthConfig from "./pages/config/AuthConfig";
import RateLimitConfig from "./pages/config/RateLimitConfig";
import RetryConfig from "./pages/config/RetryConfig";
import CacheConfig from "./pages/config/CacheConfig";
import CostConfig from "./pages/config/CostConfig";
import CORSConfig from "./pages/config/CORSConfig";
import HardwareConfig from "./pages/config/HardwareConfig";
import NegotiationConfig from "./pages/config/NegotiationConfig";
import CloudRoutingConfig from "./pages/config/CloudRoutingConfig";
import TokenizerConfig from "./pages/config/TokenizerConfig";
import ObservabilityConfig from "./pages/config/ObservabilityConfig";
import HotReloadConfig from "./pages/config/HotReloadConfig";
import ClusterConfig from "./pages/config/ClusterConfig";
import RealtimeConfig from "./pages/config/RealtimeConfig";
import OIDCConfig from "./pages/config/OIDCConfig";
import RBACConfig from "./pages/config/RBACConfig";
import SemanticCacheConfig from "./pages/config/SemanticCacheConfig";
import PromptInjectionConfig from "./pages/config/PromptInjectionConfig";
import BatchConfig from "./pages/config/BatchConfig";
import StoreConfig from "./pages/config/StoreConfig";
import ValidationConfig from "./pages/config/ValidationConfig";
import CostMarkupConfig from "./pages/config/CostMarkupConfig";
import AdminConfigPage from "./pages/config/AdminConfigPage";
import PIIConfig from "./pages/config/PIIConfig";

function getCookie(name: string): string | undefined {
    const match = document.cookie.match(
        new RegExp("(?:^|; )" + name.replace(/([.$?*|{}()\[\]\\\/+^])/g, "\\$1") + "=([^;]*)")
    );
    return match ? decodeURIComponent(match[1]) : undefined;
}

function PrivateRoute({ children }: { children: React.ReactNode }) {
    const token = getCookie("admin_token");
    if (!token) {
        return <Navigate to="/admin/login" replace />;
    }
    return <>{children}</>;
}

export default function App() {
    return (
        <ConfigProvider
            theme={{
                token: {
                    colorPrimary: "#1677ff",
                },
            }}
        >
            <BrowserRouter>
                <Routes>
                    <Route path="/admin/login" element={<Login />} />
                    <Route
                        path="/admin/"
                        element={
                            <PrivateRoute>
                                <AppLayout />
                            </PrivateRoute>
                        }
                    >
                        <Route index element={<Dashboard />} />
                        <Route path="keys" element={<Keys />} />
                        <Route path="channels" element={<Channels />} />
                        <Route path="logs" element={<Logs />} />
                        <Route path="analytics" element={<Analytics />} />
                        <Route path="settings" element={<Navigate to="/admin/config/server" replace />} />
                        <Route path="config/server" element={<ServerConfig />} />
                        <Route path="config/auth" element={<AuthConfig />} />
                        <Route path="config/rate-limit" element={<RateLimitConfig />} />
                        <Route path="config/retry" element={<RetryConfig />} />
                        <Route path="config/cache" element={<CacheConfig />} />
                        <Route path="config/cost" element={<CostConfig />} />
                        <Route path="config/cost-markup" element={<CostMarkupConfig />} />
                        <Route path="config/pii" element={<PIIConfig />} />
                        <Route path="config/prompt-injection" element={<PromptInjectionConfig />} />
                        <Route path="config/oidc" element={<OIDCConfig />} />
                        <Route path="config/rbac" element={<RBACConfig />} />
                        <Route path="config/negotiation" element={<NegotiationConfig />} />
                        <Route path="config/cloud-routing" element={<CloudRoutingConfig />} />
                        <Route path="config/hardware" element={<HardwareConfig />} />
                        <Route path="config/tokenizer" element={<TokenizerConfig />} />
                        <Route path="config/observability" element={<ObservabilityConfig />} />
                        <Route path="config/cors" element={<CORSConfig />} />
                        <Route path="config/hot-reload" element={<HotReloadConfig />} />
                        <Route path="config/cluster" element={<ClusterConfig />} />
                        <Route path="config/realtime" element={<RealtimeConfig />} />
                        <Route path="config/admin" element={<AdminConfigPage />} />
                        <Route path="config/semantic-cache" element={<SemanticCacheConfig />} />
                        <Route path="config/batch" element={<BatchConfig />} />
                        <Route path="config/store" element={<StoreConfig />} />
                        <Route path="config/validation" element={<ValidationConfig />} />
                    </Route>
                    <Route path="*" element={<Navigate to="/admin/" replace />} />
                </Routes>
            </BrowserRouter>
        </ConfigProvider>
    );
}
