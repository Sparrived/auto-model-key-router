import { QueryClient } from "@tanstack/react-query";
import { ApiError } from "@/lib/api";

const NO_RETRY_STATUS = new Set([401, 403, 404, 409, 422]);

function shouldRetry(failureCount: number, error: unknown): boolean {
  if (error instanceof ApiError && NO_RETRY_STATUS.has(error.status)) return false;
  return failureCount < 1;
}

export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 5_000,
        retry: shouldRetry,
        refetchOnWindowFocus: true,
      },
    },
  });
}

export const queryClient = createQueryClient();
