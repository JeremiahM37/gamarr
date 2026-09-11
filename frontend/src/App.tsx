import {
  FormEvent,
  ReactNode,
  useCallback,
  useEffect,
  useMemo,
  useState,
} from "react";
import {
  ApiError,
  AuthStatus,
  Config,
  Download,
  ImportCheck,
  LibraryItem,
  LibraryResponse,
  Monitor,
  Platform,
  SearchResponse,
  SearchResult,
  Settings,
  Stats,
  WishlistItem,
  json,
  request,
} from "./api";

type Tab = "search" | "library" | "downloads" | "wishlist" | "settings";
type AuthMode = "login" | "totp" | "register";
type Toast = { id: number; text: string; kind: "success" | "error" };
const tabs: Tab[] = ["search", "library", "downloads", "wishlist", "settings"];
const label = (tab: Tab) => tab.charAt(0).toUpperCase() + tab.slice(1);
const bytes = (value?: number) =>
  !value
    ? ""
    : ["B", "KB", "MB", "GB", "TB"]
        .reduce<{
          n: number;
          i: number;
        }>(
          (state) =>
            state.n >= 1024 && state.i < 4
              ? { n: state.n / 1024, i: state.i + 1 }
              : state,
          { n: value, i: 0 },
        )
        .n.toFixed(1) +
      " " +
      ["B", "KB", "MB", "GB", "TB"].reduce(
        (index, _, current) => (value >= 1024 ** current ? current : index),
        0,
      );

function useToasts() {
  const [items, setItems] = useState<Toast[]>([]);
  const add = useCallback((text: string, kind: Toast["kind"]) => {
    const item = { id: Date.now() + Math.random(), text, kind };
    setItems((old) => [...old, item]);
    window.setTimeout(
      () => setItems((old) => old.filter((toast) => toast.id !== item.id)),
      4000,
    );
  }, []);
  return { items, add };
}

function ToastRegion({ items }: { items: Toast[] }) {
  return (
    <div
      className="fixed bottom-4 right-4 z-50 flex flex-col gap-2 max-w-sm"
      aria-live="polite"
    >
      {items.map((toast) => (
        <div
          key={toast.id}
          className={`rounded-lg border px-4 py-3 text-sm shadow-lg ${toast.kind === "error" ? "bg-red-950 border-red-700 text-red-200" : "bg-emerald-950 border-emerald-700 text-emerald-200"}`}
        >
          {toast.text}
        </div>
      ))}
    </div>
  );
}

