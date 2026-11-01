/**
 * Run an async fetcher when the component mounts and again whenever reactive state
 * read synchronously inside it changes. The previous value stays visible while reloading.
 * Must be called during component initialisation.
 */
export function resource<T>(fetcher: () => Promise<T>) {
  let data = $state<T>();
  let error = $state('');
  let loading = $state(true);

  $effect(() => {
    let stale = false;
    loading = true;
    fetcher().then(
      (value) => {
        if (stale) return;
        data = value;
        error = '';
        loading = false;
      },
      (e: unknown) => {
        if (stale) return;
        error = e instanceof Error ? e.message : String(e);
        loading = false;
      },
    );
    return () => {
      stale = true;
    };
  });

  return {
    get data() {
      return data;
    },
    get error() {
      return error;
    },
    get loading() {
      return loading;
    },
  };
}
