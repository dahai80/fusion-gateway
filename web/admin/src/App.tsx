import { BrowserRouter, Routes, Route, Navigate } from "react-router-dom";
import { ConfigProvider } from "antd";
import AppLayout from "./components/AppLayout";
import Login from "./pages/Login";
import Keys from "./pages/Keys";
import Channels from "./pages/Channels";
import ServerConfig from "./pages/config/ServerConfig";
import AuthConfig from "./pages/config/AuthConfig";
import RateLimitConfig from "./pages/config/RateLimitConfig";
import RetryConfig from "./pages/config/RetryConfig";
import CacheConfig from "./pages/config/CacheConfig";
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
import PersistenceConfig from "./pages/config/PersistenceConfig";
import Latency from "./pages/insights/Latency";
import Compare from "./pages/insights/Compare";
import Explorer from "./pages/insights/Explorer";
import Optimize from "./pages/insights/Optimize";
import Anomalies from "./pages/insights/Anomalies";
import Reports from "./pages/insights/Reports";
import Capacity from "./pages/insights/Capacity";

function PrivateRoute({ children }: { children: React.ReactNode }) {
    const loggedIn = localStorage.getItem("admin_logged_in");
    if (!loggedIn) {
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
                        {/* Legacy Dashboard/Logs/Analytics deprecated in favor of
                            PG-backed Insights; routes kept as redirects. */}
                        <Route index element={<Navigate to="/admin/insights/explorer" replace />} />
                        <Route path="keys" element={<Keys />} />
                        <Route path="channels" element={<Channels />} />
                        <Route path="logs" element={<Navigate to="/admin/insights/explorer" replace />} />
                        <Route path="analytics" element={<Navigate to="/admin/insights/latency" replace />} />
                        <Route path="insights/latency" element={<Latency />} />
                        <Route path="insights/compare" element={<Compare />} />
                        <Route path="insights/explorer" element={<Explorer />} />
                        <Route path="insights/optimize" element={<Optimize />} />
                        <Route path="insights/anomalies" element={<Anomalies />} />
                        <Route path="insights/reports" element={<Reports />} />
                        <Route path="insights/capacity" element={<Capacity />} />
                        <Route path="settings" element={<Navigate to="/admin/config/server" replace />} />
                        <Route path="config/server" element={<ServerConfig />} />
                        <Route path="config/auth" element={<AuthConfig />} />
                        <Route path="config/rate-limit" element={<RateLimitConfig />} />
                        <Route path="config/retry" element={<RetryConfig />} />
                        <Route path="config/cache" element={<CacheConfig />} />
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
                        <Route path="config/persistence" element={<PersistenceConfig />} />
                    </Route>
                    <Route path="*" element={<Navigate to="/admin/" replace />} />
                </Routes>
            </BrowserRouter>
        </ConfigProvider>
    );
}