function AuthGate({
  auth,
  expired,
  onSignedIn,
}: {
  auth: AuthStatus | null;
  expired: boolean;
  onSignedIn: () => Promise<void>;
}) {
  const [mode, setMode] = useState<AuthMode>("login");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [invite, setInvite] = useState("");
  const [code, setCode] = useState("");
  const [pending, setPending] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const loginAvailable = auth?.login_available !== false;
  const signup = Boolean(auth?.has_users || auth?.can_register);
  const submitLogin = async (event: FormEvent) => {
    event.preventDefault();
    if (!username || !password)
      return setError("Enter a username and password.");
    setBusy(true);
    setError("");
    try {
      const data = await request<{
        success?: boolean;
        error?: string;
        needs_totp?: boolean;
        session_pending?: string;
      }>("/api/login", json("POST", { username, password }));
      if (data.needs_totp) {
        setPending(data.session_pending ?? "");
        setMode("totp");
        return;
      }
      setPassword("");
      await onSignedIn();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Sign in failed.");
    } finally {
      setBusy(false);
    }
  };
  const submitTOTP = async (event: FormEvent) => {
    event.preventDefault();
    if (!code) return setError("Enter your authentication code.");
    setBusy(true);
    setError("");
    try {
      await request(
        "/api/login/totp",
        json("POST", { session_pending: pending, code }),
      );
      setCode("");
      setPassword("");
      await onSignedIn();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Verification failed.");
    } finally {
      setBusy(false);
    }
  };
  const submitRegister = async (event: FormEvent) => {
    event.preventDefault();
    if (!username || !password)
      return setError("Enter a username and password.");
    setBusy(true);
    setError("");
    try {
      const data = await request<{ token?: string }>(
        "/api/register",
        json("POST", {
          username,
          password,
          ...(invite ? { invite_code: invite } : {}),
        }),
      );
      setPassword("");
      if (data.token) await onSignedIn();
      else {
        setMode("login");
        setError("Account created. Sign in to continue.");
      }
    } catch (cause) {
      setError(
        cause instanceof Error
          ? cause.message
          : "Could not create the account.",
      );
    } finally {
      setBusy(false);
    }
  };
  const heading =
    mode === "register"
      ? auth?.can_register
        ? "Create admin account"
        : "Create account"
      : mode === "totp"
        ? "Two-factor code"
        : "Sign in";
  return (
    <main
      id="auth-gate"
      className="min-h-screen flex items-center justify-center px-4"
    >
      <div className="w-full max-w-sm">
        <div className="flex items-center gap-3 mb-6 justify-center">
          <div className="w-10 h-10 bg-indigo-600 rounded-lg flex items-center justify-center text-xl">
            🎮
          </div>
          <h1 className="text-2xl font-bold text-white">Gamarr</h1>
        </div>
        <div className="bg-slate-900 border border-slate-800 rounded-xl p-6">
          <h2 className="text-sm font-semibold text-slate-300 uppercase tracking-wider mb-4">
            {heading}
          </h2>
          {expired && mode === "login" && (
            <p className="text-sm text-slate-400 mb-3">
              Your session expired. Please sign in again.
            </p>
          )}
          {mode === "login" &&
            (loginAvailable ? (
              <form
                id="login-form"
                onSubmit={submitLogin}
                className="space-y-4"
              >
                <Field label="Username">
                  <input
                    id="login-username"
                    autoFocus
                    value={username}
                    onChange={(e) => setUsername(e.target.value)}
                    autoComplete="username"
                    className="input"
                  />
                </Field>
                <Field label="Password">
                  <input
                    id="login-password"
                    type="password"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    autoComplete="current-password"
                    className="input"
                  />
                </Field>
                <Submit id="login-btn" busy={busy}>
                  Sign in
                </Submit>
              </form>
            ) : (
              <p id="auth-notice" className="text-sm text-slate-400">
                This instance is protected by an API key. Browser sign-in is not
                available.
              </p>
            ))}
          {mode === "totp" && (
            <form onSubmit={submitTOTP} className="space-y-4">
              <p className="text-xs text-slate-500">
                Enter the 6-digit code from your authenticator app, or a backup
                code.
              </p>
              <Field label="Code">
                <input
                  autoFocus
                  value={code}
                  onChange={(e) => setCode(e.target.value)}
                  autoComplete="one-time-code"
                  inputMode="numeric"
                  className="input"
                />
              </Field>
              <Submit busy={busy}>Verify</Submit>
              <button
                type="button"
                onClick={() => {
                  setMode("login");
                  setCode("");
                  setError("");
                }}
                className="w-full text-xs text-slate-400"
              >
                Back to sign in
              </button>
            </form>
          )}
          {mode === "register" && (
            <form
              id="register-form"
              onSubmit={submitRegister}
              className="space-y-4"
            >
              <p className="text-xs text-slate-500">
                {auth?.can_register
                  ? "This account becomes the administrator."
                  : "Registration requires an invite code from an administrator."}
              </p>
              <Field label="Username">
                <input
                  id="register-username"
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  autoComplete="username"
                  className="input"
                />
              </Field>
              <Field label="Password">
                <input
                  id="register-password"
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  autoComplete="new-password"
                  className="input"
                />
              </Field>
              {!auth?.can_register && (
                <Field label="Invite code">
                  <input
                    id="register-invite"
                    value={invite}
                    onChange={(e) => setInvite(e.target.value)}
                    className="input"
                  />
                </Field>
              )}
              <Submit id="register-btn" busy={busy}>
                Create account
              </Submit>
            </form>
          )}
          {(error || expired) && (
            <p id="auth-error" className="mt-4 text-sm text-red-400">
              {error || "Your session expired. Please sign in again."}
            </p>
          )}
          {auth?.oidc_enabled && mode !== "totp" && (
            <button
              onClick={() => {
                window.location.href = "/api/oidc/login";
              }}
              className="w-full mt-5 pt-4 border-t border-slate-800 text-sm text-slate-300"
            >
              Sign in with {auth.oidc_provider || "SSO"}
            </button>
          )}
          {signup && mode !== "totp" && (
            <button
              onClick={() => {
                setMode(mode === "register" ? "login" : "register");
                setError("");
              }}
              id="switch-auth-mode"
              className="w-full mt-4 text-xs text-indigo-400"
            >
              {mode === "register"
                ? "Back to sign in"
                : "Have an invite code? Create an account"}
            </button>
          )}
        </div>
      </div>
    </main>
  );
}
function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="block text-xs text-slate-400">
      <span className="block mb-1">{label}</span>
      {children}
    </label>
  );
}
function Submit({
  id,
  busy,
  children,
}: {
  id?: string;
  busy: boolean;
  children: ReactNode;
}) {
  return (
    <button
      id={id}
      disabled={busy}
      className="w-full bg-indigo-600 disabled:opacity-50 hover:bg-indigo-500 text-white font-semibold px-4 py-2.5 rounded-lg"
    >
      {busy ? "Working…" : children}
    </button>
  );
}

export function App() {
  const [auth, setAuth] = useState<AuthStatus | null | undefined>(undefined);
  const [tab, setTab] = useState<Tab>("search");
  const [menu, setMenu] = useState(false);
  const [expired, setExpired] = useState(false);
  const toasts = useToasts();
  const authStatus = useCallback(async () => {
    try {
      return await request<AuthStatus>("/api/auth/status");
    } catch {
      return null;
    }
  }, []);
  const refreshAuth = useCallback(async () => {
    const next = await authStatus();
    setAuth(next);
    setExpired(false);
  }, [authStatus]);
  useEffect(() => {
    void authStatus().then(setAuth);
  }, [authStatus]);
  const unauthorized = useCallback(() => {
    setExpired(true);
    setAuth((old) => ({
      ...old,
      authenticated: false,
      auth_required: true,
      login_available: old?.login_available ?? true,
    }));
  }, []);
  if (auth === undefined)
    return (
      <main className="min-h-screen flex items-center justify-center text-slate-500">
        <span className="spinner w-4 h-4 border-2 border-slate-600 border-t-indigo-500 rounded-full mr-3" />
        Loading…
      </main>
    );
  if (auth?.auth_required && !auth.authenticated)
    return (
      <>
        <AuthGate auth={auth} expired={expired} onSignedIn={refreshAuth} />
        <ToastRegion items={toasts.items} />
      </>
    );
  return (
    <>
      <AppShell
        auth={auth}
        tab={tab}
        setTab={setTab}
        menu={menu}
        setMenu={setMenu}
        unauthorized={unauthorized}
        toast={toasts.add}
        onLogout={async () => {
          await request("/api/logout", json("POST")).catch(() => undefined);
          setAuth(await authStatus());
          setExpired(false);
        }}
      />
      <ToastRegion items={toasts.items} />
    </>
  );
}

