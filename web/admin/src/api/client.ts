import axios from "axios";

function getCookie(name: string): string | undefined {
    const match = document.cookie.match(
        new RegExp("(?:^|; )" + name.replace(/([.$?*|{}()\[\]\\\/+^])/g, "\\$1") + "=([^;]*)")
    );
    return match ? decodeURIComponent(match[1]) : undefined;
}

const client = axios.create({
    baseURL: "/admin/api",
    timeout: 30000,
    headers: {
        "Content-Type": "application/json",
    },
});

client.interceptors.request.use(
    (config) => {
        const token = getCookie("admin_token");
        if (token) {
            config.headers.Authorization = `Bearer ${token}`;
        }
        return config;
    },
    (error) => {
        return Promise.reject(error);
    }
);

client.interceptors.response.use(
    (response) => {
        return response;
    },
    (error) => {
        if (error.response?.status === 401) {
            document.cookie = "admin_token=; path=/; max-age=0";
            window.location.href = "/admin/login";
        }
        return Promise.reject(error);
    }
);

export default client;
