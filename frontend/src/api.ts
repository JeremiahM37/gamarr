export type ApiError = Error & { status?: number; body?: ApiFailure };
export interface ApiFailure {
  success?: boolean;
  error?: string;
}
export interface AuthStatus {
  auth_required: boolean;
  authenticated: boolean;
  username?: string;
  has_users?: boolean;
  can_register?: boolean;
  login_available?: boolean;
  oidc_enabled?: boolean;
  oidc_provider?: string;
}
export interface Platform {
  id: string;
  name: string;
}
export interface SearchResult {
  title: string;
  platform: string;
  platform_slug?: string;
  is_pc?: boolean;
  source_type?: string;
  download_protocol?: string;
  indexer?: string;
  seeders?: number;
  leechers?: number;
  size_human?: string;
  safety_score?: number;
  safety_warnings?: string[];
  [key: string]: unknown;
}
export interface SearchResponse {
  results: SearchResult[];
  search_time_ms?: number;
  sources?: Array<{
    name: string;
    enabled?: boolean;
    indexers?: Array<{ name: string }>;
  }>;
}
export interface LibraryItem {
  id: number;
  title: string;
  platform: string;
  platform_slug?: string;
  is_pc?: boolean;
  file_size?: number;
}
export interface LibraryResponse {
  items: LibraryItem[];
  total: number;
  page: number;
  total_pages?: number;
}
export interface Download {
  type: "job" | "torrent" | "archive";
  files?: Omit<Download,"type"|"files">[];
  title: string;
  platform?: string;
  status: string;
  job_id?: string;
  can_retry?: boolean;
  hash?: string;
  progress?: number;
  size?: string;
  speed?: string;
  eta?: number;
  error?: string;
  detail?: string;
}
export interface WishlistItem {
  id: number;
  title: string;
  platform?: string;
  platform_slug?: string;
}
export interface Settings {
  extract_archives?: boolean;
  vault_archive_enabled?: boolean;
  import_mode?: string;
}
export interface ImportCheck {
  import_mode?: string;
  checks?: Array<{
    source?: string;
    destination?: string;
    ok: boolean;
    error?: string;
  }>;
}
export interface Config {
  romm_url?: string;
  gamevault_url?: string;
}
export interface Stats {
  library_total?: number;
  [key: string]: number | undefined;
}
export interface Monitor {
  enabled?: boolean;
  diagnosis?: string;
  recent_errors?: string[];
  pending_actions?: Array<{ id: string; title?: string; message?: string }>;
}

export async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, init);
  const body = (await response.json().catch(() => ({}) as ApiFailure)) as T &
    ApiFailure;
  if (!response.ok) {
    const error = new Error(
      body.error ?? `Request failed (${response.status})`,
    ) as ApiError;
    error.status = response.status;
    error.body = body;
    throw error;
  }
  return body;
}

export const json = (method: string, body?: unknown): RequestInit => ({
  method,
  headers: { "Content-Type": "application/json" },
  body: body === undefined ? undefined : JSON.stringify(body),
});