function AppShell({
  auth,
  tab,
  setTab,
  menu,
  setMenu,
  unauthorized,
  toast,
  onLogout,
}: {
  auth: AuthStatus | null;
  tab: Tab;
  setTab: (tab: Tab) => void;
  menu: boolean;
  setMenu: (value: boolean) => void;
  unauthorized: () => void;
  toast: (text: string, kind: Toast["kind"]) => void;
  onLogout: () => Promise<void>;
}) {
  const [count, setCount] = useState(0);
  const [libraryTotal, setLibraryTotal] = useState<number>();
  const [searchRequest, setSearchRequest] = useState<{
    title: string;
    nonce: number;
  }>();
  const call = useCallback(
    async <T,>(path: string, init?: RequestInit) => {
      try {
        return await request<T>(path, init);
      } catch (cause) {
        if ((cause as ApiError).status === 401) unauthorized();
        throw cause;
      }
    },
    [unauthorized],
  );
  useEffect(() => {
    let alive = true;
    const update = async () => {
      try {
        const data = await call<{ downloads?: Download[] }>("/api/downloads");
        if (alive)
          setCount(
            (data.downloads ?? []).filter(
              (item) => !["completed", "failed"].includes(item.status ?? ""),
            ).length,
          );
      } catch {
        /* session handler owns visible failure */
      }
    };
    void update();
    const timer = window.setInterval(() => void update(), 15000);
    return () => {
      alive = false;
      window.clearInterval(timer);
    };
  }, [call]);
  useEffect(() => {
    void call<Stats>("/api/stats")
      .then((data) => setLibraryTotal(data.library_total))
      .catch(() => undefined);
  }, [call]);
  return (
    <div id="app-root" className="min-h-screen flex flex-col">
      <header className="bg-slate-900/80 backdrop-blur-md border-b border-slate-800 sticky top-0 z-30">
        <div className="max-w-7xl mx-auto px-4 sm:px-6 h-16 flex items-center justify-between">
          <div>
            <h1 className="font-bold text-white">Gamarr</h1>
            <p className="text-xs text-slate-400 hidden sm:block">
              Game Search &amp; Download
            </p>
          </div>
          <div className="flex items-center gap-3">
            <span className="text-xs text-slate-500 hidden sm:block">
              {libraryTotal ?? 0} games
            </span>
            {auth?.authenticated && (
              <>
                <span id="user-name" className="text-xs text-slate-400">
                  {auth.username}
                </span>
                <button
                  onClick={() => void onLogout()}
                  id="logout-btn"
                  className="text-xs bg-slate-800 border border-slate-700 px-2.5 py-1 rounded-lg"
                >
                  Sign out
                </button>
              </>
            )}
            <button
              aria-label="Toggle navigation"
              onClick={() => setMenu(!menu)}
              className="sm:hidden text-xl"
            >
              ☰
            </button>
          </div>
        </div>
        <div
          id="main-nav"
          className={`max-w-7xl mx-auto px-4 sm:px-6 mobile-nav ${menu ? "open" : ""}`}
          role="navigation"
        >
          {tabs.map((item) => (
            <button
              key={item}
              data-tab={item}
              onClick={() => {
                setTab(item);
                setMenu(false);
              }}
              className={`nav-tab px-4 py-2.5 text-sm ${tab === item ? "active" : "text-slate-400"}`}
            >
              {label(item)}
              {item === "downloads" && count > 0 && (
                <span className="ml-1 px-1.5 py-0.5 text-xs bg-indigo-600 text-white rounded-full">
                  {count}
                </span>
              )}
            </button>
          ))}
        </div>
      </header>
      <main className="max-w-7xl mx-auto w-full px-4 sm:px-6 py-6">
        {tab === "search" && (
          <SearchTab
            call={call}
            toast={toast}
            requestedSearch={searchRequest}
          />
        )}
        {tab === "library" && <LibraryTab call={call} />}
        {tab === "downloads" && <DownloadsTab call={call} toast={toast} />}
        {tab === "wishlist" && (
          <WishlistTab
            call={call}
            search={(title) => {
              setSearchRequest({ title, nonce: Date.now() });
              setTab("search");
            }}
          />
        )}
        {tab === "settings" && <SettingsTab call={call} toast={toast} />}
      </main>
    </div>
  );
}

