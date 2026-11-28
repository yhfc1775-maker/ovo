import { useQuery, useMutation, useQueryClient, UseQueryOptions } from "@tanstack/react-query";
import Network from "./network";
import { getForwardList, getSpeedLimitList, getTunnelList } from "./index";

interface ApiResponse<T = unknown> {
  code: number;
  msg: string;
  data: T;
  warning?: string;
}

interface RequestOptions {
  timeout?: number;
  silent?: boolean;
  signal?: AbortSignal;
  idemKey?: string;
}

export const forwardsQueryKey = ["forwards"] as const;
export const tunnelsQueryKey = ["tunnels"] as const;
export const speedLimitsQueryKey = ["speedLimits"] as const;

// 列表接口是 POST（Network.post），不能走 useApiQuery（GET）；直接复用 @/api 的函数。
export function useForwards() {
  return useQuery({ queryKey: forwardsQueryKey, queryFn: () => getForwardList() });
}

export function useTunnels() {
  return useQuery({ queryKey: tunnelsQueryKey, queryFn: () => getTunnelList() });
}

export function useSpeedLimits() {
  return useQuery({ queryKey: speedLimitsQueryKey, queryFn: () => getSpeedLimitList() });
}

export function useApiQuery<T = unknown>(
  queryKey: readonly unknown[],
  path: string,
  params?: unknown,
  options?: Omit<UseQueryOptions<ApiResponse<T>>, "queryKey" | "queryFn"> & RequestOptions,
) {
  const { timeout, silent, signal, ...queryOptions } = options || {};

  return useQuery<ApiResponse<T>>({
    queryKey,
    queryFn: ({ signal: abortSignal }) =>
      Network.get<T>(path, params, {
        timeout,
        silent,
        signal: signal || abortSignal,
      }),
    ...queryOptions,
  });
}

export function useApiMutation<TData = unknown, TVariables = unknown>(
  mutationFn: (variables: TVariables) => Promise<ApiResponse<TData>>,
  options?: {
    onSuccess?: (data: ApiResponse<TData>, variables: TVariables) => void;
    onError?: (error: unknown, variables: TVariables) => void;
    invalidateKeys?: Array<readonly unknown[]>;
  },
) {
  const queryClient = useQueryClient();

  return useMutation<ApiResponse<TData>, unknown, TVariables>({
    mutationFn,
    onSuccess: (data, variables) => {
      if (options?.invalidateKeys) {
        options.invalidateKeys.forEach((key) => {
          queryClient.invalidateQueries({ queryKey: key });
        });
      }
      options?.onSuccess?.(data, variables);
    },
    onError: options?.onError,
  });
}
