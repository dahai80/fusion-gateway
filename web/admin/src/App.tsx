import { BrowserRouter, Routes, Route, Navigate } from "react-router-dom";
import { ConfigProvider } from "antd";
import AppLayout from "./components/AppLayout";
import Login from "./pages/Login";
import Dashboard from "./pages/Dashboard";
import Keys from "./pages/Keys";
import Channels from "./pages/Channels";
import Logs from "./pages/Logs";
import Analytics from "./pages/Analytics";

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
                    </Route>
                    <Route path="*" element={<Navigate to="/admin/" replace />} />
                </Routes>
            </BrowserRouter>
        </ConfigProvider>
    );
}