type Call = <T>(path: string, init?: RequestInit) => Promise<T>;
function usePlatforms(call: Call) {
  const [platforms, setPlatforms] = useState<Platform[]>([]);
  useEffect(() => {
    void call<{ platforms: Platform[] }>("/api/platforms")
      .then((data) => setPlatforms(data.platforms ?? []))
      .catch(() => undefined);
  }, [call]);
  return platforms;
}
function SearchTab({
  call,
  toast,
  requestedSearch,
}: {
  call: Call;
  toast: (text: string, kind: Toast["kind"]) => void;
  requestedSearch?: { title: string; nonce: number };
}) {
  const platforms = usePlatforms(call);
  const [query, setQuery] = useState("");
  const [platform, setPlatform] = useState("all");
  const [result, setResult] = useState<SearchResponse>();
  const [busy, setBusy] = useState(false);
  const [sending, setSending] = useState<number>();
  const search = async (requestedQuery = query) => {
    const normalizedQuery = requestedQuery.trim();
    if (!normalizedQuery) return;
    setBusy(true);
    try {
      setResult(
        await call<SearchResponse>(
          `/api/search?q=${encodeURIComponent(normalizedQuery)}&platform=${encodeURIComponent(platform)}`,
        ),
      );
    } catch {
      toast("Search failed", "error");
    } finally {
      setBusy(false);
    }
  };
  useEffect(() => {
    if (!requestedSearch) return;
    setQuery(requestedSearch.title);
    void search(requestedSearch.title);
  }, [requestedSearch?.nonce]);
  const download = async (item: SearchResult, index: number) => {
    setSending(index);
    try {
      await call<{ success?: boolean; error?: string }>(
        "/api/download",
        json("POST", item),
      );
      toast(`Downloading: ${item.title}`, "success");
    } catch (cause) {
      toast(
        cause instanceof Error ? cause.message : "Download failed",
        "error",
      );
    } finally {
      setSending(undefined);
    }
  };
  const indexers = result?.sources?.find(
    (source) => source.name === "prowlarr",
  );
  return (
    <section id="tab-search">
      <h2 className="text-xl font-semibold text-white mb-5">Search games</h2>
      <div className="flex flex-col sm:flex-row gap-3 mb-4">
        <input
          id="search-input"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          onKeyDown={(event) => event.key === "Enter" && void search()}
          placeholder="Search for games..."
          className="input flex-1"
        />
        <select
          id="platform-filter"
          value={platform}
          onChange={(event) => setPlatform(event.target.value)}
          className="select"
        >
          {platforms.map((item) => (
            <option key={item.id} value={item.id}>
              {item.name}
            </option>
          ))}
        </select>
        <button
          id="search-btn"
          disabled={busy}
          onClick={() => void search()}
          className="primary"
        >
          {busy ? "Searching…" : "Search"}
        </button>
      </div>
      {result && (
        <>
          <p id="search-info" className="text-sm text-slate-500 mb-1">
            {result.results.length} results in {result.search_time_ms ?? 0}ms
          </p>
          {indexers?.enabled && (
            <p
              id="search-indexers"
              data-count={indexers.indexers?.length ?? 0}
              className="text-xs text-slate-600 mb-4"
            >
              {indexers.indexers?.length
                ? `Prowlarr searched: ${indexers.indexers.map((item) => item.name).join(", ")}`
                : "Prowlarr searched no indexers — none are enabled with game categories."}
            </p>
          )}
        </>
      )}
      <div
        id="results"
        className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-3"
      >
        {busy &&
          Array.from({ length: 6 }, (_, index) => (
            <div
              key={index}
              className="bg-slate-900 rounded-xl h-32 animate-pulse"
            />
          ))}
        {!busy &&
          result?.results.map((item, index) => (
            <SearchCard
              key={`${item.title}-${index}`}
              item={item}
              busy={sending === index}
              onDownload={() => void download(item, index)}
            />
          ))}
        {!busy && result && result.results.length === 0 && (
          <Empty icon="🔎">No results found</Empty>
        )}
      </div>
    </section>
  );
}
function SearchCard({
  item,
  busy,
  onDownload,
}: {
  item: SearchResult;
  busy: boolean;
  onDownload: () => void;
}) {
  const score = item.safety_score ?? 50;
  const direct = item.source_type === "ddl";
  return (
    <article
      className={`bg-slate-900 border border-slate-800 rounded-xl p-4 ${score < 40 ? "opacity-60" : ""}`}
    >
      <div className="flex gap-3">
        <div className="text-xs space-y-1">
          <span
            className={`block px-2 py-0.5 rounded font-bold ${item.is_pc ? "bg-orange-500/20 text-orange-400" : "bg-emerald-500/20 text-emerald-400"}`}
          >
            {item.platform}
          </span>
          <span className="block px-2 py-0.5 rounded bg-slate-800 text-slate-300">
            {direct
              ? "DDL"
              : item.download_protocol === "nzb"
                ? "NZB"
                : "Torrent"}
          </span>
        </div>
        <div className="flex-1 min-w-0">
          <h3 className="text-sm font-medium text-white break-words">
            {item.title}
          </h3>
          <p className="text-xs text-slate-400 mt-1">
            {item.indexer} ·{" "}
            {direct
              ? "Direct"
              : item.indexer === "Minerva" ? "" : `${item.seeders ?? 0} seeds · ${item.leechers ?? 0} leech`}{" "}
            · {item.size_human ?? "?"}
          </p>
          {item.safety_warnings?.length ? (
            <p className="text-xs text-red-400/70 mt-1">
              {item.safety_warnings.join(" · ")}
            </p>
          ) : null}
        </div>
        <button
          id="dl-btn"
          disabled={busy}
          onClick={onDownload}
          className="primary text-xs px-3"
        >
          {busy ? "…" : "DL"}
        </button>
      </div>
    </article>
  );
}
function Empty({ icon, children }: { icon: string; children: ReactNode }) {
  return (
    <div className="col-span-full text-center py-16 text-slate-500">
      <div className="text-4xl mb-3">{icon}</div>
      {children}
    </div>
  );
}

function LibraryTab({ call }: { call: Call }) {
  const platforms = usePlatforms(call);
  const [query, setQuery] = useState("");
  const [platform, setPlatform] = useState("all");
  const [data, setData] = useState<LibraryResponse>();
  const [page, setPage] = useState(1);
  const [config, setConfig] = useState<Config>();
  const load = useCallback(
    async (next = page) => {
      const response = await call<LibraryResponse>(
        `/api/library?page=${next}&q=${encodeURIComponent(query)}&platform=${encodeURIComponent(platform)}`,
      );
      setPage(response.page);
      setData(response);
    },
    [call, page, platform, query],
  );
  useEffect(() => {
    void load(1).catch(() => undefined);
    // Imports happen asynchronously; keep the visible library current while this tab is open.
    const timer = window.setInterval(
      () => void load().catch(() => undefined),
      5000,
    );
    return () => window.clearInterval(timer);
  }, [load]);
  useEffect(() => {
    void call<Config>("/api/config")
      .then(setConfig)
      .catch(() => undefined);
  }, [call]);
  return (
    <section>
      <div className="flex flex-col sm:flex-row gap-3 mb-4">
        <input
          id="lib-search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && void load(1)}
          placeholder="Filter library..."
          className="input flex-1"
        />
        <select
          id="lib-platform"
          value={platform}
          onChange={(e) => setPlatform(e.target.value)}
          className="select"
        >
          {platforms.map((item) => (
            <option key={item.id} value={item.id}>
              {item.name}
            </option>
          ))}
        </select>
        <a
          id="romm-link"
          target="_blank"
          rel="noreferrer"
          href={config?.romm_url ?? `http://${location.hostname}:8086`}
          className="secondary"
        >
          RomM
        </a>
        <a
          id="gamevault-link"
          target="_blank"
          rel="noreferrer"
          href={config?.gamevault_url ?? `http://${location.hostname}:8087`}
          className="secondary"
        >
          GameVault
        </a>
      </div>
      <p id="lib-stats" className="text-sm text-slate-500 mb-4">
        {data?.total ?? 0} items (page {data?.page ?? 1} of{" "}
        {data?.total_pages ?? 1})
      </p>
      <div
        id="library-grid"
        className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-4"
      >
        {data?.items.map((item, index) => (
          <LibraryCard key={item.id} item={item} color={index % 6} />
        ))}
        {data && data.items.length === 0 && (
          <Empty icon="🎮">No games in library</Empty>
        )}
      </div>
      <Pagination
        page={data?.page ?? 1}
        total={data?.total_pages ?? 1}
        setPage={(next) => void load(next)}
      />
    </section>
  );
}
function LibraryCard({ item, color }: { item: LibraryItem; color: number }) {
  const gradients = [
    "from-indigo-600 to-purple-600",
    "from-emerald-600 to-teal-600",
    "from-orange-600 to-red-600",
    "from-pink-600 to-rose-600",
    "from-cyan-600 to-blue-600",
    "from-violet-600 to-fuchsia-600",
  ];
  return (
    <article className="bg-slate-900 border border-slate-800 rounded-xl overflow-hidden">
      <div
        className={`h-24 bg-gradient-to-br ${gradients[color]} flex items-center justify-center`}
      >
        <span className="text-3xl font-bold text-white/30">
          {(item.platform_slug ?? item.platform).toUpperCase().slice(0, 4)}
        </span>
      </div>
      <div className="p-3">
        <h3
          className="text-sm font-medium text-white truncate"
          title={item.title}
        >
          {item.title}
        </h3>
        <p className="text-xs text-slate-500 mt-1">
          {item.platform}
          {item.file_size ? ` · ${bytes(item.file_size)}` : ""}
        </p>
      </div>
    </article>
  );
}
function Pagination({
  page,
  total,
  setPage,
}: {
  page: number;
  total: number;
  setPage: (page: number) => void;
}) {
  if (total <= 1) return null;
  const values = Array.from(
    { length: Math.min(7, total) },
    (_, index) => Math.max(1, Math.min(total - 6, page - 3)) + index,
  );
  return (
    <div id="lib-pagination" className="flex justify-center gap-2 mt-6">
      {page > 1 && (
        <button className="secondary" onClick={() => setPage(page - 1)}>
          Prev
        </button>
      )}
      {values.map((value) => (
        <button
          key={value}
          onClick={() => setPage(value)}
          className={value === page ? "primary" : "secondary"}
        >
          {value}
        </button>
      ))}
      {page < total && (
        <button className="secondary" onClick={() => setPage(page + 1)}>
          Next
        </button>
      )}
    </div>
  );
}

function DownloadsTab({
  call,
  toast,
}: {
  call: Call;
  toast: (text: string, kind: Toast["kind"]) => void;
}) {
  const [items, setItems] = useState<Download[]>([]);
  const [archiveOpen, setArchiveOpen] = useState<Record<string,boolean>>({});
  const load = useCallback(async () => {
    const data = await call<{ downloads?: Download[] }>("/api/downloads");
    setItems(data.downloads ?? []);
  }, [call]);
  useEffect(() => {
    void load().catch(() => undefined);
    const timer = window.setInterval(
      () => void load().catch(() => undefined),
      5000,
    );
    return () => window.clearInterval(timer);
  }, [load]);
  const action = async (path: string, init: RequestInit, message: string) => {
    try {
      const result = await call<{message?: string}>(path, init);
      await load();
      toast(result.message || message, "success");
    } catch (cause) {
      toast(cause instanceof Error ? cause.message : "Action failed", "error");
    }
  };
  const remove = async (item: Download) => {
    try {
      if (item.hash) await call(`/api/downloads/torrent/${encodeURIComponent(item.hash)}`, {method:"DELETE"});
      const jobs = item.files?.length ? item.files : [item];
      for (const job of jobs) if (job.job_id) await call(`/api/downloads/${encodeURIComponent(job.job_id)}`, {method:"DELETE"});
      await load(); toast("Removed download", "success");
    } catch (cause) { toast(cause instanceof Error ? cause.message : "Remove failed", "error"); }
  };
  const organize = async (item: Download) => {
    if (!item.hash) return;
    const platform = window.prompt(
      "Platform? (pc, switch, ps2, ps3, psp, nds, 3ds, wii, ngc, dc, psx, gba, n64, snes, nes, gb, genesis, saturn, xbox, xbox360)",
      "pc",
    );
    if (!platform) return;
    const normalized = platform.trim().toLowerCase();
    const isPC = normalized === "pc";
    try {
      const response = await call<{ success?: boolean; error?: string }>(
        `/api/downloads/organize/${encodeURIComponent(item.hash)}`,
        json("POST", {
          platform: isPC ? "PC" : normalized.toUpperCase(),
          platform_slug: isPC ? "" : normalized,
          is_pc: isPC,
        }),
      );
      if (!response.success)
        throw new Error(response.error ?? "Failed to organize torrent");
      if (item.job_id)
        await call(`/api/downloads/${encodeURIComponent(item.job_id)}`, {
          method: "DELETE",
        });
      await load();
      toast("Organizing…", "success");
    } catch (cause) {
      toast(
        cause instanceof Error ? cause.message : "Failed to organize torrent",
        "error",
      );
    }
  };
  return (
    <section id="tab-downloads">
      <div className="flex justify-between items-center mb-4">
        <h2 className="text-xl font-semibold text-white">Downloads</h2>
        <button
          onClick={() =>
            void action("/api/downloads/clear", json("POST"), "Cleared")
          }
          className="secondary"
        >
          Clear Finished
        </button>
      </div>
      <div id="downloads" className="space-y-3">
        {items.length === 0 && <Empty icon="⬇">No active downloads</Empty>}
        {items.map((item) => {
          const files = item.files || [];
          const isArchive = item.type === "archive" || files.length > 1;
          const archiveKey = item.hash || item.title;
          const progress = item.progress;
          const statusColor: Record<string, string> = {
            downloading: "bg-blue-500/20 text-blue-400",
            completed: "bg-emerald-500/20 text-emerald-400",
            error: "bg-red-500/20 text-red-400",
            organizing: "bg-yellow-500/20 text-yellow-400",
            scanning: "bg-purple-500/20 text-purple-400",
            dead_letter: "bg-red-500/20 text-red-300",
            interrupted: "bg-orange-500/20 text-orange-400",
          };
          const eta =
            item.eta && item.eta > 0 && item.eta < 864000
              ? item.eta > 3600
                ? `${Math.floor(item.eta / 3600)}h ${Math.floor((item.eta % 3600) / 60)}m`
                : `${Math.floor(item.eta / 60)}m`
              : "";
          return (
            <article
              key={item.job_id ?? item.hash ?? item.title}
              className="bg-slate-900 border border-slate-800 rounded-xl p-4"
            >
              <div className="flex items-center justify-between gap-3 mb-2">
                <h3 className="text-sm font-medium text-white break-words flex-1">
                  {item.title}
                </h3>
                <div className="flex gap-1.5 shrink-0">
                  {!isArchive && item.status === "completed_unorganized" && item.hash && (
                    <button
                      onClick={() => void organize(item)}
                      className="secondary"
                    >
                      Organize
                    </button>
                  )}
                  {["error", "interrupted", "dead_letter"].includes(
                    item.status,
                  ) &&
                    !isArchive && item.job_id && item.can_retry && (
                      <button
                        onClick={() =>
                          void action(
                            `/api/downloads/${encodeURIComponent(item.job_id!)}/retry`,
                            json("POST"),
                            "Retrying…",
                          )
                        }
                        className="secondary"
                      >
                        Retry
                      </button>
                    )}
                  {item.hash ? (
                    <button
                      onClick={() => void remove(item)}
                      className="secondary"
                    >
                      Remove
                    </button>
                  ) : (
                    item.job_id && (
                      <button
                        onClick={() =>
                          void action(
                            `/api/downloads/${encodeURIComponent(item.job_id!)}`,
                            { method: "DELETE" },
                            "Dismissed job",
                          )
                        }
                        className="secondary"
                      >
                        Dismiss
                      </button>
                    )
                  )}
                </div>
              </div>
              <div className="flex flex-wrap gap-2 text-xs mb-2">
                <span
                  className={`px-2 py-0.5 rounded font-semibold ${statusColor[item.status] ?? "bg-slate-700 text-slate-400"}`}
                >
                  {item.status}
                </span>
                {item.platform && (
                  <span className="text-slate-500">{item.platform}</span>
                )}
                {item.size && item.size !== "?" && (
                  <span className="text-slate-500">{item.size}</span>
                )}
                {item.speed && !item.speed.startsWith("0") && (
                  <span className="text-slate-500">{item.speed}</span>
                )}
                {eta && <span className="text-slate-500">ETA: {eta}</span>}
                {progress !== undefined && (
                  <span className="text-slate-400">{progress}%</span>
                )}
              </div>
              {progress !== undefined && (
                <div className="bg-slate-800 rounded-full h-1.5 overflow-hidden">
                  <div
                    data-download-progress={progress}
                    className={`progress-bar h-full rounded-full ${progress >= 100 ? "bg-emerald-500" : item.status === "error" ? "bg-red-500" : "bg-indigo-500"}`}
                    style={{
                      width: `${Math.max(0, Math.min(100, progress))}%`,
                    }}
                  />
                </div>
              )}
              {isArchive && <details data-archive-hash={archiveKey} open={archiveOpen[archiveKey] !== false}
                onToggle={event => { const opened=event.currentTarget.open; setArchiveOpen(previous => previous[archiveKey] === opened ? previous : {...previous,[archiveKey]:opened}); }}>
                <summary className="text-xs text-slate-400 cursor-pointer mt-3">{files.length} files in this torrent</summary>
                {files.map(file => <div key={file.job_id || file.title} className="border-t border-slate-800 py-2" data-archive-file={file.job_id || file.title}>
                  <strong className="text-sm">{file.title}</strong>
                  <p className="text-xs text-slate-400">{file.status} {file.platform} {file.size} {file.progress != null ? `${file.progress}%` : ''}</p>
                  {file.detail && <p className="text-xs text-slate-400">{file.detail}</p>}
                  {file.error && <p className="text-xs text-red-400">{file.error}</p>}
                  {file.job_id && <div className="flex gap-2 mt-2">
                    {file.can_retry && ["error","interrupted","dead_letter"].includes(file.status) && <button className="secondary" onClick={() => void action(`/api/downloads/${encodeURIComponent(file.job_id!)}/retry`,json("POST"),"Retrying…")}>Retry</button>}
                    <button className="secondary" onClick={() => void action(`/api/downloads/${encodeURIComponent(file.job_id!)}`,{method:"DELETE"},"Dismissed job")}>Dismiss</button>
                  </div>}
                </div>)}
              </details>}
              {!isArchive && item.detail && (
                <p className="text-xs text-slate-500 mt-1.5">{item.detail}</p>
              )}
              {!isArchive && item.error && (
                <p className="text-xs text-red-400 mt-1">{item.error}</p>
              )}
            </article>
          );
        })}
      </div>
    </section>
  );
}
function WishlistTab({
  call,
  search,
}: {
  call: Call;
  search: (title: string) => void;
}) {
  const platforms = usePlatforms(call);
  const [title, setTitle] = useState("");
  const [platform, setPlatform] = useState("");
  const [items, setItems] = useState<WishlistItem[]>([]);
  const load = useCallback(async () => {
    const data = await call<{ items?: WishlistItem[] }>("/api/wishlist");
    setItems(data.items ?? []);
  }, [call]);
  useEffect(() => {
    void load().catch(() => undefined);
  }, [load]);
  useEffect(() => {
    if (!platform && platforms[0])
      setPlatform(platforms.find((item) => item.id !== "all")?.id ?? "");
  }, [platform, platforms]);
  const add = async () => {
    if (!title.trim()) return;
    await call(
      "/api/wishlist",
      json("POST", {
        title: title.trim(),
        platform_slug: platform,
        platform:
          platforms.find((item) => item.id === platform)?.name ?? platform,
      }),
    );
    setTitle("");
    await load();
  };
  return (
    <section>
      <h2 className="text-xl font-semibold text-white mb-5">Wishlist</h2>
      <div className="flex flex-col sm:flex-row gap-3 mb-5">
        <input
          id="wish-title"
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && void add()}
          placeholder="Game title"
          className="input flex-1"
        />
        <select
          id="wish-platform"
          value={platform}
          onChange={(e) => setPlatform(e.target.value)}
          className="select"
        >
          {platforms
            .filter((item) => item.id !== "all")
            .map((item) => (
              <option key={item.id} value={item.id}>
                {item.name}
              </option>
            ))}
        </select>
        <button
          onClick={() => void add().catch(() => undefined)}
          className="primary"
        >
          Add
        </button>
      </div>
      <div id="wishlist" className="space-y-2">
        {items.map((item) => (
          <div
            key={item.id}
            className="bg-slate-900 border border-slate-800 rounded-lg px-4 py-3 flex gap-3 items-center"
          >
            <div className="flex-1">
              <span className="text-sm text-white">{item.title}</span>
              <span className="text-xs text-slate-500 ml-2">
                {item.platform}
              </span>
            </div>
            <button onClick={() => search(item.title)} className="secondary">
              Search
            </button>
            <button
              onClick={() =>
                void call(`/api/wishlist/${item.id}`, { method: "DELETE" })
                  .then(load)
                  .catch(() => undefined)
              }
              className="secondary"
            >
              Delete
            </button>
          </div>
        ))}
        {items.length === 0 && (
          <Empty icon="☆">No games on your wishlist</Empty>
        )}
      </div>
    </section>
  );
}

function SettingsTab({
  call,
  toast,
}: {
  call: Call;
  toast: (text: string, kind: Toast["kind"]) => void;
}) {
  const [settings, setSettings] = useState<Settings>({});
  const [settingsLoaded, setSettingsLoaded] = useState(false);
  const [connectionStatus, setConnectionStatus] = useState<Record<string,string>>({});
  const [sources, setSources] = useState<
    Array<{ name: string; enabled?: boolean; health?: { download_degraded?: boolean; last_error?: string } }>
  >([]);
  const [stats, setStats] = useState<Stats>({});
  const [monitor, setMonitor] = useState<Monitor>({});
  const [activity, setActivity] = useState<
    Array<{ message?: string; title?: string; created_at?: string }>
  >([]);
  const [check, setCheck] = useState<ImportCheck>();
  const load = useCallback(async () => {
    const [saved, sourceData, statData, monitorData, activityData] =
      await Promise.all([
        call<Settings>("/api/settings"),
        call<{ sources?: Array<{ name: string; enabled?: boolean; health?: { download_degraded?: boolean; last_error?: string } }> }>(
          "/api/sources",
        ),
        call<Stats>("/api/stats"),
        call<Monitor>("/api/monitor/status"),
        call<{
          entries?: Array<{
            message?: string;
            title?: string;
            created_at?: string;
          }>;
        }>("/api/activity"),
      ]);
    setSettings(saved);
    setSources(sourceData.sources ?? []);
    setStats(statData);
    setMonitor(monitorData);
    setActivity(activityData.entries ?? []);
    setSettingsLoaded(true);
  }, [call]);
  useEffect(() => {
    void load().catch(() => undefined);
  }, [load]);
  const save = async (patch: Settings) => {
    const next = { ...settings, ...patch };
    setSettings(next);
    try {
      await call("/api/settings", json("PUT", patch));
    } catch (cause) {
      toast(
        cause instanceof Error ? cause.message : "Could not save settings",
        "error",
      );
    }
  };
  const importCheck = async (mode: string) => {
    if (mode !== "hardlink") {
      setCheck(undefined);
      return;
    }
    try {
      setCheck(await call<ImportCheck>("/api/settings/import-check"));
    } catch {
      setCheck({
        checks: [{ ok: false, error: "Could not check import mode" }],
      });
    }
  };
  const modeHints: Record<string, string> = {
    move: "Files leave the download folder. Torrents stop seeding.",
    hardlink:
      "Links files into the library. Torrents keep seeding, no extra disk. Needs downloads and library under one mount.",
    symlink:
      "Links the library entry to the download. Torrents keep seeding, but the entry breaks if the download is deleted.",
    copy: "Duplicates the files. Torrents keep seeding, at double the disk usage.",
  };
  const failedCheck = check?.checks?.find((item) => !item.ok);
  const firstCheck = check?.checks?.[0];
  useEffect(() => {
    void importCheck(settings.import_mode ?? "move");
  }, [settings.import_mode]);
  const test = async (service: string) => {
    setConnectionStatus(status => ({...status,[service]:"Testing…"}));
    try {
      const data = await call<{ success?: boolean; error?: string; version?: string }>(
        `/api/test/${service}`,
        json("POST"),
      );
      setConnectionStatus(status => ({...status,[service]: data.success ? (data.version ? `Connected (${data.version})` : "Connected") : (data.error || "Failed")}));
      toast(
        data.success
          ? `${service} connected`
          : (data.error ?? `${service} failed`),
        data.success ? "success" : "error",
      );
    } catch (cause) {
      setConnectionStatus(status => ({...status,[service]:cause instanceof Error ? cause.message : "Connection test failed"}));
      toast(
        cause instanceof Error ? cause.message : "Connection test failed",
        "error",
      );
    }
  };
  return (
    <section id="tab-settings">
      <h2 className="text-xl font-semibold text-white mb-5">Settings</h2>
      <div className="grid gap-5 lg:grid-cols-2">
        <Panel title="Connections">
          <div className="grid grid-cols-2 gap-3">
            {["prowlarr", "qbittorrent", "sabnzbd", "nzbget", "flaresolverr"].map((service) => (
              <button
                key={service}
                onClick={() => void test(service)}
                className="secondary text-left"
              >
                Test {service}
                <span id={`test-${service}-status`} className="block text-xs mt-1">{connectionStatus[service]}</span>
              </button>
            ))}
          </div>
        </Panel>
        <Panel title="Sources">
          <div id="settings-sources" className="space-y-2">
            {sources.map((source) => (
              <p key={source.name} className="text-sm text-slate-400">
                {source.name}{" "}
                <span
                  className={
                    source.enabled ? "text-emerald-400" : "text-slate-500"
                  }
                >
                  {source.enabled ? "enabled" : "disabled"}
                </span>
                {source.health?.download_degraded && <span
                  data-source-degraded={source.name}
                  title={source.health.last_error || ''}
                  className="ml-2 px-2 py-0.5 text-xs rounded bg-red-500/20 text-red-400"
                >downloads failing</span>}
              </p>
            ))}
          </div>
        </Panel>
        <Panel title="Download settings">
          <label className="flex gap-3 text-sm text-slate-300">
            <input
              id="setting-extract"
              disabled={!settingsLoaded}
              type="checkbox"
              checked={Boolean(settings.extract_archives)}
              onChange={(e) =>
                void save({ extract_archives: e.target.checked })
              }
            />
            Extract archives
          </label>
          <label className="flex gap-3 mt-4 text-sm text-slate-300">
            <input id="setting-vault-archive" disabled={!settingsLoaded} type="checkbox" checked={Boolean(settings.vault_archive_enabled)}
              onChange={event => void save({vault_archive_enabled:event.target.checked})} />
            Archive PC games in the vault
          </label>
          <label className="block mt-4 text-sm text-slate-300">
            Import mode
            <select
              id="setting-import-mode"
              disabled={!settingsLoaded}
              value={settings.import_mode ?? "move"}
              onChange={(e) => {
                void save({ import_mode: e.target.value });
                void importCheck(e.target.value);
              }}
              className="select w-full mt-2"
            >
              <option value="move">Move (default)</option>
              <option value="hardlink">Hardlink — keep seeding</option>
              <option value="symlink">Symlink — keep seeding</option>
              <option value="copy">Copy — keep seeding</option>
            </select>
          </label>
          <p
            id="import-mode-hint"
            data-mode={settings.import_mode ?? "move"}
            className="text-xs text-slate-500 mt-2"
          >
            {modeHints[settings.import_mode ?? "move"]}
          </p>
          <p
            id="hardlink-check"
            data-status={
              settings.import_mode !== "hardlink"
                ? ""
                : !check
                  ? "checking"
                  : failedCheck
                    ? "failed"
                    : firstCheck
                      ? "ok"
                      : ""
            }
            className={`text-xs mt-2 ${failedCheck ? "text-red-400" : firstCheck ? "text-emerald-400" : "text-slate-500"}`}
          >
            {failedCheck?.error ??
              (firstCheck
                ? `Verified: hardlinks work from ${firstCheck.source} into the library.`
                : "")}
          </p>
        </Panel>
        <Panel title="Stats">
          <div id="settings-stats" className="text-sm text-slate-400">
            Library: {stats.library_total ?? 0} games
          </div>
        </Panel>
        <Panel title="AI Monitor">
          <div className="flex justify-between gap-4">
            <p className="text-sm text-slate-400">
              {monitor.diagnosis ??
                (monitor.enabled ? "Enabled" : "Not enabled")}
            </p>
            <button
              onClick={() =>
                void call("/api/monitor/analyze", json("POST"))
                  .then(() => window.setTimeout(() => void load(), 500))
                  .then(() => toast("Analysis triggered", "success"))
                  .catch((cause) =>
                    toast(
                      cause instanceof Error
                        ? cause.message
                        : "Analysis failed",
                      "error",
                    ),
                  )
              }
              className="primary"
            >
              Analyze Now
            </button>
          </div>
          <div id="monitor-info" className="text-xs text-slate-500 mt-3">
            {monitor.recent_errors?.map((error) => <p key={error}>{error}</p>)}
          </div>
        </Panel>
        <Panel title="Recent Activity">
          <div
            id="activity-log"
            className="space-y-1 text-sm text-slate-400 max-h-64 overflow-y-auto"
          >
            {activity.map((item, index) => (
              <p key={`${item.created_at}-${index}`}>
                {item.title ?? item.message ?? "Activity"}
              </p>
            ))}
          </div>
        </Panel>
      </div>
    </section>
  );
}
function Panel({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="bg-slate-900 border border-slate-800 rounded-xl p-5">
      <h3 className="text-sm font-semibold text-slate-300 uppercase tracking-wider mb-4">
        {title}
      </h3>
      {children}
    </section>
  );
}
